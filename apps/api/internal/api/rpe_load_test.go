package api_test

import (
	"context"
	"math"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func sameLoad(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// ratedRide plants a ride and its analysis, with a second, later ride so a
// change to the first ride's load shows up in the fitness snapshots.
func ratedRide(t *testing.T, h *trainingHarness, req workout.UpsertSessionRequest, loadSource string) string {
	t.Helper()
	ctx := context.Background()
	req.Rider, req.Provider, req.Sport, req.Date = "wilant", "garmin", "cycling", "2026-03-10"
	req.DurationSeconds = 3600
	sess, err := h.store.UpsertSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "later", Sport: "cycling", Date: "2026-03-14", DurationSeconds: 1800, TrainingLoad: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{SessionID: sess.ID, Rider: "wilant", Outcome: "unplanned", LoadSource: loadSource}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.RecomputeFitnessSnapshots(ctx, "wilant"); err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func loadOf(t *testing.T, h *trainingHarness, id string) float64 {
	t.Helper()
	s, err := h.store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return s.TrainingLoad
}

func TestRatingARideWithNoPowerOrHeartRateSetsItsLoadFromTheEffort(t *testing.T) {
	h := newTrainingHarness(t)
	ctx := context.Background()
	id := ratedRide(t, h, workout.UpsertSessionRequest{ExternalID: "plain", TrainingLoad: 50}, "estimate")
	before, _ := h.store.ListFitnessSnapshots(ctx, "wilant")

	if status, _ := rateSurvey(t, h, "wilant", id, `{"feel":5}`); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if got := loadOf(t, h, id); !sameLoad(got, 100) {
		t.Errorf("load = %v, want 100 (an hour at effort 5)", got)
	}
	if a, _, _ := h.store.GetAnalysis(ctx, id); a.LoadSource != "session_rpe" {
		t.Errorf("load basis = %q, want session_rpe", a.LoadSource)
	}
	after, _ := h.store.ListFitnessSnapshots(ctx, "wilant")
	if reflect.DeepEqual(before, after) {
		t.Error("the fitness snapshots were not recomputed")
	}

	// A re-rate replaces the load, it does not stack on it.
	rateSurvey(t, h, "wilant", id, `{"feel":2}`)
	if got := loadOf(t, h, id); !sameLoad(got, 0.65*0.65*100) {
		t.Errorf("load after re-rating = %v, want %v", got, 0.65*0.65*100)
	}
}

func TestRatingNeverReplacesAMeasuredLoad(t *testing.T) {
	cases := map[string]struct {
		req        workout.UpsertSessionRequest
		loadSource string
		profile    workout.RiderProfile
	}{
		"power with an FTP": {
			workout.UpsertSessionRequest{ExternalID: "p", AvgPowerWatts: 200, TrainingLoad: 64}, "fit_power",
			workout.RiderProfile{Rider: "wilant", FTPWatts: 250}},
		"power from the summary alone": {
			workout.UpsertSessionRequest{ExternalID: "p2", AvgPowerWatts: 200, TrainingLoad: 64}, "estimate",
			workout.RiderProfile{Rider: "wilant", FTPWatts: 250}},
		"heart rate with a max": {
			workout.UpsertSessionRequest{ExternalID: "hr", AvgHR: 150, TrainingLoad: 62}, "estimate",
			workout.RiderProfile{Rider: "wilant", MaxHR: 190}},
		"a provider's own TSS": {
			workout.UpsertSessionRequest{ExternalID: "tss", TrainingLoad: 77}, "provider_tss",
			workout.RiderProfile{Rider: "wilant"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newTrainingHarness(t)
			ctx := context.Background()
			if _, err := h.store.SaveProfile(ctx, tc.profile); err != nil {
				t.Fatal(err)
			}
			id := ratedRide(t, h, tc.req, tc.loadSource)
			before, _ := h.store.ListFitnessSnapshots(ctx, "wilant")
			want := loadOf(t, h, id)

			if status, _ := rateSurvey(t, h, "wilant", id, `{"feel":5}`); status != http.StatusOK {
				t.Fatalf("status = %d", status)
			}
			if got := loadOf(t, h, id); got != want {
				t.Errorf("load = %v, want the measured %v untouched", got, want)
			}
			if a, _, _ := h.store.GetAnalysis(ctx, id); a.LoadSource != tc.loadSource {
				t.Errorf("load basis = %q, want %q", a.LoadSource, tc.loadSource)
			}
			if after, _ := h.store.ListFitnessSnapshots(ctx, "wilant"); !reflect.DeepEqual(before, after) {
				t.Error("the fitness snapshots moved for a measured ride")
			}
		})
	}
}

// TestASyncKeepsTheEffortLoadOfARatedRide: the metrics sync rewrites a
// session's load every time it sees the ride, so it has to look the rating up
// or it would put the flat guess straight back.
func TestASyncKeepsTheEffortLoadOfARatedRide(t *testing.T) {
	start := time.Now().AddDate(0, 0, -1)
	fake := &fakeGarmin{activities: []garmin.Activity{{ID: "8801", Sport: "cycling", StartTime: start, DurationSeconds: 3600}}}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	ctx := context.Background()

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("first sync status = %d", resp.StatusCode)
	}
	sess, err := h.srv.Training.GetSession(ctx, "garmin:8801")
	if err != nil || !sameLoad(sess.TrainingLoad, 50) {
		t.Fatalf("first sync: %+v, %v; want the flat 50", sess, err)
	}
	if err := h.srv.Training.SaveAnalysis(ctx, workout.SessionAnalysis{SessionID: sess.ID, Rider: "wilant", Outcome: "unplanned", LoadSource: "estimate"}); err != nil {
		t.Fatal(err)
	}
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/sessions/garmin:8801/feel", `{"feel":3}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rate status = %d", resp.StatusCode)
	}
	want := 0.78 * 0.78 * 100
	if got, _ := h.srv.Training.GetSession(ctx, sess.ID); !sameLoad(got.TrainingLoad, want) {
		t.Fatalf("after rating: %v, want %v", got.TrainingLoad, want)
	}

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("second sync status = %d", resp.StatusCode)
	}
	if got, _ := h.srv.Training.GetSession(ctx, sess.ID); !sameLoad(got.TrainingLoad, want) {
		t.Errorf("after a second sync: %v, want the effort load %v kept", got.TrainingLoad, want)
	}
}
