package workout

import (
	"fmt"
	"math"
	"strings"
)

// distanceSecondsPerMeter turns a distance step into a comparable length only
// to rank steps against each other (about 29 km/h); it is never shown.
const distanceSecondsPerMeter = 1.0 / 8.0

// openEffort is what a session with nothing to state about its main set reads.
const openEffort = "open effort"

// Summary is the main set of a workout in one line, for a calendar entry and
// the morning email: "3 x 10 min at 250-260 W", "60 min at 180-200 W",
// "45 min at 140-150 bpm", "open effort".
//
// It states what is prescribed, which is a target and not a health value. The
// main set is the repeat block or single step carrying the most work; warm-up,
// cool-down, rest and recovery steps are never the answer, so a session of
// nothing but those reads "open effort".
func Summary(steps []WorkoutStep) string {
	var (
		best      string
		bestScore = -1.0
	)
	consider := func(score float64, text string) {
		if score > bestScore {
			best, bestScore = text, score
		}
	}
	for _, s := range steps {
		if s.Repeat >= 2 {
			var total float64
			var main *WorkoutStep
			var mainScore float64 = -1
			for i := range s.Steps {
				c := &s.Steps[i]
				if !isWork(*c) || c.Repeat >= 2 {
					continue
				}
				total += stepScore(*c)
				if stepScore(*c) > mainScore {
					main, mainScore = c, stepScore(*c)
				}
			}
			if main != nil {
				consider(total*float64(s.Repeat), fmt.Sprintf("%d x %s", s.Repeat, stepText(*main)))
			}
			continue
		}
		if isWork(s) {
			consider(stepScore(s), stepText(s))
		}
	}
	if bestScore < 0 {
		return openEffort
	}
	return best
}

// isWork is true for a step that is the effort itself, not the lead-in or the
// recovery around it.
func isWork(s WorkoutStep) bool {
	switch s.Intensity {
	case IntensityWarmup, IntensityCooldown, IntensityRest, IntensityRecovery:
		return false
	}
	return true
}

func stepScore(s WorkoutStep) float64 {
	switch s.Duration {
	case DurationTime:
		return s.Seconds
	case DurationDistance:
		return s.Meters * distanceSecondsPerMeter
	}
	return 0
}

// stepText is one step: "10 min at 250-260 W", "10 min, open effort",
// "open at 200 W", "open effort".
func stepText(s WorkoutStep) string {
	target := targetText(s)
	var length string
	switch s.Duration {
	case DurationTime:
		length = secondsText(s.Seconds)
	case DurationDistance:
		length = metersText(s.Meters)
	}
	switch {
	case length == "" && target == "":
		return openEffort
	case length == "":
		return "open at " + target
	case target == "":
		return length + ", " + openEffort
	}
	return length + " at " + target
}

func targetText(s WorkoutStep) string {
	rng := func(lo, hi float64, unit string) string {
		if math.Round(lo) == math.Round(hi) {
			return fmt.Sprintf("%.0f %s", lo, unit)
		}
		return fmt.Sprintf("%.0f-%.0f %s", lo, hi, unit)
	}
	switch s.Target {
	case TargetPower:
		return rng(s.TargetLow, s.TargetHigh, "W")
	case TargetHeartRate:
		return rng(s.TargetLow, s.TargetHigh, "bpm")
	case TargetCadence:
		return rng(s.TargetLow, s.TargetHigh, "rpm")
	case TargetPace:
		lo, hi := s.TargetLow, s.TargetHigh
		if lo <= 0 || hi <= 0 {
			return ""
		}
		// m/s to min/km, the fast end (highest speed) first.
		fast, slow := paceText(math.Max(lo, hi)), paceText(math.Min(lo, hi))
		if fast == slow {
			return fast + " /km"
		}
		return fast + "-" + slow + " /km"
	}
	return ""
}

func paceText(metersPerSecond float64) string {
	sec := int(math.Round(1000 / metersPerSecond))
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func secondsText(seconds float64) string {
	sec := int(math.Round(seconds))
	switch {
	case sec <= 0:
		return ""
	case sec < 60:
		return fmt.Sprintf("%d s", sec)
	case sec%60 == 0:
		return fmt.Sprintf("%d min", sec/60)
	}
	return fmt.Sprintf("%d min %d s", sec/60, sec%60)
}

func metersText(meters float64) string {
	switch {
	case meters <= 0:
		return ""
	case meters < 1000:
		return fmt.Sprintf("%.0f m", meters)
	}
	km := strings.TrimSuffix(fmt.Sprintf("%.1f", meters/1000), ".0")
	return km + " km"
}

// DurationLabel is a whole session's length as a rider says it: "45 min" under
// an hour, "1h05" from an hour up. Zero or less is "", meaning unknown.
func DurationLabel(seconds float64) string {
	min := int(math.Round(seconds / 60))
	switch {
	case min <= 0:
		return ""
	case min < 60:
		return fmt.Sprintf("%d min", min)
	}
	return fmt.Sprintf("%dh%02d", min/60, min%60)
}

// ZoneLabel is a zone as a rider reads it; "" for an unset or unknown one.
func ZoneLabel(z Zone) string {
	switch z {
	case ZoneTempo:
		return "Tempo"
	case ZoneSweetSpot:
		return "Sweet spot"
	case ZoneThreshold:
		return "Threshold"
	case ZoneVO2Max:
		return "VO2 max"
	case ZoneAnaerobic:
		return "Anaerobic"
	case ZoneIntervals:
		return "Intervals"
	case ZoneEndurance:
		return "Endurance"
	}
	return ""
}
