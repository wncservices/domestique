package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A replan reports how many of the sessions it just made were eased. A session
// that was only made longer to pay back owed load was not eased.
func TestCountAdjustedAmongIgnoresRepayment(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()
	step := []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetOpen}}
	create := func(date string, rules ...why.Rule) string {
		// The stored flags and the description markers must agree.
		description := scheduler.GeneratedDescription
		for range rules {
			description += " " + scheduler.AdjustedMarker + " note."
		}
		w, err := s.Training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
			Rider: "wilant", GoalID: "g", Sport: model.SportCycling, Name: "Endurance ride", Date: date,
			Description: description, Steps: step,
			Flags: workout.Flags{Origin: workout.OriginPlan, AdaptedRules: rules},
		})
		if err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	repaid := create("2026-10-08", why.LoadRepaid)
	eased := create("2026-10-09", why.ReadinessCaution)
	both := create("2026-10-10", why.LoadRepaid, why.ReadinessCaution)
	plain := create("2026-10-11")

	got, err := s.countAdjustedAmong(ctx, "wilant", []string{repaid, eased, both, plain})
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("adjusted = %d, want 2 (eased, and repaid-then-eased; not repaid alone)", got)
	}
}
