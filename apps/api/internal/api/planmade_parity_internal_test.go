package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/testschedule"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

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
	}
	for name, c := range cases {
		if got := isPlanMade(c.w); got != c.want {
			t.Errorf("api.isPlanMade(%s) = %v, want %v", name, got, c.want)
		}
		if got := testschedule.IsPlanMade(c.w); got != c.want {
			t.Errorf("testschedule.IsPlanMade(%s) = %v, want %v", name, got, c.want)
		}
	}
}
