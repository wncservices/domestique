package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/rideimport/rideimporttest"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// histClock is the fixed "now" of these tests, with an explicit zone.
var histClock = time.Date(2026, time.September, 10, 9, 0, 0, 0, time.UTC)

func (h *metricsSyncHarness) setClock(now time.Time) {
	h.srv.Clock = func() time.Time { return now }
	followServerClock(h.srv, h.training)
}

func (h *metricsSyncHarness) fixClock() { h.setClock(histClock) }

func (h *metricsSyncHarness) ftpChanges() []workout.FTPChange {
	h.t.Helper()
	got, err := h.training.ListFTPChanges(context.Background(), "wilant", "", "")
	if err != nil {
		h.t.Fatal(err)
	}
	return got
}

func describeChange(c workout.FTPChange) [3]any { return [3]any{c.From, c.To, c.Source} }

func wantChanges(t *testing.T, got []workout.FTPChange, want ...[3]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("FTP changes = %+v, want %v", got, want)
	}
	for i := range want {
		if describeChange(got[i]) != want[i] {
			t.Errorf("change %d = %v, want %v", i, describeChange(got[i]), want[i])
		}
	}
}

func TestSavingTheProfileRecordsAManualFTPChangeOnlyWhenFTPMoves(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.fixClock()
	for _, body := range []string{`{"ftpWatts":250}`, `{"ftpWatts":250,"maxHr":188}`, `{"ftpWatts":262}`} {
		if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", body); resp.StatusCode != http.StatusOK {
			t.Fatalf("save %s: status %d", body, resp.StatusCode)
		}
	}
	got := h.ftpChanges()
	wantChanges(t, got, [3]any{0.0, 250.0, "manual"}, [3]any{250.0, 262.0, "manual"})
	if got[0].Date != "2026-09-10" {
		t.Errorf("date = %q, want the store's day", got[0].Date)
	}
}

func TestAcceptingATestSuggestionRecordsTheChangeOnceAsATest(t *testing.T) {
	h := newFTPResultHarnessAt(t, "ramp", 400, 1200, workout.RiderProfile{FTPWatts: 250}, histClock)
	h.sync()
	sug := h.pending()
	if len(sug) != 1 {
		t.Fatalf("pending = %+v", sug)
	}
	for _, want := range []int{http.StatusOK, http.StatusConflict} { // the second accept is a double click
		if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/thresholds/"+sug[0].ID, `{"action":"accept"}`); resp.StatusCode != want {
			t.Fatalf("accept status = %d, want %d", resp.StatusCode, want)
		}
	}
	// 250 was typed by the seed; the accepted ramp test took it to 300.
	wantChanges(t, h.ftpChanges(), [3]any{0.0, 250.0, "manual"}, [3]any{250.0, 300.0, "test"})
}

func TestAnAutoAppliedTestResultIsRecordedAsATest(t *testing.T) {
	h := newFTPResultHarnessAt(t, "ramp", 400, 1200, workout.RiderProfile{FTPWatts: 260, FTPEstimated: true}, histClock)
	if out := h.sync(); len(out.FTPTests) != 1 || out.FTPTests[0].Outcome != "applied" {
		t.Fatalf("sync = %+v", out.FTPTests)
	}
	wantChanges(t, h.ftpChanges(), [3]any{0.0, 260.0, "manual"}, [3]any{260.0, 300.0, "test"})
	// Syncing again changes nothing, so nothing is appended.
	h.sync()
	if got := len(h.ftpChanges()); got != 2 {
		t.Errorf("a second sync left %d changes, want 2", got)
	}
}

func TestAnAutoAppliedRideFindingIsRecordedAsDetection(t *testing.T) {
	start := time.Date(2026, time.September, 9, 8, 0, 0, 0, time.UTC)
	fake := &fakeGarmin{
		activities: []garmin.Activity{{ID: "8101", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 280}},
		fitByID:    map[string][]byte{"8101": buildRideFIT(t, start, 1200, 280)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.fixClock()
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	wantChanges(t, h.ftpChanges(), [3]any{0.0, 266.0, "detection"})
}

func TestSyncStoresTheProvidersAscentAndLeavesZeroWhenItReportsNone(t *testing.T) {
	start := time.Date(2026, time.September, 9, 8, 0, 0, 0, time.UTC)
	fake := &fakeGarmin{activities: []garmin.Activity{
		{ID: "8201", Sport: "cycling", StartTime: start, DurationSeconds: 3600, AscentM: 412},
		{ID: "8202", Sport: "cycling", StartTime: start.AddDate(0, 0, -1), DurationSeconds: 3600},
	}}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.fixClock()
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")

	got := map[string]float64{}
	sessions, _ := h.training.ListSessions(context.Background(), "wilant")
	for _, s := range sessions {
		got[s.ID] = s.AscentM
	}
	if got["garmin:8201"] != 412 || got["garmin:8202"] != 0 || len(got) != 2 {
		t.Errorf("ascents = %v, want 412 and 0 (not reported)", got)
	}
}

func TestAnImportedRideKeepsItsAscent(t *testing.T) {
	h := newImportHarness(t)
	spec := rideimporttest.Spec{Start: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), Seconds: 1800, Watts: 200, HR: 140, AscentM: 350}
	if st := h.runImport("wilant", map[string][]byte{"a.zip": stravaZip(t, spec)}); st.Added != 1 {
		t.Fatalf("import = %+v", st)
	}
	sessions, _ := h.store.ListSessions(context.Background(), "wilant")
	if len(sessions) != 1 || sessions[0].AscentM != 350 {
		t.Errorf("imported sessions = %+v, want the file's 350 m of climbing", sessions)
	}
}
