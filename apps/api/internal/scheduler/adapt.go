package scheduler

import (
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// GeneratedDescription is what every workout this package builds carries as
// its description — which is also how the rest of the app tells a workout the
// scheduler made from one a rider built by hand. Automatic adjustments
// (internal/adapter) only ever touch the former: a rider's own session is
// theirs, however much the plan around it moves.
const GeneratedDescription = "Generated from the periodization plan."

// AdjustedMarker prefixes the note appended to a generated workout's
// description when it has been changed automatically. Its presence is also
// what stops the same workout being adjusted twice.
const AdjustedMarker = "Adjusted automatically:"

// SwappedMarker prefixes the note appended when the rider swapped a plan-made
// session for an alternate ("I have N minutes" fits count too). Like
// AdjustedMarker it makes the session rider-touched: adaptation, the
// whole-season refresh and replan all leave it alone, because the rider
// chose it on purpose. Unlike AdjustedMarker it does not stop a further
// swap: each one is the rider acting.
const SwappedMarker = "Swapped by you:"

// IsGenerated reports whether w was made by the scheduler and has not
// already been adjusted or swapped, the only workouts adaptation may change.
func IsGenerated(w workout.Workout) bool {
	return w.GoalID != "" &&
		strings.HasPrefix(w.Description, GeneratedDescription) &&
		!strings.Contains(w.Description, AdjustedMarker) &&
		!strings.Contains(w.Description, SwappedMarker)
}

// keyNames are the legacy session names IsKeySession/IsHardSession fall back
// to for a workout with no zone — a row made before the zone column
// existed, when this package still built exactly these four fixed session
// types (see enduranceName for the long/easy names it still produces
// today). A workout with a zone is classified from that instead; see
// IsKeySession's own doc comment.
var keyNames = map[string]bool{
	"Long ride": true, "Long run": true,
	"Tempo ride": true, "Tempo run": true,
	"VO2max intervals": true, "Interval session": true,
}

var hardNames = map[string]bool{
	"Tempo ride": true, "Tempo run": true,
	"VO2max intervals": true, "Interval session": true,
}

// IsKeySession reports whether w is a long, tempo or interval session. A
// structured zone (threshold, vo2max, ...) decides this outright; a workout
// with no zone — ZoneEndurance or "" — falls back to keyNames, which covers
// both a legacy row made before the zone column existed and a long/easy day
// that is key by being the week's volume, not its intensity.
func IsKeySession(w workout.Workout) bool {
	if workout.IsStructuredZone(w.Zone) {
		return true
	}
	return keyNames[w.Name]
}

// IsHardSession reports whether w is a tempo or interval session — the ones
// worth swapping for something easy when a rider is carrying a lot of
// fatigue. A long ride is key but not hard: it is the volume, and easy
// pacing is already how it is meant to be ridden. Same zone-first,
// name-fallback shape as IsKeySession.
func IsHardSession(w workout.Workout) bool {
	if workout.IsStructuredZone(w.Zone) {
		return true
	}
	return hardNames[w.Name]
}

// EasyVariant builds the easy session that replaces w: the same sport and
// the same targets the plan would give any easy day (zone endurance, no
// level), a fifth shorter than what it replaces — a rider swapped off a hard
// day because they are tired should not also be handed a long one.
func EasyVariant(w workout.Workout, profile workout.RiderProfile) workout.CreateWorkoutRequest {
	// An indoor session's own steps are already the shortened trainer version.
	// Sizing the easy ride a fifth under that would shrink it twice (the
	// conversion is applied again to the replacement), so an indoor session is
	// sized from the outdoor steps it was made from.
	steps := w.Steps
	if w.OutdoorSteps != nil {
		steps = *w.OutdoorSteps
	}
	hours := workout.PlannedSeconds(steps) * 0.8 / 3600
	if hours <= 0 {
		hours = 1
	}
	return BuildEnduranceSession(hours, false, w.Sport, profile)
}

// OneRungEasier builds the session one rung below w's own on its zone's ladder,
// the same zone and sport, for the return ramp after a life event. It reports
// false when there is nothing to step down to: no ladder for the zone (an
// endurance session, or a legacy zone-less one) or w already at the bottom
// rung. The caller decides what to do then; the ramp falls back to EasyVariant
// for a hard session.
func OneRungEasier(w workout.Workout, profile workout.RiderProfile) (workout.CreateWorkoutRequest, bool) {
	ladder, ok := workoutlib.LadderFor(w.Sport, string(w.Zone))
	if !ok {
		return workout.CreateWorkoutRequest{}, false
	}
	target := int(math.Round(w.Level)) - 1
	if target < 1 {
		return workout.CreateWorkoutRequest{}, false
	}
	for _, r := range ladder.Rungs {
		if r.Level == target {
			return workoutlib.Instantiate(ladder, r, profile), true
		}
	}
	return workout.CreateWorkoutRequest{}, false
}

// EasedBeforeTestReason is the note on a hard session eased because an FTP test
// follows it the next day.
const EasedBeforeTestReason = "eased the day before your FTP test"

// NeedsEasingBeforeTest reports whether w should be swapped for an easy
// session because one of workouts is an FTP test dated the following day. A
// test read on tired legs under-reads FTP, so the day before it is easy.
// Only a generated, unadjusted hard session qualifies: a session the rider
// built is theirs, one already adjusted has been dealt with once, and the
// test itself is never eased.
func NeedsEasingBeforeTest(w workout.Workout, workouts []workout.Workout) bool {
	if w.TestProtocol != "" || w.Date == "" || !IsGenerated(w) || !IsHardSession(w) {
		return false
	}
	day, err := time.Parse("2006-01-02", w.Date)
	if err != nil {
		return false
	}
	next := day.AddDate(0, 0, 1).Format("2006-01-02")
	for _, t := range workouts {
		if t.TestProtocol != "" && t.Date == next {
			return true
		}
	}
	return false
}

var movedFromRE = regexp.MustCompile(`moved from (\d{4}-\d{2}-\d{2})`)

// MovedFrom returns the date a generated workout was originally planned for,
// when an automatic adjustment has since moved it. Scheduling reads this so
// it does not see the vacated day as empty and make the missed session again.
func MovedFrom(description string) (string, bool) {
	m := movedFromRE.FindStringSubmatch(description)
	if m == nil {
		return "", false
	}
	return m[1], true
}
