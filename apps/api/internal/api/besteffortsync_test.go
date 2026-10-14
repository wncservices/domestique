package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/rideimport/rideimporttest"
)

// effortClock is the fixed "now" of these tests, with an explicit zone. Rides
// are dated relative to it as plain date strings, so the outcome is the same
// under TZ=UTC and TZ=Europe/Brussels.
var effortClock = time.Date(2026, time.September, 10, 9, 0, 0, 0, time.UTC)

type effortSyncResult struct {
	Synced      int      `json:"synced"`
	Warnings    []string `json:"warnings"`
	BestEfforts []struct {
		SessionID string  `json:"sessionId"`
		Metric    string  `json:"metric"`
		Label     string  `json:"label"`
		Unit      string  `json:"unit"`
		Scope     string  `json:"scope"`
		Value     float64 `json:"value"`
		Previous  float64 `json:"previous"`
	} `json:"bestEfforts"`
}

// newEffortHarness is the metrics-sync harness on a fixed clock, with a rider
// who already has three earlier values for the three power windows a 400 s ride
// can fill, so a new best is possible (a scope needs three prior values).
func newEffortHarness(t *testing.T, acts []garmin.Activity, fits map[string][]byte) (*metricsSyncHarness, *bytes.Buffer) {
	t.Helper()
	return newEffortHarnessAt(t, acts, fits, effortClock)
}

func newEffortHarnessAt(t *testing.T, acts []garmin.Activity, fits map[string][]byte, now time.Time) (*metricsSyncHarness, *bytes.Buffer) {
	t.Helper()
	h := newMetricsSyncHarness(t, &fakeGarmin{activities: acts, fitByID: fits})
	h.seedGarminSession("wilant")
	h.srv.Clock = func() time.Time { return now }
	followServerClock(h.srv, h.training)
	logs := &bytes.Buffer{}
	h.srv.Log = slog.New(slog.NewTextHandler(logs, nil))

	for i, date := range []string{"2026-03-01", "2026-04-01", "2026-05-01"} {
		w := float64(250 + 5*i) // 250, 255, 260
		if _, err := h.training.UpsertEfforts(context.Background(), "wilant", "seed-"+date, "cycling", date,
			map[string]float64{"power_5": w, "power_60": w, "power_300": w}); err != nil {
			t.Fatal(err)
		}
	}
	return h, logs
}

func (h *metricsSyncHarness) syncEfforts() effortSyncResult {
	h.t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("sync status = %d", resp.StatusCode)
	}
	var out effortSyncResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func rideAt(t *testing.T, id string, start time.Time, watts int) (garmin.Activity, []byte) {
	t.Helper()
	return garmin.Activity{ID: id, Sport: "cycling", StartTime: start, DurationSeconds: 400, AvgPowerWatts: float64(watts)},
		buildRideFIT(t, start, 400, watts)
}

func TestSyncAwardsANewBestOnceAndLogsNoValue(t *testing.T) {
	start := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	act, fit := rideAt(t, "7001", start, 317)
	h, logs := newEffortHarness(t, []garmin.Activity{act}, map[string][]byte{"7001": fit})

	first := h.syncEfforts()
	if len(first.BestEfforts) != 3 {
		t.Fatalf("bestEfforts = %+v, want the 5-sec, 1-min and 5-min bests", first.BestEfforts)
	}
	for _, b := range first.BestEfforts {
		if b.SessionID != "garmin:7001" || b.Scope != "all_time" || b.Value != 317 || b.Previous != 260 || b.Label == "" || b.Unit != "W" {
			t.Errorf("award = %+v, want an all-time best of 317 W over 260 W on the synced ride", b)
		}
	}
	// The ride is stored with its award, and a second sync says nothing new.
	best, ok, _ := h.training.BestIn(context.Background(), "wilant", "power_300", "2026-01-01", "2026-12-31")
	if !ok || best.SessionID != "garmin:7001" || best.Award != "all_time" {
		t.Errorf("stored best = %+v %v, want the synced ride with its award", best, ok)
	}
	if again := h.syncEfforts(); len(again.BestEfforts) != 0 {
		t.Errorf("a second sync announced %+v again", again.BestEfforts)
	}

	// Rider, metric and scope are in the log; the watts are not.
	out := logs.String()
	if !strings.Contains(out, "best effort recorded") || !strings.Contains(out, "metric=power_300") || !strings.Contains(out, "scope=all_time") {
		t.Errorf("no log line naming the rider, metric and scope:\n%s", out)
	}
	for _, leak := range []string{"317", "260"} {
		if strings.Contains(out, leak) {
			t.Errorf("the log carries a health value (%s):\n%s", leak, out)
		}
	}
}

func TestSyncDoesNotLetAnOlderRideClaimARecordANewerOneBeats(t *testing.T) {
	older := time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	a1, f1 := rideAt(t, "7002", older, 280)
	a2, f2 := rideAt(t, "7003", newer, 320)
	h, _ := newEffortHarness(t, []garmin.Activity{a1, a2}, map[string][]byte{"7002": f1, "7003": f2})

	out := h.syncEfforts()
	for _, b := range out.BestEfforts {
		if b.SessionID == "garmin:7002" {
			t.Errorf("the older ride was awarded %+v although the newer one in the same sync beats it", b)
		}
	}
	if len(out.BestEfforts) != 3 {
		t.Errorf("bestEfforts = %+v, want only the newer ride's three", out.BestEfforts)
	}
	// The older ride's efforts still count towards the record book.
	if vals, _ := h.training.ScopeValues(context.Background(), "wilant", "power_300", "2026-09-08", "2026-09-08", ""); len(vals) != 1 || vals[0] != 280 {
		t.Errorf("older ride's values = %v, want 280 stored", vals)
	}
}

func TestSyncStoresALateAnalysedRideWithoutAnAward(t *testing.T) {
	// Five days before the clock: analysed more than 3 days after its date.
	start := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	act, fit := rideAt(t, "7004", start, 317)
	h, _ := newEffortHarness(t, []garmin.Activity{act}, map[string][]byte{"7004": fit})

	out := h.syncEfforts()
	if len(out.BestEfforts) != 0 {
		t.Errorf("a late-analysed ride toasted %+v", out.BestEfforts)
	}
	best, ok, _ := h.training.BestIn(context.Background(), "wilant", "power_300", "2026-01-01", "2026-12-31")
	if !ok || best.SessionID != "garmin:7004" || best.Award != "" {
		t.Errorf("stored best = %+v %v, want the ride counted with no award", best, ok)
	}
}

// A ride can be awarded for three days after its date, counted on the rider's
// local calendar (the training timezone, Europe/Brussels by default), not UTC.
// The dates come from the clock, and a late-evening clock is the case that
// differs: at 23:30 UTC the rider's day has already turned over.
func TestTheFourthDayIsTheLastDayARideCanBeAwarded(t *testing.T) {
	brussels, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	for name, now := range map[string]time.Time{
		"morning":      time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC),
		"late evening": time.Date(2026, 9, 10, 23, 30, 0, 0, time.UTC),
	} {
		t.Run(name, func(t *testing.T) {
			today := now.In(brussels)
			for days, want := range map[int]int{3: 3, 4: 0} {
				day := time.Date(today.Year(), today.Month(), today.Day()-days, 8, 0, 0, 0, time.UTC)
				act, fit := rideAt(t, "7010", day, 317)
				h, _ := newEffortHarnessAt(t, []garmin.Activity{act}, map[string][]byte{"7010": fit}, now)
				if got := len(h.syncEfforts().BestEfforts); got != want {
					t.Errorf("a ride %d days before the rider's today (%s, clock %s) got %d awards, want %d",
						days, day.Format("2006-01-02"), now.Format(time.RFC3339), got, want)
				}
			}
		})
	}
}

func TestABackgroundSyncAwardsAndTheNextManualSyncAnnouncesItOnce(t *testing.T) {
	start := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	act, fit := rideAt(t, "7005", start, 317)
	h, _ := newEffortHarness(t, []garmin.Activity{act}, map[string][]byte{"7005": fit})
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "admin"); err != nil {
		t.Fatal(err)
	}

	// Nobody is looking: the loop records the ride and its award, and discards
	// its own result.
	h.srv.AutoScheduleTick(context.Background())
	pending, _ := h.training.UnannouncedAwards(context.Background(), "wilant")
	if len(pending) != 3 {
		t.Fatalf("pending awards after a background sync = %+v, want 3", pending)
	}

	// The rider's own sync is the first thing that can tell them, so it does,
	// even though it found nothing new itself; and only once.
	first := h.syncEfforts()
	if len(first.BestEfforts) != 3 {
		t.Errorf("manual sync announced %+v, want the three the background pass made", first.BestEfforts)
	}
	if second := h.syncEfforts(); len(second.BestEfforts) != 0 {
		t.Errorf("announced again: %+v", second.BestEfforts)
	}
}

func TestSyncNeverRecordsEffortsForAnUnreadableFile(t *testing.T) {
	// Every record is dated 1995: the file decodes but not one second of it is
	// usable, so the ride is scored from the provider's summary and has no
	// samples to take a best from.
	act := garmin.Activity{ID: "7006", Sport: "cycling", StartTime: time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC), DurationSeconds: 400, AvgPowerWatts: 317}
	fit := buildRideFIT(t, time.Date(1995, 1, 1, 8, 0, 0, 0, time.UTC), 400, 317)
	h, _ := newEffortHarness(t, []garmin.Activity{act}, map[string][]byte{"7006": fit})

	out := h.syncEfforts()
	if len(out.BestEfforts) != 0 {
		t.Errorf("an unreadable ride was awarded %+v", out.BestEfforts)
	}
	if _, ok, _ := h.training.GetAnalysis(context.Background(), "garmin:7006"); !ok {
		t.Fatal("the ride was not analysed at all, so this test proves nothing")
	}
	vals, _ := h.training.ScopeValues(context.Background(), "wilant", "power_300", "2026-09-09", "2026-09-09", "")
	if len(vals) != 0 {
		t.Errorf("an unreadable ride has efforts %v", vals)
	}
}

func TestImportedRidesCountTowardsBestsButNeverAwardOrAnnounce(t *testing.T) {
	h := newImportHarness(t)
	// Three older rides, then a much stronger one the day before the import's
	// "now": inside the 3-day window, and it would be a record for a synced ride.
	var specs []rideimporttest.Spec
	for i, w := range []int{200, 205, 210} {
		// Different lengths, or the importer takes neighbouring days for one ride twice.
		specs = append(specs, ride(i+1, 1800+600*i, w))
	}
	specs = append(specs, rideimporttest.Spec{Start: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC), Seconds: 1800, Watts: 330, HR: 150})
	st := h.runImport("wilant", map[string][]byte{"a.zip": stravaZip(t, specs...)})
	if st.State != "done" || st.Added != 4 {
		t.Fatalf("import = %+v", st)
	}

	ctx := context.Background()
	best, ok, err := h.store.BestIn(ctx, "wilant", "power_1200", "", "")
	if err != nil || !ok {
		t.Fatalf("best 20 min: %v %v", ok, err)
	}
	if best.Value != 330 || best.Date != "2026-10-03" {
		t.Errorf("best 20 min = %+v, want the imported ride to count", best)
	}
	if best.Award != "" {
		t.Errorf("an imported ride earned the award %q", best.Award)
	}
	if pending, _ := h.store.UnannouncedAwards(ctx, "wilant"); len(pending) != 0 {
		t.Errorf("a bulk import left %d awards to announce", len(pending))
	}
	// Every ride is in the book, not only the strongest.
	if vals, _ := h.store.ScopeValues(ctx, "wilant", "power_1200", "", "", ""); len(vals) != 4 {
		t.Errorf("%d imported values stored, want 4", len(vals))
	}
}

func TestStartupRebuildFillsAnEmptyTableFromTheAnalysesAndLogsOnlyACount(t *testing.T) {
	act, fit := rideAt(t, "7007", time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC), 317)
	h, logs := newEffortHarness(t, []garmin.Activity{act}, map[string][]byte{"7007": fit})
	h.syncEfforts()
	if _, err := h.db.Conn().Exec(`DELETE FROM best_efforts`); err != nil {
		t.Fatal(err)
	}

	if err := h.srv.RebuildBestEfforts(context.Background()); err != nil {
		t.Fatal(err)
	}
	best, ok, _ := h.training.BestIn(context.Background(), "wilant", "power_300", "", "")
	if !ok || best.Value != 317 || best.Award != "" {
		t.Errorf("rebuilt best = %+v %v, want the analysed ride with no award", best, ok)
	}
	if !strings.Contains(logs.String(), "best efforts rebuilt") || strings.Contains(logs.String(), "317") {
		t.Errorf("rebuild log wrong:\n%s", logs.String())
	}
}
