package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// TestWellnessSyncFetchesTodayAndYesterday drives syncRiderMetrics end to
// end for a rider who already has at least one daily_wellness row (so the
// one-time backfill — see TestWellnessSyncBackfillsOnceForANewRider — does
// not also fire): each sync fetches exactly today and yesterday, and saves
// both.
func TestWellnessSyncFetchesTodayAndYesterday(t *testing.T) {
	fake := &fakeGarmin{wellnessByDate: map[string]garmin.Wellness{
		"2026-03-19": {Date: "2026-03-19", HRVStatus: "BALANCED", SleepScore: 80, RestingHR: 50},
		"2026-03-18": {Date: "2026-03-18", HRVStatus: "BALANCED", SleepScore: 75, RestingHR: 51},
	}}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) }

	ctx := context.Background()
	if err := h.srv.Training.SaveWellness(ctx, workout.DailyWellness{Rider: "wilant", Date: "2026-02-01", RestingHR: 49}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(fake.wellnessCalls) != 2 {
		t.Fatalf("wellness calls = %v, want exactly today and yesterday — rows already exist, no backfill", fake.wellnessCalls)
	}

	rows, err := h.srv.Training.ListWellness(ctx, "wilant", "2026-03-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("wellness rows since March = %d, want 2 (today and yesterday)", len(rows))
	}
	if rows[0].Date != "2026-03-18" || rows[0].RestingHR != 51 {
		t.Errorf("row[0] = %+v, want yesterday with restingHr 51", rows[0])
	}
	if rows[1].Date != "2026-03-19" || rows[1].SleepScore != 80 {
		t.Errorf("row[1] = %+v, want today with sleepScore 80", rows[1])
	}
}

// TestWellnessSyncBackfillsOnceForANewRider is the spec's own cap: the first
// sync for a rider with no daily_wellness rows at all backfills the last 28
// days on top of today/yesterday (29 Garmin calls total); a second sync,
// with rows now on file, fetches only today and yesterday again.
func TestWellnessSyncBackfillsOnceForANewRider(t *testing.T) {
	fake := &fakeGarmin{}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) }

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(fake.wellnessCalls) != 29 {
		t.Fatalf("wellness calls on first sync = %d, want 29 (today, yesterday, 27 more days of a 28-day backfill)", len(fake.wellnessCalls))
	}

	// Second sync: rows now exist, so only today and yesterday are asked for.
	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(fake.wellnessCalls) != 31 {
		t.Fatalf("wellness calls after second sync = %d, want 31 (29 + 2, no second backfill)", len(fake.wellnessCalls))
	}
}

// TestWellnessSyncGarminFailureIsNotAWarning mirrors
// TestSyncGarminRestingHeartRateFailureIsNotAWarning: the wellness endpoints
// are the same kind of undocumented, best-effort lookup, so a failure must
// not surface as a "Sync warning" the way a real provider (activities,
// Wahoo workouts) failure does — the sync still reports success.
func TestWellnessSyncGarminFailureIsNotAWarning(t *testing.T) {
	fake := &fakeGarmin{wellnessErr: context.DeadlineExceeded}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) }

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	var out struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when wellness lookups fail", resp.StatusCode)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("warnings = %v, want none — a failed best-effort wellness lookup is not a sync failure", out.Warnings)
	}

	rows, err := h.srv.Training.ListWellness(context.Background(), "wilant", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("wellness rows = %d, want 0 — nothing to save when every lookup failed", len(rows))
	}
}

// TestWellnessSyncNeverCalledForAWahooOnlyRider is the asymmetry this
// feature must not introduce: a rider with no Garmin connection makes zero
// Garmin calls of any kind, wellness included.
func TestWellnessSyncNeverCalledForAWahooOnlyRider(t *testing.T) {
	fake := &fakeGarmin{}
	h := newMetricsSyncHarness(t, fake)
	h.seedWahooSession("wilant")
	// Deliberately no h.seedGarminSession: this rider only uses Wahoo.

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(fake.wellnessCalls) != 0 {
		t.Errorf("wellness calls = %v, want none — no Garmin connection at all", fake.wellnessCalls)
	}
}
