package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/crewplan"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A session the rider ticked to keep, around a crew ride or through a return
// to training, is their decision. "Re-plan this week" used to delete the crew
// kind (and rebuild it) while sparing the life-event kind.
func TestReplanLeavesAKeptSessionAloneWhichEverFeatureKeptIt(t *testing.T) {
	for name, marker := range map[string]string{"crew": crewplan.KeptMarker, "life event": lifeevents.KeptMarker} {
		t.Run(name, func(t *testing.T) {
			h := newOnceHarness(t)
			ctx := context.Background()
			kept := h.hardSession(t, "2026-10-10", 60)
			d := scheduler.GeneratedDescription + " " + marker
			if _, err := h.store.UpdateWorkout(ctx, kept.ID, workout.UpdateWorkoutRequest{Description: &d}); err != nil {
				t.Fatal(err)
			}
			if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", ""); resp.StatusCode != http.StatusOK {
				t.Fatalf("replan status %d", resp.StatusCode)
			}
			got, err := h.store.GetWorkout(ctx, kept.ID)
			if err != nil || !strings.Contains(got.Description, marker) {
				t.Errorf("replan rebuilt a kept session: %q, %v", got.Description, err)
			}
		})
	}
}
