package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
)

// A session the rider ticked to keep as planned is theirs: scheduling a test on
// its day neither deletes it nor stacks a second session beside it.
func TestAnFTPTestIsNotScheduledOverAKeptSession(t *testing.T) {
	for name, marker := range map[string]string{"crew": scheduler.CrewKeptMarker, "life event": scheduler.LifeKeptMarker} {
		t.Run(name, func(t *testing.T) {
			h := newReplanHarness(t)
			ctx := context.Background()
			goal := h.scheduleSetup(250)
			req := generated("wilant", goal.ID, "Endurance ride", replanFriday, 3600)
			req.Description += " " + marker
			kept, err := h.training.CreateWorkout(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"ramp","date":"`+replanFriday+`"}`)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409 for a day with a kept session", resp.StatusCode)
			}
			if _, err := h.training.GetWorkout(ctx, kept.ID); err != nil {
				t.Errorf("the kept session was removed: %v", err)
			}
		})
	}
}
