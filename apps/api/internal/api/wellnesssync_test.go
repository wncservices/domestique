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

// TestWellnessSyncFailingGarminNeverRepeatsTheBackfill is the fix for the
// bug the review reproduced: a rider whose Garmin session always fails
// (expired token, unrecognised display name — whatever it is, every date
// fails the same way) must not have the full 28-day backfill re-fire on
// every single sync tick forever, since daily_wellness for that rider stays
// empty no matter how many times it is tried. Both today's and yesterday's
// fetches failing skips the backfill outright (rule a) — this asserts the
// second sync costs exactly the same 2 calls as the first, not 2+29.
func TestWellnessSyncFailingGarminNeverRepeatsTheBackfill(t *testing.T) {
	fake := &fakeGarmin{wellnessErr: context.DeadlineExceeded}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) }

	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if len(fake.wellnessCalls) != 2 {
		t.Fatalf("wellness calls after first sync = %d, want 2 (today, yesterday; both fail, backfill skipped)", len(fake.wellnessCalls))
	}

	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if len(fake.wellnessCalls) != 4 {
		t.Fatalf("wellness calls after second sync = %d, want 4 (2 more, still no backfill — not 2+29)", len(fake.wellnessCalls))
	}
}

// TestWellnessSyncBackfillCooldownExpires is rule (b)'s own cap: a backfill
// is considered at most once per rider per 24h regardless of outcome. While
// the rider's Garmin keeps failing within that window, nothing changes; once
// both the cooldown has passed *and* Garmin starts answering again, the
// backfill is attempted again.
func TestWellnessSyncBackfillCooldownExpires(t *testing.T) {
	fake := &fakeGarmin{wellnessErr: context.DeadlineExceeded}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	start := time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC)
	h.srv.Clock = func() time.Time { return start }

	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if len(fake.wellnessCalls) != 2 {
		t.Fatalf("wellness calls after first sync = %d, want 2", len(fake.wellnessCalls))
	}

	// Still within the 24h cooldown, and Garmin is still failing — rows.
	// stay empty, so without the cooldown this would try the backfill
	// again; the cooldown holds it back regardless.
	h.srv.Clock = func() time.Time { return start.Add(1 * time.Hour) }
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if len(fake.wellnessCalls) != 4 {
		t.Fatalf("wellness calls after second sync (within cooldown) = %d, want 4 (2 more, no backfill yet)", len(fake.wellnessCalls))
	}
	rows, err := h.srv.Training.ListWellness(context.Background(), "wilant", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("wellness rows within the cooldown = %d, want 0 — Garmin is still failing every call", len(rows))
	}

	// Past the cooldown, with Garmin now answering: the backfill is
	// attempted again.
	h.srv.Clock = func() time.Time { return start.Add(25 * time.Hour) }
	fake.wellnessErr = nil
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if len(fake.wellnessCalls) != 4+2+27 {
		t.Fatalf("wellness calls after the cooldown passed = %d, want %d (2 more + a full 27-day backfill)", len(fake.wellnessCalls), 4+2+27)
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
