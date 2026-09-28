package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type readinessResponseBody struct {
	Today struct {
		Verdict  string   `json:"verdict"`
		Reasons  []string `json:"reasons"`
		Wellness *struct {
			Date       string `json:"date"`
			SleepScore int    `json:"sleepScore"`
		} `json:"wellness"`
	} `json:"today"`
	Days []struct {
		Date string `json:"date"`
	} `json:"days"`
}

// TestReadinessEndpointReturnsRestVerdictFromTodaysWellness drives
// GET /api/training/readiness end to end: a rider with a poor Garmin
// readiness score today gets a rest verdict, the reasons, and today's own
// wellness echoed back.
func TestReadinessEndpointReturnsRestVerdictFromTodaysWellness(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) }
	ctx := context.Background()

	if err := h.srv.Training.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: "2026-03-19", ReadinessLevel: "POOR", ReadinessScore: 10, SleepScore: 70,
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/readiness", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out readinessResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Today.Verdict != "rest" {
		t.Fatalf("verdict = %q, want rest", out.Today.Verdict)
	}
	if len(out.Today.Reasons) == 0 {
		t.Error("reasons = [], want at least one naming Garmin readiness")
	}
	if out.Today.Wellness == nil || out.Today.Wellness.Date != "2026-03-19" || out.Today.Wellness.SleepScore != 70 {
		t.Errorf("today wellness = %+v, want today's own row echoed back", out.Today.Wellness)
	}
	if len(out.Days) != 1 || out.Days[0].Date != "2026-03-19" {
		t.Errorf("days = %+v, want the one row on file", out.Days)
	}
}

// TestReadinessEndpointOmitsWellnessWithNoGarminRowToday covers the
// Wahoo-only/watch-not-worn case: no daily_wellness row for today means the
// verdict falls back to load/form alone and Wellness is omitted, not a
// zero-valued row.
func TestReadinessEndpointOmitsWellnessWithNoGarminRowToday(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) }

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/readiness", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out readinessResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Today.Wellness != nil {
		t.Errorf("today wellness = %+v, want omitted with no Garmin row today", out.Today.Wellness)
	}
	if out.Today.Verdict != "ready" {
		t.Errorf("verdict = %q, want ready with nothing at all on file", out.Today.Verdict)
	}
}

// TestReadinessEndpointIsOwnerOnly is the same rule every other training
// endpoint enforces: the rider comes from the session, so one rider's
// wellness never leaks into another's response.
func TestReadinessEndpointIsOwnerOnly(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) }
	ctx := context.Background()

	if err := h.srv.Training.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: "2026-03-19", ReadinessLevel: "POOR", ReadinessScore: 10,
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("someone-else", "cyclists", http.MethodGet, "/api/training/readiness", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out readinessResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Days) != 0 || out.Today.Wellness != nil {
		t.Errorf("out = %+v, want a rider with no wellness of their own to see none of wilant's", out)
	}
}
