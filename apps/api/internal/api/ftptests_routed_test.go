package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Scheduling an FTP test on a day replaces that day's plan-made session. A
// session with a route is the rider's own choice for the day, so the test is
// refused rather than deleting the ride and stranding its loop.
func TestAnFTPTestIsNotScheduledOverARoutedRide(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	goal := h.scheduleSetup(250)

	routed, err := h.training.CreateWorkout(ctx, generated("wilant", goal.ID, "Endurance ride", replanFriday, 3600))
	if err != nil {
		t.Fatal(err)
	}
	slug, secs := "friday-loop", 3600.0
	if _, err := h.training.UpdateWorkout(ctx, routed.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs}); err != nil {
		t.Fatal(err)
	}

	resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"ramp","date":"`+replanFriday+`"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a day with a routed ride", resp.StatusCode)
	}
	got, err := h.training.GetWorkout(ctx, routed.ID)
	if err != nil || got.RouteSlug != slug {
		t.Errorf("the routed ride was removed or unlinked: %+v (err %v)", got, err)
	}
	all, _ := h.training.ListWorkouts(ctx, "wilant")
	for _, wk := range all {
		if wk.TestProtocol != "" {
			t.Errorf("a test workout %q was left behind", wk.ID)
		}
	}

	// A day without a route is unaffected.
	if resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"ramp","date":"`+replanSaturday+`"}`); resp.StatusCode != http.StatusCreated {
		t.Errorf("a plain day: status %d, want 201", resp.StatusCode)
	}
}
