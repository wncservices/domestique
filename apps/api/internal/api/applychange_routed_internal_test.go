package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The adapter never offers a routed easy day to a missed session, so
// applyChange's refusal is defence in depth: a stale or hand-built change must
// not delete a ride the rider chose a route for (it would strand the loop).
func TestApplyChangeRefusesToDeleteARoutedEasyDay(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()
	step := []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetOpen}}
	create := func(name, date string) workout.Workout {
		w, err := s.Training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
			Rider: "wilant", GoalID: "g", Sport: model.SportCycling, Name: name, Date: date,
			Description: scheduler.GeneratedDescription, Steps: step,
		})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	missed := create("Threshold 3x12", "2026-10-06")
	easy := create("Easy spin", "2026-10-08")
	slug, secs := "a-loop", 3600.0
	if _, err := s.Training.UpdateWorkout(ctx, easy.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs}); err != nil {
		t.Fatal(err)
	}
	easy, _ = s.Training.GetWorkout(ctx, easy.ID)

	_, err := s.applyChange(ctx, "wilant", missed, map[string]workout.Workout{missed.ID: missed, easy.ID: easy},
		workout.RiderProfile{}, adapter.Change{WorkoutID: missed.ID, NewDate: easy.Date, ReplaceWorkoutID: easy.ID})
	if err == nil {
		t.Fatal("applyChange deleted a routed easy day")
	}
	if got, err := s.Training.GetWorkout(ctx, easy.ID); err != nil || got.RouteSlug != slug {
		t.Errorf("the routed easy day was removed or unlinked: %+v, %v", got, err)
	}
	if got, _ := s.Training.GetWorkout(ctx, missed.ID); got.Date != missed.Date {
		t.Errorf("the missed session moved to %s although the change was refused", got.Date)
	}
}
