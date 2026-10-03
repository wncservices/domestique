package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/alternates"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/testschedule"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func generatedSession() workout.Workout {
	return workout.Workout{
		ID: "w", GoalID: "g", Sport: model.SportCycling, Name: "Endurance ride", Zone: workout.ZoneEndurance,
		Description: scheduler.GeneratedDescription, CreatedAt: "t", UpdatedAt: "t",
	}
}

func TestARoutedSessionIsNotUntouched(t *testing.T) {
	plain := generatedSession()
	if !untouchedPlanSession(plain) {
		t.Fatal("fixture: a plain generated session must be untouched")
	}
	routed := plain
	routed.RouteSlug = "a-loop"
	if untouchedPlanSession(routed) {
		t.Error("a routed session counts as untouched: refresh and trim would rebuild it and drop the link")
	}
}

// A routed session is the rider's own choice for its day, so it is not
// plan-made in either copy of the definition: the FTP-test suggester will not
// offer to replace it and the apply will not delete it. The two definitions
// stay equal (planmade_parity_internal_test.go holds the general case).
func TestARoutedSessionIsNotPlanMadeInEitherDefinition(t *testing.T) {
	routed := generatedSession()
	routed.RouteSlug = "a-loop"
	if isPlanMade(routed) || testschedule.IsPlanMade(routed) {
		t.Errorf("isPlanMade = %v, testschedule.IsPlanMade = %v, want both false", isPlanMade(routed), testschedule.IsPlanMade(routed))
	}
}

// Readiness easing acts on scheduler.IsGenerated sessions: protecting the
// rider outranks the route, so a link must not take a session out of it.
func TestARoutedSessionCanStillBeEasedByReadiness(t *testing.T) {
	routed := generatedSession()
	routed.RouteSlug = "a-loop"
	if !scheduler.IsGenerated(routed) {
		t.Error("a routed session is no longer eligible for readiness easing")
	}
}

func TestAlternatesAreNotOfferedOnARoutedSession(t *testing.T) {
	plain := generatedSession()
	if !alternates.Plannable(plain) {
		t.Fatal("fixture: a plain generated endurance session must have alternates")
	}
	routed := plain
	routed.RouteSlug = "a-loop"
	if alternates.Plannable(routed) || alternates.Available(routed, "2000-01-01", false) {
		t.Error("alternates are offered on a routed session")
	}
}

func TestReplanLeavesARoutedSessionAndRemovesTheRest(t *testing.T) {
	env := openRiderDataEnv(t, filepath.Join(t.TempDir(), "r.db"))
	ctx := context.Background()
	mk := func(name, date string) workout.Workout {
		wk, err := env.srv.Training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
			Rider: "wilant", Sport: model.SportCycling, Name: name, GoalID: "g", Date: date,
			Description: scheduler.GeneratedDescription, Zone: workout.ZoneEndurance,
			Steps: []workout.WorkoutStep{{Name: "Ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3600}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return wk
	}
	routed := mk("Routed ride", "2026-10-05")
	plain := mk("Plain ride", "2026-10-06")
	slug, secs := "a-loop", 3600.0
	if _, err := env.srv.Training.UpdateWorkout(ctx, routed.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs}); err != nil {
		t.Fatal(err)
	}

	removed, err := env.srv.removePlanMadeWorkouts(ctx, "wilant", "2026-10-03", "2026-10-11")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed %d, want only the unrouted one", removed)
	}
	if _, err := env.srv.Training.GetWorkout(ctx, routed.ID); err != nil {
		t.Errorf("the routed session was removed by a replan: %v", err)
	}
	if _, err := env.srv.Training.GetWorkout(ctx, plain.ID); err == nil {
		t.Error("the plain session survived the replan")
	}
}
