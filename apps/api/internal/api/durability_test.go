package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// buildDriftRideFIT is a steady-power ride whose heart rate is firstBPM until
// secondHalfFrom seconds and secondBPM after it, so the ride's aerobic
// decoupling is whatever the two heart rates imply. bpm 0 means no heart rate.
func buildDriftRideFIT(t *testing.T, start time.Time, seconds, watts, firstBPM, secondBPM, secondHalfFrom int) []byte {
	t.Helper()
	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).
		SetType(typedef.FileActivity).
		SetManufacturer(typedef.ManufacturerDevelopment).
		SetProduct(0).
		SetTimeCreated(start).
		SetSerialNumber(0)
	for i := 0; i < seconds; i++ {
		r := mesgdef.NewRecord(nil).SetTimestamp(start.Add(time.Duration(i) * time.Second)).SetPower(uint16(watts))
		bpm := firstBPM
		if i >= secondHalfFrom {
			bpm = secondBPM
		}
		if bpm > 0 {
			r.SetHeartRate(uint8(bpm))
		}
		act.Records = append(act.Records, r)
	}
	fitFile := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fitFile); err != nil {
		t.Fatalf("encode fixture fit: %v", err)
	}
	return buf.Bytes()
}

// syncLongRide plans a generated "Long ride" of plannedSeconds, syncs a ride of
// rideSeconds with the given heart rates, and returns the harness and session.
func syncLongRide(t *testing.T, plannedSeconds, rideSeconds, firstBPM, secondBPM int) (*metricsSyncHarness, string) {
	t.Helper()
	start := time.Now().AddDate(0, 0, -1)
	date := start.Format("2006-01-02")
	fake := &fakeGarmin{
		activities: []garmin.Activity{
			{ID: "7100", Sport: "cycling", StartTime: start, DurationSeconds: float64(rideSeconds), AvgPowerWatts: 150},
		},
		fitByID: map[string][]byte{"7100": buildDriftRideFIT(t, start, rideSeconds, 150, firstBPM, secondBPM, 3300)},
	}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"ftpWatts":200}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("save profile: status = %d", resp.StatusCode)
	}
	if _, err := h.srv.Training.CreateWorkout(context.Background(), api.LegacyFlagged(workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "Long ride", Date: date, GoalID: "g1",
		Zone: workout.ZoneEndurance, Description: workout.GeneratedDescription,
		Steps: []workout.WorkoutStep{
			{Name: "Ride", Duration: workout.DurationTime, Seconds: float64(plannedSeconds), Target: workout.TargetOpen},
		},
	})); err != nil {
		t.Fatalf("create long ride: %v", err)
	}
	return h, "garmin:7100"
}

func (h *metricsSyncHarness) syncOK() {
	h.t.Helper()
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", ""); resp.StatusCode != http.StatusOK {
		h.t.Fatalf("sync status = %d", resp.StatusCode)
	}
}

func (h *metricsSyncHarness) enduranceLevel() (workout.ProgressionLevel, bool) {
	h.t.Helper()
	levels, err := h.srv.Training.ListLevels(context.Background(), "wilant")
	if err != nil {
		h.t.Fatal(err)
	}
	for _, l := range levels {
		if l.Sport == model.SportCycling && l.Zone == workout.ZoneEndurance {
			return l, true
		}
	}
	return workout.ProgressionLevel{}, false
}

func TestALongRideWithLowDecouplingRaisesEndurance(t *testing.T) {
	h, session := syncLongRide(t, 6000, 6000, 130, 133) // ~2.3% drift
	h.syncOK()

	l, ok := h.enduranceLevel()
	if !ok || l.Level != 3.3 { // unset experience starts at 3.0
		t.Fatalf("endurance = %+v, ok=%v, want 3.3", l, ok)
	}
	a, _, err := h.srv.Training.GetAnalysis(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if a.LevelDelta != 0.3 {
		t.Errorf("LevelDelta = %v, want 0.3", a.LevelDelta)
	}
}

func TestACutShortLongRideLowersEndurance(t *testing.T) {
	h, _ := syncLongRide(t, 9000, 6000, 130, 133) // 67% of planned
	h.syncOK()

	if l, ok := h.enduranceLevel(); !ok || l.Level != 2.8 {
		t.Fatalf("endurance = %+v, ok=%v, want 2.8", l, ok)
	}
}

func TestAHighDecouplingLongRideLowersEndurance(t *testing.T) {
	h, _ := syncLongRide(t, 6000, 6000, 130, 150) // ~13% drift
	h.syncOK()

	if l, ok := h.enduranceLevel(); !ok || l.Level != 2.8 {
		t.Fatalf("endurance = %+v, ok=%v, want 2.8", l, ok)
	}
}

func TestNoDecouplingNoMove(t *testing.T) {
	h, session := syncLongRide(t, 6000, 6000, 0, 0) // no heart rate: no decoupling
	h.syncOK()

	if l, ok := h.enduranceLevel(); ok && l.Level != 3.0 {
		t.Fatalf("endurance moved to %v without a decoupling", l.Level)
	}
	a, _, err := h.srv.Training.GetAnalysis(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if a.DecouplingPct != nil || a.LevelDelta != 0 {
		t.Errorf("analysis = decoupling %v, delta %v, want none", a.DecouplingPct, a.LevelDelta)
	}
	got, err := h.srv.Training.LatestAdjustments(context.Background(), "wilant", workout.SubjectLevel, []string{"cycling:endurance"})
	if err != nil || len(got) != 0 {
		t.Errorf("adjustments = %v, err %v, want none", got, err)
	}
}

func TestDurabilityMoveRecordsItsWhy(t *testing.T) {
	h, _ := syncLongRide(t, 6000, 6000, 130, 133)
	h.syncOK()

	got, err := h.srv.Training.LatestAdjustments(context.Background(), "wilant", workout.SubjectLevel, []string{"cycling:endurance"})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := got["cycling:endurance"]
	if !ok || a.Rule != why.DurabilityMove {
		t.Fatalf("adjustment = %+v, ok=%v, want a durability_move", a, ok)
	}
	if a.Inputs["from"] != 3.0 || a.Inputs["to"] != 3.3 || a.Inputs["durationRatio"] != 1.0 {
		t.Errorf("inputs = %v", a.Inputs)
	}
	if _, ok := a.Inputs["decoupling"].(float64); !ok {
		t.Errorf("inputs = %v, want a decoupling", a.Inputs)
	}
}

func TestDurabilityLogsCarryNoValues(t *testing.T) {
	h, _ := syncLongRide(t, 6000, 6000, 130, 133)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.syncOK()

	for _, bad := range []string{"3.3", "decoupling", "durationRatio", "2.3", "endurance"} {
		if strings.Contains(logs.String(), bad) {
			t.Errorf("log carries %q:\n%s", bad, logs.String())
		}
	}
}

// A survey re-rate recomputes a structured zone's move from the outcome. A
// durability move has no outcome to recompute, so rating the ride must leave
// the endurance level and the stored delta alone.
func TestASurveyDoesNotReapplyADurabilityMove(t *testing.T) {
	h, session := syncLongRide(t, 6000, 6000, 130, 133)
	h.syncOK()

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/sessions/"+session+"/feel", `{"feel":2}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feel status = %d", resp.StatusCode)
	}
	if l, _ := h.enduranceLevel(); l.Level != 3.3 {
		t.Errorf("endurance after a survey = %v, want 3.3", l.Level)
	}
	a, _, err := h.srv.Training.GetAnalysis(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if a.LevelDelta != 0.3 {
		t.Errorf("LevelDelta after a survey = %v, want 0.3", a.LevelDelta)
	}
}
