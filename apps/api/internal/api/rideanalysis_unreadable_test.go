package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
)

// A FIT whose timestamps are all unusable must leave the ride scored from the
// provider's summary, with a Warn that names ids only, and never as a
// zero-length ride.
func TestUnreadableTimestampsFallBackToTheSummaryWithAWarn(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "6100", Sport: "cycling", StartTime: start, DurationSeconds: 1800, AvgPowerWatts: 150, NormalizedPower: 155, TrainingStressScore: 42},
		},
		fitByID: map[string][]byte{"6100": buildRideFIT(t, start, 600, 150)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
	defer api.SetRideAnalysisForTest(func(in rideanalysis.Input) rideanalysis.Analysis {
		if in.Activity != nil {
			for _, r := range in.Activity.Records {
				r.Timestamp = time.Time{} // a device whose clock was never set
			}
		}
		return rideanalysis.Analyze(in)
	}, nil)()

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got, ok, err := h.srv.Training.GetAnalysis(context.Background(), "garmin:6100")
	if err != nil || !ok {
		t.Fatalf("analysis stored = %v (%v)", ok, err)
	}
	if got.LoadSource != "provider_tss" || got.TSS != 42 {
		t.Errorf("loadSource %q tss %v, want the provider's 42", got.LoadSource, got.TSS)
	}
	sessions, _ := h.srv.Training.ListSessions(context.Background(), "wilant")
	if len(sessions) != 1 || sessions[0].DurationSeconds != 1800 {
		t.Errorf("sessions = %+v, want the duration kept at 1800", sessions)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "no usable timestamps") {
		t.Errorf("logs = %s, want a Warn about unusable timestamps", logs.String())
	}
}
