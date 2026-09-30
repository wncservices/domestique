// Package trainnow answers "I have N minutes today": up to three sessions that
// fit the time, shaped by readiness and recent load.
//
// Pure, like internal/alternates: it is handed what the caller has already read
// (today's session, the week's zones, the rider's levels and verdict) and
// returns suggestions. Nothing here reads or writes anything, and nothing it
// returns is applied by it: a suggestion is a proposal the rider accepts with a
// POST (see internal/api). The rules are the alternates design's, "I have N
// minutes today" section.
package trainnow

import (
	"fmt"
	"math"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/alternates"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// Kind names one of the three suggestions.
type Kind string

const (
	// Planned is today's plan fitted to N.
	Planned Kind = "planned"
	// Wanted is a zone the plan wants this week that is not done yet.
	Wanted Kind = "wanted"
	// Easy is an endurance ride of N minutes.
	Easy Kind = "easy"
)

// The bounds of the time a rider can ask for, in minutes.
const (
	MinMinutes = 30
	MaxMinutes = 180
)

const (
	// restCapMinutes is the longest ride suggested on a rest verdict.
	restCapMinutes = 60
	// enduranceCap keeps an endurance stand-in from growing far past the
	// session it stands in for.
	enduranceCap = 1.25
	// hardDaysBeforeWarning is how many hard days in the last 7 make one more a
	// third.
	hardDaysBeforeWarning = 2

	// HardDayWarning is what `planned` carries after two hard days in 7.
	HardDayWarning = "A third hard day in 7"

	roundSeconds = 5 * 60
)

// Input is everything Suggest needs; the caller reads it all.
type Input struct {
	Minutes int
	// Level is the rider's current level for a zone.
	Level   func(zone string) float64
	Profile workout.RiderProfile
	// Today is the session `planned` stands in for: today's plan-made undone
	// session, else the next undone plan-made one this week (PlannedIsToday
	// says which). nil when there is none. A session that is not plan-made, or
	// an FTP test, is never used.
	Today          *workout.Workout
	PlannedIsToday bool
	// WeekZones are the structured zones the plan puts in this week, in plan
	// order; DoneZones those already done this week.
	WeekZones []string
	DoneZones map[string]bool
	// HardDaysLast7 counts the days in the last 7 (before today) with a
	// completed structured session.
	HardDaysLast7 int
	// Verdict is today's readiness ("" behaves as ready: no wellness data).
	Verdict          readiness.Verdict
	ReadinessReasons []string
	Sport            model.Sport
	// HasPlan is whether the rider has a focus goal. Without one only `easy` is
	// offered.
	HasPlan bool
}

// Suggestion is one session that fits the time.
type Suggestion struct {
	Kind       Kind
	Sport      model.Sport
	Name       string
	Zone       workout.Zone
	Level      float64
	Seconds    float64
	TSS        float64
	Difficulty string
	Why        string
	Warning    string
	Steps      []workout.WorkoutStep
}

// Suggest returns up to three suggestions, each fitting Input.Minutes, in the
// order planned, wanted, easy. Readiness and load shape all three: rest gives
// only an easy ride (capped at an hour), caution takes structured sessions one
// rung down and drops `wanted`, and two hard days in the last 7 drop `wanted`
// and warn on `planned`.
func Suggest(in Input) []Suggestion {
	if in.Level == nil {
		in.Level = func(string) float64 { return 3 }
	}
	if in.Sport == "" {
		in.Sport = model.SportCycling
	}
	if in.Verdict == readiness.Rest {
		s := easy(in, math.Min(float64(in.Minutes), restCapMinutes))
		s.Why = restWhy(in.ReadinessReasons)
		return []Suggestion{s}
	}
	if !in.HasPlan {
		return []Suggestion{easy(in, float64(in.Minutes))}
	}

	caution := in.Verdict == readiness.Caution
	hardLoad := !caution && in.HardDaysLast7 >= hardDaysBeforeWarning

	var out []Suggestion
	plannedZone := ""
	if p, ok := planned(in, caution); ok {
		if hardLoad && workout.IsStructuredZone(in.Today.Zone) {
			p.Warning = HardDayWarning
		}
		out = append(out, p)
	}
	if in.Today != nil && alternates.Plannable(*in.Today) && in.Today.TestProtocol == "" {
		plannedZone = string(in.Today.Zone)
	}
	if !caution && !hardLoad {
		if w, ok := wanted(in, plannedZone); ok {
			out = append(out, w)
		}
	}
	out = append(out, easy(in, float64(in.Minutes)))
	return dedupe(out)
}

// planned is today's session fitted to N: a structured one at the rider's level
// in its zone (one rung down under caution), an endurance or long one with its
// time fitted under the 1.25 cap. It is the plan, shortened or lengthened for
// the day.
func planned(in Input, caution bool) (Suggestion, bool) {
	t := in.Today
	if t == nil || !alternates.Plannable(*t) {
		return Suggestion{}, false
	}
	when := "The next session on your plan"
	if in.PlannedIsToday {
		when = "Today's plan"
	}

	// The session's own sport decides its ladder, whatever the goal's is.
	if t.Sport != "" {
		in.Sport = t.Sport
	}
	if workout.IsStructuredZone(t.Zone) {
		s, fits, ok := structured(in, string(t.Zone), caution)
		if !ok {
			return Suggestion{}, false // no ladder for this sport and zone
		}
		if fits {
			s.Kind = Planned
			s.Why = fmt.Sprintf("%s, %s, fitted to your %d minutes.", when, zoneLabel(string(t.Zone)), in.Minutes)
			return s, true
		}
		// Even rung 1 does not fit: the day becomes an endurance ride, still
		// the plan's day and never longer than 1.25 x what it replaces.
		s = endurance(in, t, alternates.OutdoorSeconds(*t))
		s.Kind = Planned
		s.Why = fmt.Sprintf("%s does not fit %d minutes, so an endurance ride instead.", when, in.Minutes)
		return s, true
	}

	s := endurance(in, t, alternates.OutdoorSeconds(*t))
	s.Kind = Planned
	s.Why = fmt.Sprintf("%s, in the time you have.", when)
	return s, true
}

// wanted is the first zone the plan puts in this week that has no completed
// session yet, other than the one `planned` covers, at the rider's level fitted
// to N. A zone that will not fit even at rung 1 is dropped rather than demoted:
// an endurance ride here would be `easy` under another name.
func wanted(in Input, plannedZone string) (Suggestion, bool) {
	for _, zone := range in.WeekZones {
		if zone == plannedZone || in.DoneZones[zone] {
			continue
		}
		s, fits, ok := structured(in, zone, false)
		if !ok || !fits {
			continue
		}
		s.Kind = Wanted
		s.Why = fmt.Sprintf("Your plan has %s this week and you have not done it yet.", zoneLabel(zone))
		return s, true
	}
	return Suggestion{}, false
}

// structured picks the rung of zone's ladder closest to the rider's target
// level (one rung down under caution, floor 1) among those that fit N. ok is
// false when there is no ladder for the sport and zone; fits is false when even
// rung 1 is too long.
func structured(in Input, zone string, caution bool) (s Suggestion, fits, ok bool) {
	ladder, has := workoutlib.LadderFor(in.Sport, zone)
	if !has {
		return Suggestion{}, false, false
	}
	l := in.Level(zone)
	target := l
	if caution {
		target = math.Max(1, l-1)
	}
	rung, fits := workoutlib.Pick(ladder, target, float64(in.Minutes)*60)
	if !fits {
		return Suggestion{}, false, true
	}
	req := workoutlib.Instantiate(ladder, rung, in.Profile)
	return Suggestion{
		Sport: in.Sport,
		Name:  req.Name, Zone: req.Zone, Level: req.Level, Seconds: workoutlib.TotalSeconds(rung),
		TSS:        tss(in.Sport, req.Steps, in.Profile),
		Difficulty: alternates.Difficulty(req.Level, l),
		Steps:      req.Steps,
	}, true, true
}

// endurance is a ride shaped like stand-in, N minutes long but never more than
// 1.25 x standInSeconds. The cap wins over the 30-minute floor an endurance ride
// otherwise has (a 20-minute stand-in gives 25 minutes, not 30); the one hard
// floor is the fixed warmup and cooldown, and N bounds everything because the
// ride has to fit.
func endurance(in Input, stand *workout.Workout, standInSeconds float64) Suggestion {
	seconds := float64(in.Minutes) * 60
	if standInSeconds > 0 {
		limit := math.Floor(enduranceCap*standInSeconds/roundSeconds) * roundSeconds
		seconds = math.Min(seconds, limit)
	}
	seconds = math.Max(seconds, math.Min(workoutlib.WarmupCooldownSeconds, float64(in.Minutes)*60))
	req := alternates.EnduranceRide(*stand, seconds, in.Profile)
	s := enduranceSuggestion(req, stand.Sport, seconds, alternates.LabelAchievable, in.Profile)
	s.Sport = stand.Sport
	return s
}

// easy is an endurance ride of exactly the given minutes.
func easy(in Input, minutes float64) Suggestion {
	seconds := minutes * 60
	req := scheduler.BuildEnduranceSession(minutes/60, false, in.Sport, in.Profile)
	s := enduranceSuggestion(req, in.Sport, seconds, alternates.LabelRecovery, in.Profile)
	s.Kind, s.Sport = Easy, in.Sport
	s.Why = "An easy ride in the time you have."
	return s
}

func enduranceSuggestion(req workout.CreateWorkoutRequest, sport model.Sport, seconds float64, difficulty string, profile workout.RiderProfile) Suggestion {
	return Suggestion{
		Name: req.Name, Zone: req.Zone, Seconds: seconds,
		TSS: tss(sport, req.Steps, profile), Difficulty: difficulty, Steps: req.Steps,
	}
}

func restWhy(reasons []string) string {
	if len(reasons) == 0 {
		return "Readiness says rest today, so only an easy ride."
	}
	return "Readiness says rest today: " + strings.Join(reasons, "; ") + "."
}

func tss(sport model.Sport, steps []workout.WorkoutStep, profile workout.RiderProfile) float64 {
	return alternates.TSS(sport, steps, profile.FTPWatts)
}

func zoneLabel(zone string) string { return strings.ReplaceAll(zone, "_", " ") }

// dedupe drops a suggestion that lands on the same zone, level and minutes as
// an earlier one.
func dedupe(in []Suggestion) []Suggestion {
	type key struct {
		zone    workout.Zone
		level   float64
		minutes int
	}
	seen := map[key]bool{}
	out := make([]Suggestion, 0, len(in))
	for _, s := range in {
		k := key{s.Zone, s.Level, int(math.Round(s.Seconds / 60))}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}
