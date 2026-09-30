package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// profileAdjustment is the stored reason for a profile threshold. It is kept
// under the level kind with a "profile:<field>" subject: thresholds are
// neither a workout nor a zone, and the two kinds are all the table has.
func profileAdjustment(t *testing.T, store *workout.DB, field string) (workout.Adjustment, bool) {
	t.Helper()
	id := "profile:" + field
	got, err := store.LatestAdjustments(context.Background(), "wilant", workout.SubjectLevel, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := got[id]
	return a, ok
}

func TestAnAutoAppliedThresholdFromRidesRecordsWhy(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{
		activities: []garmin.Activity{{ID: "8001", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 280}},
		fitByID:    map[string][]byte{"8001": buildRideFIT(t, start, 1200, 280)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	a, ok := profileAdjustment(t, h.srv.Training, "ftp")
	if !ok || a.Rule != why.ThresholdAuto {
		t.Fatalf("adjustment = %+v (found %v), want threshold_auto", a, ok)
	}
	in := decodeInputs[why.ThresholdAutoInputs](t, a)
	if in.Field != "ftp" || in.From != 0 || in.To != 280*0.95 || in.Source != "rides" || in.Reason == "" {
		t.Errorf("inputs = %+v", in)
	}
}

func TestAnAutoAppliedTestResultRecordsSourceTest(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 260, FTPEstimated: true})
	h.sync()

	a, ok := profileAdjustment(t, h.srv.Training, "ftp")
	if !ok || a.Rule != why.ThresholdAuto {
		t.Fatalf("adjustment = %+v (found %v)", a, ok)
	}
	in := decodeInputs[why.ThresholdAutoInputs](t, a)
	if in.From != 260 || in.To != 300 || in.Source != "test" {
		t.Errorf("inputs = %+v, want 260 -> 300 from the test", in)
	}
}

func TestASuggestedOrAcceptedThresholdRecordsNothing(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 400, workout.RiderProfile{FTPWatts: 250}) // rider-typed: only suggested
	h.sync()
	pending := h.pending()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	if _, ok := profileAdjustment(t, h.srv.Training, "ftp"); ok {
		t.Fatal("a suggestion recorded an automatic change")
	}
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/thresholds/"+pending[0].ID, `{"action":"accept"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept status = %d", resp.StatusCode)
	}
	if _, ok := profileAdjustment(t, h.srv.Training, "ftp"); ok {
		t.Error("accepting is the rider's own action and must not record threshold_auto")
	}
}
