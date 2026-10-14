package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/filedef"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
)

// A panic while scoring or decoding one ride must not take the process down
// (the metrics sync runs on a background goroutine that nothing recovers), and
// must not repeat on every sync: the ride is recorded as analysed from the
// provider's own summary, and the failure is logged at Error with ids only.
func TestSyncSurvivesAPanicWhileAnalysingARide(t *testing.T) {
	for name, hook := range map[string]func() func(){
		"analysis": func() func() {
			return api.SetRideAnalysisForTest(func(in rideanalysis.Input) rideanalysis.Analysis {
				if in.Activity != nil {
					panic("boom while analysing")
				}
				return rideanalysis.Analyze(in)
			}, nil)
		},
		"decode": func() func() {
			return api.SetRideAnalysisForTest(nil, func([]byte) (*filedef.Activity, error) { panic("boom while decoding") })
		},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now().AddDate(0, 0, -1)
			fake := &fakeGarmin{
				activities: []garmin.Activity{
					{ID: "5900", Sport: "cycling", StartTime: start, DurationSeconds: 600, AvgPowerWatts: 150, NormalizedPower: 155, TrainingStressScore: 30},
				},
				fitByID: map[string][]byte{"5900": buildRideFIT(t, start, 600, 150)},
			}
			h := newMetricsSyncHarness(t, fake)
			h.seedGarminSession("wilant")
			var logs bytes.Buffer
			h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
			defer hook()()

			resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if _, ok, err := h.srv.Training.GetAnalysis(context.Background(), "garmin:5900"); err != nil || !ok {
				t.Fatalf("analysis stored = %v (err %v), want the summary fallback so it is not retried forever", ok, err)
			}
			if !strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("logs = %q, want an Error line", logs.String())
			}
			fake.fitCalls = nil
			h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
			if len(fake.fitCalls) != 0 {
				t.Errorf("fit calls on the next sync = %v, want none", fake.fitCalls)
			}
		})
	}
}
