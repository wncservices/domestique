package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A ride the rider put on a day with a route ("new" in routeschedule.go)
// carries the goal id so its day reads as taken, but it is theirs, not the
// plan's. Editing the goal must still build the current week's plan sessions
// around it; the goal-edit path used to read the routed ride as "this week was
// already filled" and record the week without making anything.
func TestAGoalEditFillsTheCurrentWeekAroundARoutedRide(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	for _, w := range h.all(t) {
		_ = h.store.DeleteWorkout(ctx, w.ID)
	}
	if _, err := h.conn.Exec(`DELETE FROM scheduled_weeks`); err != nil {
		t.Fatal(err)
	}
	routed, err := h.store.CreateWorkout(ctx, api.LegacyFlagged(workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: h.goal.ID, Sport: model.SportCycling, Name: "Loop", Date: "2026-10-10",
		Description: "Sized to Loop.",
		Steps:       []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetOpen}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	slug, secs := "loop", 3600.0
	if _, err := h.store.UpdateWorkout(ctx, routed.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs}); err != nil {
		t.Fatal(err)
	}

	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+h.goal.ID, `{"notes":"edited"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status %d", resp.StatusCode)
	}

	// The save also starts the season fill in the background; let it finish so
	// it is not still writing when the test database goes away.
	h.srv.WaitForBackground()

	planned := 0
	for _, w := range h.week(t, h.now.AddDate(0, 0, -2)) {
		if w.ID != routed.ID && scheduler.IsGenerated(w) {
			planned++
		}
	}
	if planned == 0 {
		t.Error("the current week holds only the routed ride: the edit recorded it as filled without making the plan sessions")
	}
}
