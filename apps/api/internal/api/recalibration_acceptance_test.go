package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The three places an FTP can change and be persisted — the metrics sync's
// auto-apply, accepting a threshold suggestion, and the rider's own profile
// save — must all recalibrate through the one shared helper and leave the
// same Reason on the Progression card's own data.

// seedRecalRider gives "wilant" a profile calibrated against ftp and a
// threshold level of 5.3 plus a sweet-spot level of 4.0, the shape every
// scenario below starts from.
func seedRecalRider(t *testing.T, h *metricsSyncHarness, ftp float64, estimated bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.srv.Training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", FTPWatts: ftp, FTPEstimated: estimated,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.srv.Training.SetFTPCalibrated(ctx, "wilant", 0, ftp); err != nil {
		t.Fatal(err)
	}
	for zone, level := range map[workout.Zone]float64{workout.ZoneThreshold: 5.3, workout.ZoneSweetSpot: 4.0} {
		if err := h.srv.Training.SaveLevel(ctx, workout.ProgressionLevel{
			Rider: "wilant", Sport: model.SportCycling, Zone: zone, Level: level, Reason: "seeded",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func recalLevel(t *testing.T, h *metricsSyncHarness, zone workout.Zone) (float64, string) {
	t.Helper()
	levels, err := h.srv.Training.ListLevels(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range levels {
		if l.Sport == model.SportCycling && l.Zone == zone {
			return l.Level, l.Reason
		}
	}
	t.Fatalf("no cycling %s level", zone)
	return 0, ""
}

func TestRecalibrationSyncViaGarminBiometrics(t *testing.T) {
	fake := &fakeGarmin{biometrics: garmin.Biometrics{CyclingFTPWatts: 262}}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	seedRecalRider(t, h, 250, true)

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	level, reason := recalLevel(t, h, workout.ZoneThreshold)
	if level != 4.6 || reason != "FTP 250 → 262 W — threshold 5.3 → 4.6" {
		t.Errorf("threshold = %v %q", level, reason)
	}
	sweet, _ := recalLevel(t, h, workout.ZoneSweetSpot)
	if sweet != 3.3 {
		t.Errorf("sweet_spot = %v, want 3.3", sweet)
	}

	// A second pass at the same FTP does nothing further.
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("second sync status = %d", resp.StatusCode)
	}
	if again, _ := recalLevel(t, h, workout.ZoneThreshold); again != 4.6 {
		t.Errorf("threshold after a second sync = %v, want it unchanged at 4.6", again)
	}
}

func TestRecalibrationSyncViaThresholdDetection(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "8001", Sport: "cycling", StartTime: start, DurationSeconds: 1200, AvgPowerWatts: 280},
		},
		fitByID: map[string][]byte{"8001": buildRideFIT(t, start, 1200, 280)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	seedRecalRider(t, h, 250, true) // detection finds 266 (280 x 0.95)

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	level, reason := recalLevel(t, h, workout.ZoneThreshold)
	if level != 4.4 || reason != "FTP 250 → 266 W — threshold 5.3 → 4.4" {
		t.Errorf("threshold = %v %q", level, reason)
	}
}

func TestRecalibrationSyncViaEstimateFTPFallback(t *testing.T) {
	// A ride with average power but no FIT: no power curve, so detection has
	// nothing and the crude EstimateFTP fallback (256.5 W, shown as 256) is what applies.
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "8002", Sport: "cycling", StartTime: time.Now().AddDate(0, 0, -1), DurationSeconds: 1200, AvgPowerWatts: 270},
		},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	seedRecalRider(t, h, 240, true)

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	level, reason := recalLevel(t, h, workout.ZoneThreshold)
	if level != 4.3 || reason != "FTP 240 → 256 W — threshold 5.3 → 4.3" {
		t.Errorf("threshold = %v %q", level, reason)
	}
}

func TestRecalibrationOnThresholdAccept(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	seedRecalRider(t, h, 255, false)

	sug, err := h.srv.Training.CreateSuggestion(context.Background(), workout.ThresholdSuggestion{
		Rider: "wilant", Field: "ftp", Value: 268, Previous: 255,
		SourceSessionID: "garmin:9001", SourceDate: "2026-01-12", Reason: "a strong 20 minute effort",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/thresholds/"+sug.ID, `{"action":"accept"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept status = %d", resp.StatusCode)
	}
	level, reason := recalLevel(t, h, workout.ZoneThreshold)
	if level != 4.6 || reason != "FTP 255 → 268 W — threshold 5.3 → 4.6" {
		t.Errorf("threshold = %v %q", level, reason)
	}
}

func TestRecalibrationOnManualProfileSave(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	seedRecalRider(t, h, 255, false)

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"ftpWatts":268,"maxHr":190}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d", resp.StatusCode)
	}
	level, reason := recalLevel(t, h, workout.ZoneThreshold)
	if level != 4.6 || reason != "FTP 255 → 268 W — threshold 5.3 → 4.6" {
		t.Errorf("threshold = %v %q", level, reason)
	}

	// Saving the very same profile again is a no-op for levels — and the
	// marker survived the form's fresh-struct save, or this would recalibrate
	// 255 -> 268 a second time.
	resp = h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"ftpWatts":268,"maxHr":190}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second save status = %d", resp.StatusCode)
	}
	if again, _ := recalLevel(t, h, workout.ZoneThreshold); again != 4.6 {
		t.Errorf("threshold after an identical re-save = %v, want 4.6", again)
	}
	var p struct {
		FTPWatts float64 `json:"ftpWatts"`
	}
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "")
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil || p.FTPWatts != 268 {
		t.Errorf("profile = %+v err = %v", p, err)
	}
	stored, _, err := h.srv.Training.GetProfile(context.Background(), "wilant")
	if err != nil || stored.FTPLevelsCalibratedAt != 268 {
		t.Errorf("marker = %v err = %v, want 268 preserved through the form save", stored.FTPLevelsCalibratedAt, err)
	}
}

func TestRecalibrationOnManualProfileSaveIgnoresATypo(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	seedRecalRider(t, h, 255, false)

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"ftpWatts":2550}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d", resp.StatusCode)
	}
	if level, _ := recalLevel(t, h, workout.ZoneThreshold); level != 5.3 {
		t.Errorf("threshold = %v, want it untouched by a typo", level)
	}
}
