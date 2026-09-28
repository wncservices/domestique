// Package progression is the pure rules engine behind TrainerRoad-style
// progression levels: where a rider's level in each training zone starts,
// how a completed workout moves it, and the human-readable reason recorded
// alongside each move. See docs/superpowers/specs/2026-09-27-progression-levels-design.md
// ("Levels move after every analysed ride") for the table this implements.
//
// This package is deliberately free of storage and API concerns — plain
// values in, plain values out. internal/workout owns the ProgressionLevel
// row type and the database; nothing here imports it, so this package can
// be tested without a database and reused wherever a level needs computing.
package progression

import (
	"fmt"
	"math"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

// Level is one sport/zone progression level, either a freshly initialised
// starting value or the result of applying a Delta. Reason is empty for an
// initial level — there is no "change" to explain yet.
type Level struct {
	Sport  string
	Zone   string
	Value  float64
	Reason string
}

// Outcome is a ride's rideanalysis verdict, as Delta and Reason see it. It
// mirrors rideanalysis.Outcome's own string values by hand rather than
// importing that package — the same reasoning workout.SessionAnalysis's own
// doc comment gives, and internal/adapter's own outcome type gives too: this
// package stays free of the rideanalysis dependency, so a caller that holds
// a plain string (as workout.SessionAnalysis.Outcome is stored) converts
// with Outcome(s).
type Outcome string

const (
	OutcomeNailed     Outcome = "nailed"
	OutcomeCompleted  Outcome = "completed"
	OutcomeStruggled  Outcome = "struggled"
	OutcomeIncomplete Outcome = "incomplete"
	OutcomeUnplanned  Outcome = "unplanned"
)

// cyclingZones and runningZones are the structured zones progression levels
// apply to, per sport — the spec's "Levels" list. Endurance/easy/long
// sessions stay volume-sized and carry no level (out of scope). These
// mirror workout.Zone's string values by hand rather than importing that
// package, keeping this package free of the storage layer it lives beside.
var (
	cyclingZones = []string{"tempo", "sweet_spot", "threshold", "vo2max", "anaerobic"}
	runningZones = []string{"tempo", "threshold", "intervals"}
)

// Initial returns the starting level for every structured zone of sport,
// seeded from the rider's profile experience level: beginner 2.0,
// intermediate 4.0, advanced 6.0, anything else (unset, unrecognised) 3.0.
// experience is matched case-insensitively and trimmed.
func Initial(experience string, sport model.Sport) []Level {
	value := 3.0
	switch strings.ToLower(strings.TrimSpace(experience)) {
	case "beginner":
		value = 2.0
	case "intermediate":
		value = 4.0
	case "advanced":
		value = 6.0
	}

	var zones []string
	switch sport {
	case model.SportCycling:
		zones = cyclingZones
	case model.SportRunning:
		zones = runningZones
	}

	levels := make([]Level, 0, len(zones))
	for _, z := range zones {
		levels = append(levels, Level{Sport: string(sport), Zone: z, Value: value})
	}
	return levels
}

// Delta computes how much a rider's level should move after one analysed
// workout, following the spec's update table exactly. cur is the rider's
// current level in the workout's zone, workoutLevel is the workout's own
// rated level, outcome is rideanalysis's verdict ("nailed", "completed",
// "struggled", "incomplete", "unplanned", or "" for no zone/unplanned —
// all of which mean no change), and feel is the rider's optional 1–5
// "how did it feel" rating (0 = not rated, no adjustment).
//
// The result is a delta, not a new level: Apply(cur, Delta(...)) is the new
// level, clamped and rounded.
func Delta(cur, workoutLevel float64, outcome Outcome, feel int) float64 {
	diff := workoutLevel - cur

	switch outcome {
	case OutcomeNailed:
		if diff >= -0.5 {
			bump := 0.3 + feelAdjustment(feel)
			if bump < 0.1 {
				bump = 0.1
			}
			newLevel := math.Max(cur, workoutLevel) + bump
			return newLevel - cur
		}
		return 0.1

	case OutcomeCompleted:
		if diff >= 0 {
			return 0.1
		}
		return 0

	case OutcomeStruggled:
		// The adapter steps the next workout down instead; the level
		// itself does not move on a struggled session.
		return 0

	case OutcomeIncomplete:
		if diff <= 0 {
			return -0.3
		}
		return 0

	default:
		// "unplanned", "", or anything else this package doesn't
		// recognise: no zone to move, so no change.
		return 0
	}
}

// feelAdjustment is the spec's feel table, applied only to a nailed bump.
// 0 means "not rated" and adjusts nothing.
func feelAdjustment(feel int) float64 {
	switch feel {
	case 1, 2:
		return 0.2
	case 3:
		return 0
	case 4:
		return -0.1
	case 5:
		return -0.2
	default:
		return 0
	}
}

// Apply moves cur by delta, clamped to [1.0, 10.0] and rounded to one
// decimal — the shape every stored level and every reason string uses.
func Apply(cur, delta float64) float64 {
	v := cur + delta
	if v < 1.0 {
		v = 1.0
	}
	if v > 10.0 {
		v = 10.0
	}
	return math.Round(v*10) / 10
}

// outcomeVerbs capitalises the outcomes that actually move a level, for
// Reason's leading word — "the same word rideanalysis already uses for the
// outcome, just capitalised for a sentence" rather than a separate
// vocabulary.
var outcomeVerbs = map[Outcome]string{
	OutcomeNailed:     "Nailed",
	OutcomeCompleted:  "Completed",
	OutcomeStruggled:  "Struggled",
	OutcomeIncomplete: "Incomplete",
}

// Reason builds the human-readable text recorded alongside a level change,
// e.g. "Nailed Threshold 3×12 (5.0) — threshold 4.6 → 5.3". zone is the
// workout's training-load bucket (lowercase, as stored); workoutLevel is
// the workout's own rated level; from/to are the rider's level before and
// after this change.
//
// This takes an explicit zone parameter, unlike the brief's illustrative
// signature — the spec's example text embeds the zone label ("threshold"
// after the em dash), which has no other source once Delta has already
// collapsed cur/workoutLevel into a single number.
func Reason(workoutName, zone string, workoutLevel, from, to float64, outcome Outcome) string {
	verb, ok := outcomeVerbs[outcome]
	if !ok {
		verb = string(outcome)
		if len(outcome) > 0 {
			verb = strings.ToUpper(verb[:1]) + verb[1:]
		}
	}
	return fmt.Sprintf("%s %s (%.1f) — %s %.1f → %.1f", verb, workoutName, workoutLevel, zone, from, to)
}
