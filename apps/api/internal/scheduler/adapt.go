package scheduler

import (
	"regexp"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/workout"
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

// IsGenerated reports whether w was made by the scheduler and has not
// already been adjusted — the only workouts adaptation may change.
func IsGenerated(w workout.Workout) bool {
	return w.GoalID != "" &&
		strings.HasPrefix(w.Description, GeneratedDescription) &&
		!strings.Contains(w.Description, AdjustedMarker)
}

// keyNames are the sessions a week is built around — the long day and the
// harder mid-week one — as opposed to easy days, which nothing is lost by
// skipping. Kept beside sessionLabel, which names them, so a rename there
// cannot silently stop a missed key session from being noticed; the test in
// adapt_test.go fails if the two drift.
var keyNames = map[string]bool{
	"Long ride": true, "Long run": true,
	"Tempo ride": true, "Tempo run": true,
	"VO2max intervals": true, "Interval session": true,
}

var hardNames = map[string]bool{
	"Tempo ride": true, "Tempo run": true,
	"VO2max intervals": true, "Interval session": true,
}

// IsKeySession reports whether w is a long, tempo or interval session.
func IsKeySession(w workout.Workout) bool { return keyNames[w.Name] }

// IsHardSession reports whether w is a tempo or interval session — the ones
// worth swapping for something easy when a rider is carrying a lot of
// fatigue. A long ride is key but not hard: it is the volume, and easy
// pacing is already how it is meant to be ridden.
func IsHardSession(w workout.Workout) bool { return hardNames[w.Name] }

// EasyVariant builds the easy session that replaces w: the same sport and
// the same targets the plan would give any easy day, a fifth shorter than
// what it replaces — a rider swapped off a hard day because they are tired
// should not also be handed a long one.
func EasyVariant(w workout.Workout, profile workout.RiderProfile) workout.CreateWorkoutRequest {
	hours := workout.PlannedSeconds(w.Steps) * 0.8 / 3600
	if hours <= 0 {
		hours = 1
	}
	return buildSession(sessionEasy, hours, w.Sport, profile)
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
