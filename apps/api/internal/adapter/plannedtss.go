package adapter

import (
	"math"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// PlannedTSS is the TSS a planned cycling workout would score if ridden exactly
// as written, sized step by step so a structured session is priced by its work
// and not by its warm-up (which is what EstimatePlannedTSS reads, and why that
// one is left for callers that only ask "is today hard"). Per leaf step,
// descending into repeats: watts are the midpoint of a power target, otherwise
// a fixed fraction of FTP by intensity (warm-up and cool-down 0.55, rest and
// recovery 0.45, everything else 0.65, the same default as defaultPlannedIF).
// Normalised power is Coggan's fourth-power mean,
// NP = (sum(t*P^4)/sum(t))^(1/4), then IF = NP/FTP and TSS = hours*IF^2*100;
// the 30-second smoothing is ignored because steps are minutes long.
// Distance-based and open steps have no duration and count 0. ok is false when
// nothing could be sized: no FTP, not a cycling workout, or no time steps. This
// is the estimator the race-day projection specifies; alternates and "I have N
// minutes" share it.
func PlannedTSS(w workout.Workout, ftpWatts float64) (tss float64, ok bool) {
	if ftpWatts <= 0 || w.Sport != model.SportCycling {
		return 0, false
	}
	var seconds, weighted float64
	var walk func([]workout.WorkoutStep)
	walk = func(steps []workout.WorkoutStep) {
		for _, s := range steps {
			if s.Repeat > 1 {
				for i := 0; i < s.Repeat; i++ {
					walk(s.Steps)
				}
				continue
			}
			if s.Duration != workout.DurationTime || s.Seconds <= 0 {
				continue
			}
			seconds += s.Seconds
			weighted += s.Seconds * math.Pow(plannedStepWatts(s, ftpWatts), 4)
		}
	}
	walk(w.Steps)
	if seconds <= 0 {
		return 0, false
	}
	np := math.Pow(weighted/seconds, 0.25)
	intensity := np / ftpWatts
	return seconds / 3600 * intensity * intensity * 100, true
}

// plannedStepWatts is one step's assumed power: its power target's midpoint, or
// the fixed fraction of FTP its intensity implies.
func plannedStepWatts(s workout.WorkoutStep, ftpWatts float64) float64 {
	if s.Target == workout.TargetPower && s.TargetHigh > 0 {
		return (s.TargetLow + s.TargetHigh) / 2
	}
	switch s.Intensity {
	case workout.IntensityWarmup, workout.IntensityCooldown:
		return 0.55 * ftpWatts
	case workout.IntensityRest, workout.IntensityRecovery:
		return 0.45 * ftpWatts
	default:
		return defaultPlannedIF * ftpWatts
	}
}
