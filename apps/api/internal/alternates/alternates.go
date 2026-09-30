// Package alternates computes the easier, harder, shorter and longer versions
// of a plan-made session, each with a predicted difficulty.
//
// Pure, like internal/workoutlib which it reads: given a workout, the rider's
// level for its zone and a profile it returns options, and nothing here
// reads or writes anything. Applying one is internal/api's job. The rules
// (rung arithmetic, the level + 1 cap, the 75 % / 125 % thresholds, the
// recovery and taper restriction) are the alternates design's; see
// docs/superpowers/specs/2026-09-29-alternates-design.md.
package alternates

import (
	"math"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// Kind names one of the four alternates.
type Kind string

const (
	Easier  Kind = "easier"
	Harder  Kind = "harder"
	Shorter Kind = "shorter"
	Longer  Kind = "longer"
)

// Valid reports whether k is one of the four kinds.
func (k Kind) Valid() bool {
	switch k {
	case Easier, Harder, Shorter, Longer:
		return true
	}
	return false
}

// The predicted-difficulty labels. They are TrainerRoad's vocabulary mapped
// onto the one signal this app has, the distance between a rung and the
// rider's level; they are not TrainerRoad's model.
const (
	LabelRecovery     = "Recovery"
	LabelAchievable   = "Achievable"
	LabelProductive   = "Productive"
	LabelStretch      = "Stretch"
	LabelBreakthrough = "Breakthrough"
)

// The endurance scaling. The floor is the fixed warmup and cooldown plus a
// ten-minute main step.
const (
	shorterFactor = 0.75
	longerFactor  = 1.25

	enduranceRoundSeconds = 5 * 60
	enduranceMinSeconds   = workoutlib.WarmupCooldownSeconds + 10*60
	enduranceMaxSeconds   = 6 * 3600
)

// Option is one offered alternate. Steps are always the outdoor form: an
// indoor session's caller converts them again (see internal/indoor).
type Option struct {
	Kind Kind
	Name string
	Zone workout.Zone
	// Level is the rung, 0 for an endurance or long ride.
	Level      float64
	Seconds    float64
	TSS        float64
	Difficulty string
	Steps      []workout.WorkoutStep
}

// Difficulty labels a rung against the rider's level: d = rung - level.
func Difficulty(rung, riderLevel float64) string {
	d := rung - riderLevel
	switch {
	case d <= -2:
		return LabelRecovery
	case d <= -0.5:
		return LabelAchievable
	case d < 0.5:
		return LabelProductive
	case d < 1:
		return LabelStretch
	default:
		return LabelBreakthrough
	}
}

// Plannable reports whether w is the kind of session that has alternates at
// all: made by the plan (a goal and the generated description prefix, so also
// one an automatic adjustment or an earlier swap changed), not an FTP test,
// with a structured zone or zone endurance. Whether it is still ahead of the
// rider is Available's question.
func Plannable(w workout.Workout) bool {
	if w.GoalID == "" || !strings.HasPrefix(w.Description, scheduler.GeneratedDescription) {
		return false
	}
	if w.TestProtocol != "" {
		return false
	}
	return workout.IsStructuredZone(w.Zone) || w.Zone == workout.ZoneEndurance
}

// Available is Plannable plus the clock: dated today or later (today is
// "YYYY-MM-DD") and not ridden. done is adapter.WorkoutDone's answer, passed
// in so this package needs no completed sessions.
func Available(w workout.Workout, today string, done bool) bool {
	return Plannable(w) && !done && w.Date != "" && w.Date >= today
}

// outdoorSteps is the form alternates are computed from: an indoor session
// stores its trainer steps in Steps and the road ones in OutdoorSteps.
func outdoorSteps(w workout.Workout) []workout.WorkoutStep {
	if w.OutdoorSteps != nil {
		return *w.OutdoorSteps
	}
	return w.Steps
}

// OutdoorSeconds is how long w takes in its outdoor form, what its alternates
// (and internal/trainnow's stand-in for it) are sized from.
func OutdoorSeconds(w workout.Workout) float64 {
	return workout.PlannedSeconds(outdoorSteps(w))
}

// EnduranceRide builds an endurance or long ride of totalSeconds in the shape
// of w (a long ride stays a long ride, a run stays a run), through the
// scheduler's own builder.
func EnduranceRide(w workout.Workout, totalSeconds float64, profile workout.RiderProfile) workout.CreateWorkoutRequest {
	return scheduler.BuildEnduranceSession(totalSeconds/3600, isLong(w, totalSeconds, profile), w.Sport, profile)
}

// isLong reports whether w is the week's long day, which the builder names
// differently ("Long ride" against "Endurance ride"): asking it for a long
// session of any length and comparing names avoids a second copy of the names.
func isLong(w workout.Workout, totalSeconds float64, profile workout.RiderProfile) bool {
	return scheduler.BuildEnduranceSession(totalSeconds/3600, true, w.Sport, profile).Name == w.Name
}

// Options returns the alternates on offer for w, in the order easier, harder,
// shorter, longer, omitting any that has no rung behind it. reducedWeek is a
// recovery or taper week: the plan is shedding load there, so no harder and no
// structured longer (an endurance or long ride still scales its time). A
// session that is not Plannable gets nil.
func Options(w workout.Workout, riderLevel float64, reducedWeek bool, profile workout.RiderProfile) []Option {
	if !Plannable(w) {
		return nil
	}
	if w.Zone == workout.ZoneEndurance {
		return enduranceOptions(w, profile)
	}
	return structuredOptions(w, riderLevel, reducedWeek, profile)
}

func structuredOptions(w workout.Workout, riderLevel float64, reducedWeek bool, profile workout.RiderProfile) []Option {
	ladder, ok := workoutlib.LadderFor(w.Sport, string(w.Zone))
	if !ok {
		return nil
	}
	curLevel := int(math.Round(w.Level))
	if curLevel < 1 || curLevel > len(ladder.Rungs) {
		return nil
	}
	cur := ladder.Rungs[curLevel-1]
	curTotal := workoutlib.TotalSeconds(cur)
	// The harderCap is the rider's level + 1, not the session's: a session the plan
	// set below the rider (a recovery week, an eased one) can be swapped back
	// up to where they are, and never past one rung beyond it.
	harderCap := int(math.Floor(riderLevel)) + 1

	rung := func(kind Kind, r workoutlib.Rung) Option {
		req := workoutlib.Instantiate(ladder, r, profile)
		return Option{
			Kind: kind, Name: req.Name, Zone: req.Zone, Level: req.Level,
			Seconds:    workoutlib.TotalSeconds(r),
			TSS:        TSS(w.Sport, req.Steps, profile.FTPWatts),
			Difficulty: Difficulty(req.Level, riderLevel),
			Steps:      req.Steps,
		}
	}

	var out []Option
	if curLevel > 1 {
		out = append(out, rung(Easier, ladder.Rungs[curLevel-2]))
	}
	if !reducedWeek && curLevel < len(ladder.Rungs) && curLevel+1 <= harderCap {
		out = append(out, rung(Harder, ladder.Rungs[curLevel]))
	}

	// Shorter: the longest rung at or below the current level that is at most
	// 75 % of the current total. Ladder durations are not monotone in level,
	// so this is a search, not "one rung down".
	var shorter *workoutlib.Rung
	for i := range ladder.Rungs[:curLevel] {
		r := ladder.Rungs[i]
		if workoutlib.TotalSeconds(r) > shorterFactor*curTotal {
			continue
		}
		if shorter == nil || workoutlib.TotalSeconds(r) >= workoutlib.TotalSeconds(*shorter) {
			shorter = &r
		}
	}
	if shorter != nil {
		out = append(out, rung(Shorter, *shorter))
	}

	// Longer: the shortest rung at or above the current level, within the
	// harder cap, that is at least 125 % of the current total.
	if !reducedWeek {
		var longer *workoutlib.Rung
		for i := curLevel - 1; i < len(ladder.Rungs) && ladder.Rungs[i].Level <= harderCap; i++ {
			r := ladder.Rungs[i]
			if workoutlib.TotalSeconds(r) < longerFactor*curTotal {
				continue
			}
			if longer == nil || workoutlib.TotalSeconds(r) < workoutlib.TotalSeconds(*longer) {
				longer = &r
			}
		}
		if longer != nil {
			out = append(out, rung(Longer, *longer))
		}
	}
	return out
}

// enduranceOptions scales the main step of an endurance or long ride to 75 %
// and 125 %, through the scheduler's own builder so a rescaled ride is exactly
// the shape a generated one is. There is no easier or harder: endurance has no
// ladder.
func enduranceOptions(w workout.Workout, profile workout.RiderProfile) []Option {
	steps := outdoorSteps(w)
	total := workout.PlannedSeconds(steps)
	if total <= 0 {
		return nil
	}
	main := total - workoutlib.WarmupCooldownSeconds
	if main < 0 {
		main = 0
	}
	var out []Option
	for _, c := range []struct {
		kind   Kind
		factor float64
	}{{Shorter, shorterFactor}, {Longer, longerFactor}} {
		seconds := scaleEndurance(main, c.factor)
		if seconds == math.Round(total) {
			continue // clamped back to what it already is
		}
		req := EnduranceRide(w, seconds, profile)
		out = append(out, Option{
			Kind: c.kind, Name: req.Name, Zone: req.Zone,
			Seconds:    seconds,
			TSS:        TSS(w.Sport, req.Steps, profile.FTPWatts),
			Difficulty: LabelAchievable,
			Steps:      req.Steps,
		})
	}
	return out
}

// scaleEndurance is the new total of a ride whose main step is main seconds,
// scaled by factor: the fixed warmup and cooldown back on, rounded to 5
// minutes, floored at 30 minutes and capped at 6 hours.
func scaleEndurance(main, factor float64) float64 {
	seconds := workoutlib.WarmupCooldownSeconds + main*factor
	seconds = math.Round(seconds/enduranceRoundSeconds) * enduranceRoundSeconds
	return math.Min(math.Max(seconds, enduranceMinSeconds), enduranceMaxSeconds)
}

// TSS is the planned TSS of steps for sport (adapter.PlannedTSS, the step-by-step
// estimator the race-day projection shares), 0 without an FTP or for a run.
// Shared by the alternates menu and "I have N minutes" so they price alike.
func TSS(sport model.Sport, steps []workout.WorkoutStep, ftpWatts float64) float64 {
	tss, _ := adapter.PlannedTSS(workout.Workout{Sport: sport, Steps: steps}, ftpWatts)
	return tss
}
