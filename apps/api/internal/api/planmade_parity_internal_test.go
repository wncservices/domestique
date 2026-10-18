package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// There is one definition of "plan-made" now (internal/scheduler), where this
// used to pin two copies against each other. The suggester and the apply both
// call scheduler.AutoReplaceable, and this table keeps its answers honest.
//
// The FTP-test suggester decides which session a test would "replace"; the API
// decides which one the apply actually replaces. When the two definitions of
// "plan-made" drift, the suggester offers a swap the apply refuses to make and
// the rider ends up with two sessions. This pins them against each other.
func TestTheTwoDefinitionsOfPlanMadeAgree(t *testing.T) {
	gen := scheduler.GeneratedDescription
	cases := map[string]struct {
		w    workout.Workout
		want bool
	}{
		"generated":   {workout.Workout{GoalID: "g", Description: gen}, true},
		"adjusted":    {workout.Workout{GoalID: "g", Description: gen + " " + scheduler.AdjustedMarker + " eased."}, true},
		"moved":       {workout.Workout{GoalID: "g", Description: gen + " " + scheduler.AdjustedMarker + " moved from 2026-03-24."}, true},
		"swapped":     {workout.Workout{GoalID: "g", Description: gen + " " + scheduler.SwappedMarker + " harder, was Threshold 4 (1h08)."}, false},
		"reverted":    {workout.Workout{GoalID: "g", Description: gen + " " + scheduler.SwappedMarker + " Back to the planned version."}, false},
		"rider-built": {workout.Workout{Description: "mine"}, false},
		"no goal":     {workout.Workout{Description: gen}, false},
		"other text":  {workout.Workout{GoalID: "g", Description: "my own plan"}, false},
		// A test carries the goal so its day reads as taken, but not the
		// generated description, so it is never replaced.
		"test": {workout.Workout{GoalID: "g", Description: "A test.", TestProtocol: "ramp"}, false},
		// A crew ride carries the goal too, and is never plan-made, whatever its text.
		"crew ride": {workout.Workout{GoalID: "g", Description: gen, CrewRideID: "ride-1"}, false},
		// A route link is the rider's own choice for the day: neither copy may
		// offer to replace it, or the apply deletes the ride and strands its loop.
		"routed": {workout.Workout{GoalID: "g", Description: gen, RouteSlug: "a-loop"}, false},
	}
	for name, c := range cases {
		c.w = withLegacyFlags(c.w)
		if got := scheduler.IsPlanMade(c.w); got != c.want {
			t.Errorf("scheduler.IsPlanMade(%s) = %v, want %v", name, got, c.want)
		}
		// Replacing is IsPlanMade minus what the rider kept (below).
		if got := scheduler.AutoReplaceable(c.w); got != c.want {
			t.Errorf("scheduler.AutoReplaceable(%s) = %v, want %v", name, got, c.want)
		}
	}
}

// The suggester (testschedule) must offer to replace exactly what the apply
// (replacePlanMadeOn) removes: both go through scheduler.AutoReplaceable, so a
// kept session is neither offered nor deleted.
func TestAKeptSessionIsNeitherOfferedNorReplacedByAnFTPTest(t *testing.T) {
	for _, marker := range []string{scheduler.CrewKeptMarker, scheduler.LifeKeptMarker} {
		w := withLegacyFlags(workout.Workout{GoalID: "g", Description: scheduler.GeneratedDescription + " " + marker})
		if scheduler.AutoReplaceable(w) {
			t.Errorf("a session carrying %q is replaceable", marker)
		}
		if !scheduler.IsPlanMade(w) {
			t.Errorf("a kept session is still plan-made structurally (%q)", marker)
		}
	}
}

// legacyFlagged gives a fixture the Flags its description text implies, so a
// table written in markers drives the predicates, which read the columns.
func withLegacyFlags(w workout.Workout) workout.Workout {
	w.Flags = workout.LegacyFlags(w)
	return w
}
