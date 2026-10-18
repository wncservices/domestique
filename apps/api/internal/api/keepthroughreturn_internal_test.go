package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A session the rider kept for their crew ride stays crew-kept when a life
// event's ease is unticked too: LegacyFlags gives the crew marker precedence,
// so turning it into life-kept would break parity (and lose which was meant).
func TestKeepThroughReturnKeepsAnExistingCrewKept(t *testing.T) {
	s := newThresholdTestServer(t)
	ctx := t.Context()
	desc := scheduler.GeneratedDescription + " " + scheduler.CrewKeptMarker
	wk, err := s.Training.CreateWorkout(ctx, LegacyFlagged(workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: "g1", Sport: model.SportCycling, Name: "x", Date: "2026-10-09", Description: desc,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.keepThroughReturn(ctx, wk); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Training.GetWorkout(ctx, wk.ID)
	if got.Flags.Kept != workout.KeptCrew {
		t.Errorf("kept = %q, want crew", got.Flags.Kept)
	}
}
