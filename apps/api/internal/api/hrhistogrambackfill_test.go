package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// An older ride analysed before histograms existed (here: analysed from the
// provider's summary because its file could not be read then) gets its
// histogram on a later pass, and the fitness response then shows time in zone.
func TestAnOlderRideShowsTimeInZoneAfterTheNextSync(t *testing.T) {
	start := time.Now().AddDate(0, 0, -2)
	fake := &fakeGarmin{
		activities: []garmin.Activity{{ID: "7100", Sport: "cycling", StartTime: start, DurationSeconds: 600, AvgPowerWatts: 150}},
		fitErr:     fmt.Errorf("garmin: activity FIT download returned 500"),
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	if _, err := h.training.SaveProfile(context.Background(), workout.RiderProfile{Rider: "wilant", ThresholdHR: 160, MaxHR: 190}); err != nil {
		t.Fatal(err)
	}
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	a, ok, _ := h.training.GetAnalysis(context.Background(), "garmin:7100")
	if !ok || a.HRHistogram != nil {
		t.Fatalf("setup: analysis present=%v histogram=%v, want an analysis with no histogram", ok, a.HRHistogram)
	}

	fake.fitErr = nil
	fake.fitByID = map[string][]byte{"7100": buildRideFITWithHR(t, start, 600, 150, 150)}
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/fitness", "")
	var out struct {
		Sessions []struct {
			ID       string `json:"id"`
			Analysis *struct {
				HRZones []zoneRow `json:"hrZones"`
			} `json:"analysis"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, s := range out.Sessions {
		if s.ID == "garmin:7100" && s.Analysis != nil {
			for _, z := range s.Analysis.HRZones {
				total += z.Seconds
			}
		}
	}
	if total != 600 {
		t.Errorf("time in zone after the next sync = %d s, want 600", total)
	}
}

func TestHRBackfillNeverFetchesAnImportAndLeavesAFailedDownloadUnfilled(t *testing.T) {
	ctx := context.Background()
	start := time.Now().AddDate(0, 0, -2)
	fake := &fakeGarmin{
		activities: []garmin.Activity{{ID: "7200", Sport: "cycling", StartTime: start, DurationSeconds: 600, AvgPowerWatts: 150}},
		fitErr:     fmt.Errorf("garmin: activity FIT download returned 500"),
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	imp, err := h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "import", ExternalID: "1-abc", Sport: "cycling",
		Date: time.Now().Format("2006-01-02"), DurationSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.training.SaveAnalysis(ctx, workout.SessionAnalysis{SessionID: imp.ID, Rider: "wilant", Outcome: "completed"}); err != nil {
		t.Fatal(err)
	}

	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	fake.fitCalls = nil
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")

	for _, id := range fake.fitCalls {
		if id != "7200" {
			t.Errorf("fetched %q, only the synced ride may be fetched", id)
		}
	}
	for _, id := range []string{imp.ID, "garmin:7200"} {
		if a, _, _ := h.training.GetAnalysis(ctx, id); a.HRHistogram != nil {
			t.Errorf("%s got a histogram without a successful download", id)
		}
	}
}

func TestHRBackfillFillsAtMostTenRidesPerProviderPerPass(t *testing.T) {
	const total = 12
	var activities []garmin.Activity
	fits := map[string][]byte{}
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("r%d", i)
		start := time.Now().AddDate(0, 0, -i-1)
		activities = append(activities, garmin.Activity{ID: id, Sport: "cycling", StartTime: start, DurationSeconds: 600, AvgPowerWatts: 150})
		fits[id] = buildRideFITWithHR(t, start, 600, 150, 140)
	}
	fake := &fakeGarmin{activities: activities, fitErr: fmt.Errorf("garmin: 500")}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")

	fake.fitErr, fake.fitByID, fake.fitCalls = nil, fits, nil
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if len(fake.fitCalls) != 10 {
		t.Fatalf("downloads in one pass = %d, want 10", len(fake.fitCalls))
	}
	filled := 0
	for i := 0; i < total; i++ {
		if a, _, _ := h.training.GetAnalysis(context.Background(), fmt.Sprintf("garmin:r%d", i)); a.HRHistogram != nil {
			filled++
		}
	}
	if filled != 10 {
		t.Errorf("filled = %d, want 10", filled)
	}
	fake.fitCalls = nil
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if len(fake.fitCalls) != 2 {
		t.Errorf("next pass downloads = %d, want the remaining 2", len(fake.fitCalls))
	}
}
