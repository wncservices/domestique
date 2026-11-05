package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
)

// A rider whose step panics must not stop the riders after them in the same
// pass: each per-rider loop wraps its step in the panic guard. This drives the
// metrics loop with two connected riders and a panic for the first. If the
// loop's guard is removed, the panic ends the tick and the second rider is
// never synced, so this test goes red.
func TestAPanickingRiderDoesNotStarveTheRidersAfterIt(t *testing.T) {
	fake := &fakeGarmin{activities: []garmin.Activity{
		{ID: "6001", Sport: "cycling", StartTime: time.Date(2026, 3, 4, 7, 0, 0, 0, time.UTC),
			DurationSeconds: 5400, DistanceM: 45000, AvgHR: 150, AvgPowerWatts: 230},
	}}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.seedWahooSession("other")
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "admin"); err != nil {
		t.Fatal(err)
	}

	var stepped []string
	h.srv.BeforeRiderStep = func(stage, rider string) {
		stepped = append(stepped, rider)
		if rider == "wilant" {
			panic("poison rider data")
		}
	}

	// The tick-level guard (runGuarded in RunAutoScheduleLoop) is what would
	// catch a panic that escapes a rider's step, so stand in for it here. What
	// is under test is whether the other riders still get their step.
	func() {
		defer func() { _ = recover() }()
		h.srv.AutoScheduleTick(context.Background())
	}()

	if len(stepped) != 2 {
		t.Fatalf("steps = %v, want both riders stepped", stepped)
	}
	resp := h.as("other", "cyclists", http.MethodGet, "/api/training/fitness", "")
	var fit struct {
		Sessions []struct {
			Provider string `json:"provider"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fit); err != nil {
		t.Fatal(err)
	}
	if len(fit.Sessions) != 1 {
		t.Fatalf("other: sessions = %d, want 1 although the rider before them panicked", len(fit.Sessions))
	}
}
