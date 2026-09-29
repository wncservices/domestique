// Package indoor turns a cycling workout into its trainer-friendly version.
//
// A road session assumes the road: a long ride is three or four hours, an
// endurance step is a heart-rate or power range, and a hand-built workout may
// be measured in kilometres or left open. A smart trainer in ERG mode can only
// hold an absolute wattage for a length of time, so the conversion makes every
// step time-based, gives it a power target where FTP is known, and shortens
// the easy and long rides (an hour indoors is worth more than an hour
// outdoors: no coasting, junctions or descents).
//
// Pure: given a workout and a profile it returns steps and a note. Nothing
// here stores anything, and it imports no store, so the rules can be read and
// tested on their own. The invariant it leaves behind — time-based steps,
// power in watts, FTP on the profile — is also all a future Zwift writer would
// need; nothing is built for that.
package indoor

import (
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// ErrNotCycling is returned for a workout that is not a cycling session:
// a treadmill version is out of scope.
var ErrNotCycling = errors.New("indoor: only cycling workouts have an indoor version")

// The conversion constants. Every one is a heuristic, not a standard, and is
// meant to be easy to retune.
const (
	// DistanceSpeedMS turns a distance step into a time step: 28 km/h, a
	// moderate road pace.
	DistanceSpeedMS = 7.8
	// OtherOpenSeconds is the length given to an open-duration step that is
	// not a warmup or cooldown; those get the library's own 10 minutes.
	OtherOpenSeconds = 300
	// EnduranceFactor scales an easy or long ride's main work: an outdoor
	// hour becomes 45 minutes, the middle of the common 1.25 to 1.5 rule of
	// thumb (1 / 1.33 = 0.75).
	EnduranceFactor = 0.75
	// MaxMainSeconds caps the main work at two hours, MinMainSeconds floors
	// it at 30 minutes (never longer than the original), RoundSeconds is the
	// rounding step.
	MaxMainSeconds = 7200
	MinMainSeconds = 1800
	RoundSeconds   = 300
)

// Power bands, as fractions of FTP, for an HR step that is not the workout's
// main work. They are the library's own (workoutlib.Warmup/Cooldown and its
// rest between reps), so a converted session warms up like a generated one.
const (
	warmupLowPct, warmupHighPct     = 0.50, 0.65
	cooldownLowPct, cooldownHighPct = 0.45, 0.55
	restLowPct, restHighPct         = 0.45, 0.55
	enduranceLowPct, enduranceHigh  = 0.55, 0.75
)

// Result is what converting a workout produces.
type Result struct {
	Steps []workout.WorkoutStep
	Note  string
	// ERG is true when the trainer can hold every step it is asked to: each
	// step that is neither open nor a warmup has a power target, and there is
	// at least one. The UI says "Trainer control (ERG)" or "Ride by feel".
	ERG bool
	// Changed reports whether Steps differ from the workout's own. An FTP
	// test, or a workout already indoor, comes back unchanged.
	Changed bool
}

// Convert returns w's indoor version. Whether ranges collapse to a midpoint
// comes from the profile's SmartTrainer, so no caller can pass it inconsistently.
// It is idempotent: an already-indoor
// workout comes back as it is, so a double click or a retried request cannot
// shorten a ride twice.
func Convert(w workout.Workout, p workout.RiderProfile) (Result, error) {
	if w.Sport != model.SportCycling {
		return Result{}, ErrNotCycling
	}
	steps := cloneSteps(w.Steps)

	if w.Indoor {
		return Result{Steps: steps, ERG: ergFor(w, steps)}, nil
	}
	if isTest(w) {
		return Result{
			Steps: steps,
			ERG:   ergFor(w, steps),
			Note:  fmt.Sprintf("Indoor version of %s: an FTP test is already trainer-ready, so its steps are unchanged.", w.Name),
		}, nil
	}

	c := converter{w: w, p: p, smart: p.SmartTrainer, band: zoneBand(w)}
	steps = c.convert(steps)
	unscaled := workout.PlannedSeconds(steps)

	shortened := false
	if !scheduler.IsHardSession(w) && !hasIntervalWork(steps) {
		steps, shortened = shorten(steps)
	}

	erg := ergFor(w, steps)
	return Result{
		Steps:   steps,
		Note:    note(w, steps, unscaled, shortened, erg, p),
		ERG:     erg,
		Changed: !reflect.DeepEqual(steps, w.Steps),
	}, nil
}

// isTest reports whether w is an FTP test, whose steps are already what a
// trainer needs: the ramp is absolute watts for ERG, and the 20-minute and
// 2 x 8-minute efforts stay open (resistance mode, because ERG would lock the
// watts and measure the target rather than the rider).
func isTest(w workout.Workout) bool { return w.TestProtocol != "" }

type converter struct {
	w     workout.Workout
	p     workout.RiderProfile
	smart bool
	// band is the FTP fraction an HR step of the workout's main work maps to;
	// zero when the zone is unknown, in which case HR steps stay HR.
	band [2]float64
}

// zoneBand is the power band of w's zone: the endurance bucket uses the band
// scheduler.enduranceZoneTarget uses, a structured zone its ladder's.
func zoneBand(w workout.Workout) [2]float64 {
	switch {
	case w.Zone == workout.ZoneEndurance:
		return [2]float64{enduranceLowPct, enduranceHigh}
	case workout.IsStructuredZone(w.Zone):
		if l, ok := workoutlib.LadderFor(model.SportCycling, string(w.Zone)); ok {
			return [2]float64{l.LowPct, l.HighPct}
		}
	}
	return [2]float64{}
}

func (c converter) convert(steps []workout.WorkoutStep) []workout.WorkoutStep {
	for i := range steps {
		s := &steps[i]
		if s.Repeat >= 2 {
			s.Steps = c.convert(s.Steps)
			continue
		}
		c.duration(s)
		c.target(s)
	}
	return steps
}

func (c converter) duration(s *workout.WorkoutStep) {
	switch s.Duration {
	case workout.DurationDistance:
		s.Seconds = math.Round(s.Meters / DistanceSpeedMS)
		s.Meters = 0
		s.Duration = workout.DurationTime
	case workout.DurationOpen:
		s.Seconds = OtherOpenSeconds
		if s.Intensity == workout.IntensityWarmup || s.Intensity == workout.IntensityCooldown {
			s.Seconds = workoutlib.WarmupCooldownSeconds / 2
		}
		s.Duration = workout.DurationTime
	}
}

func (c converter) target(s *workout.WorkoutStep) {
	if s.Target == workout.TargetHeartRate && c.p.FTPWatts > 0 {
		if low, high, ok := c.powerBand(s.Intensity); ok {
			s.Target, s.TargetLow, s.TargetHigh = workout.TargetPower, c.p.FTPWatts*low, c.p.FTPWatts*high
		}
		// No band (zone unknown): the HR target stays. Inventing a wattage
		// for a session whose intensity is not known would be a guess.
	}
	// ERG holds one number, and how a range is resolved differs by device, so
	// a smart-trainer rider gets the midpoint. Without the preference the
	// range stays: a power-meter rider on rollers wants the zone, not a lock.
	if c.smart && s.Target == workout.TargetPower && s.TargetLow != s.TargetHigh {
		mid := math.Round((s.TargetLow + s.TargetHigh) / 2)
		s.TargetLow, s.TargetHigh = mid, mid
	}
}

func (c converter) powerBand(in workout.Intensity) (low, high float64, ok bool) {
	switch in {
	case workout.IntensityWarmup:
		return warmupLowPct, warmupHighPct, true
	case workout.IntensityCooldown:
		return cooldownLowPct, cooldownHighPct, true
	case workout.IntensityRest, workout.IntensityRecovery:
		return restLowPct, restHighPct, true
	}
	if c.band == [2]float64{} {
		return 0, 0, false
	}
	return c.band[0], c.band[1], true
}

// hasIntervalWork reports whether the steps hold repeat blocks or interval
// intensity: structured work, which is never scaled even when the workout
// carries no zone (a hand-built session) and so IsHardSession says no.
func hasIntervalWork(steps []workout.WorkoutStep) bool {
	for _, s := range steps {
		if s.Repeat >= 2 || s.Intensity == workout.IntensityInterval {
			return true
		}
	}
	return false
}

// shorten scales the active steps to EnduranceFactor of their length, rounded
// to five minutes, capped at two hours of main work and floored at 30 minutes,
// never longer than the original. Warmup, cooldown and any other step are
// kept. It reports whether anything changed.
func shorten(steps []workout.WorkoutStep) ([]workout.WorkoutStep, bool) {
	var main float64
	for _, s := range steps {
		if s.Intensity == workout.IntensityActive && s.Duration == workout.DurationTime {
			main += s.Seconds
		}
	}
	if main <= 0 {
		return steps, false
	}
	target := math.Round(main*EnduranceFactor/RoundSeconds) * RoundSeconds
	target = math.Min(math.Max(target, MinMainSeconds), MaxMainSeconds)
	target = math.Min(target, main)
	if target >= main {
		return steps, false
	}
	f := target / main
	for i := range steps {
		s := &steps[i]
		if s.Intensity == workout.IntensityActive && s.Duration == workout.DurationTime {
			s.Seconds = math.Round(s.Seconds * f)
		}
	}
	return steps, true
}

// ergFor reports whether a trainer in ERG mode can drive steps. An FTP test
// follows its protocol's own trainer mode: the 20-minute and 2 x 8-minute
// efforts are resistance mode by design, whatever their other steps say.
func ergFor(w workout.Workout, steps []workout.WorkoutStep) bool {
	if isTest(w) {
		for _, p := range fitnesstest.Protocols() {
			if p.ID == w.TestProtocol && p.TrainerMode != fitnesstest.TrainerModeERG {
				return false
			}
		}
	}
	power, other := countTargets(steps)
	return power > 0 && other == 0
}

// countTargets counts power steps and steps ERG cannot drive: any that is
// neither open-targeted nor a warmup, and not power.
func countTargets(steps []workout.WorkoutStep) (power, other int) {
	for _, s := range steps {
		if s.Repeat >= 2 {
			p, o := countTargets(s.Steps)
			power, other = power+p, other+o
			continue
		}
		switch {
		case s.Target == workout.TargetPower:
			power++
		case s.Target == workout.TargetOpen, s.Intensity == workout.IntensityWarmup:
		default:
			other++
		}
	}
	return power, other
}

func hasHeartRateTarget(steps []workout.WorkoutStep) bool {
	for _, s := range steps {
		if s.Target == workout.TargetHeartRate || (s.Repeat >= 2 && hasHeartRateTarget(s.Steps)) {
			return true
		}
	}
	return false
}

func note(w workout.Workout, steps []workout.WorkoutStep, unscaled float64, shortened, erg bool, p workout.RiderProfile) string {
	total := workout.PlannedSeconds(steps)
	var out string
	if orig := unscaled; orig > 0 {
		out = fmt.Sprintf("Indoor version of %s (%s), %s on the trainer.", w.Name, duration(orig), duration(total))
	} else {
		out = fmt.Sprintf("Indoor version of %s.", w.Name)
	}
	if shortened {
		out += " The rest can be ridden outside on another day."
	}
	if hasHeartRateTarget(steps) {
		if p.FTPWatts <= 0 && !erg {
			out += " No FTP set, so the trainer cannot control resistance. Ride by heart rate."
		} else if p.FTPWatts > 0 {
			// FTP is known but the zone is not, so there is nothing to turn the
			// heart-rate target into: inventing a wattage would be a guess.
			out += " Heart-rate steps stay as heart rate because the session's zone is not known, so the trainer cannot control them."
		}
	}
	return out
}

// duration formats seconds as "2h15", or "45 min" under an hour.
func duration(seconds float64) string {
	mins := int(math.Round(seconds / 60))
	if mins < 60 {
		return fmt.Sprintf("%d min", mins)
	}
	return fmt.Sprintf("%dh%02d", mins/60, mins%60)
}

func cloneSteps(in []workout.WorkoutStep) []workout.WorkoutStep {
	if in == nil {
		return nil
	}
	out := make([]workout.WorkoutStep, len(in))
	for i, s := range in {
		out[i] = s
		out[i].Steps = cloneSteps(s.Steps)
	}
	return out
}
