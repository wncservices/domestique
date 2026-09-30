package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type whyOut struct {
	Rule  string `json:"rule"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Day   string `json:"day"`
	Facts []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"facts"`
}

type whyWorkoutOut struct {
	ID  string  `json:"id"`
	Why *whyOut `json:"why"`
}

func plainWorkout(t *testing.T, h *trainingHarness, rider, name, date string) workout.Workout {
	t.Helper()
	wk, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: rider, Sport: model.SportCycling, Name: name, Date: date,
		Steps: []workout.WorkoutStep{{Name: "ride", Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetOpen}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return wk
}

func recordWhy(t *testing.T, h *trainingHarness, rider, id string, rec why.Record, day string) {
	t.Helper()
	if err := h.store.RecordAdjustment(context.Background(), rider, workout.SubjectWorkout, id, rec, day); err != nil {
		t.Fatal(err)
	}
}

func weekWorkouts(t *testing.T, h *trainingHarness) map[string]whyWorkoutOut {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/week", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("week status = %d", resp.StatusCode)
	}
	var out struct {
		Days []struct {
			Planned []whyWorkoutOut `json:"planned"`
		} `json:"days"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]whyWorkoutOut{}
	for _, d := range out.Days {
		for _, w := range d.Planned {
			byID[w.ID] = w
		}
	}
	return byID
}

func TestTheWeekCarriesAWhyOnAdjustedWorkoutsOnly(t *testing.T) {
	h := newTrainingHarness(t)
	h.srv.Clock = weekClock
	adjusted := plainWorkout(t, h, "wilant", "Threshold", "2026-09-30")
	older := why.NewRecord(why.FatigueOverload, "older sentence", why.FatigueOverloadInputs{AnalysedTSS: 300, PlannedTSS: 200, Ratio: 1.5})
	newer := why.NewRecord(why.ReadinessCaution, "Eased one level — HRV is unbalanced today", why.ReadinessInputs{
		Verdict: "caution", Signals: []why.Signal{{Kind: "hrv", Label: "HRV unbalanced", Value: "41 ms vs usual 52"}},
	})
	recordWhy(t, h, "wilant", adjusted.ID, older, "2026-09-29")
	recordWhy(t, h, "wilant", adjusted.ID, newer, "2026-09-30")
	plain := plainWorkout(t, h, "wilant", "Endurance", "2026-10-01")

	got := weekWorkouts(t, h)
	w := got[adjusted.ID].Why
	if w == nil {
		t.Fatal("the adjusted workout has no why")
	}
	if w.Rule != "readiness_caution" || w.Title != "Eased one level" || w.Text != "Eased one level — HRV is unbalanced today" || w.Day != "2026-09-30" {
		t.Errorf("why = %+v, want the latest row", w)
	}
	if len(w.Facts) != 1 || w.Facts[0].Label != "HRV unbalanced" || w.Facts[0].Value != "41 ms vs usual 52" {
		t.Errorf("facts = %+v", w.Facts)
	}
	if got[plain.ID].Why != nil {
		t.Errorf("an unadjusted workout has a why: %+v", got[plain.ID].Why)
	}
}

func TestWorkoutListAndGetCarryTheWhyToo(t *testing.T) {
	h := newTrainingHarness(t)
	wk := plainWorkout(t, h, "wilant", "Threshold", "2026-09-30")
	recordWhy(t, h, "wilant", wk.ID, why.NewRecord(why.MissedMoved, "moved", why.MissedMovedInputs{From: "2026-09-28", To: "2026-09-30"}), "2026-09-30")

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts", "")
	var list []whyWorkoutOut
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Why == nil || list[0].Why.Rule != "missed_moved" || len(list[0].Why.Facts) != 2 {
		t.Errorf("list = %+v", list)
	}
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+wk.ID, "")
	var one whyWorkoutOut
	if err := json.NewDecoder(resp.Body).Decode(&one); err != nil {
		t.Fatal(err)
	}
	if one.Why == nil || one.Why.Rule != "missed_moved" {
		t.Errorf("get = %+v", one)
	}
}

func TestAnotherRidersWorkoutNeverLeaksItsWhy(t *testing.T) {
	h := newTrainingHarness(t)
	theirs := plainWorkout(t, h, "other", "Secret session", "2026-09-30")
	recordWhy(t, h, "other", theirs.ID, why.NewRecord(why.ReadinessRest, "sleep score 31 sentence", why.ReadinessInputs{Verdict: "rest"}), "2026-09-30")

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+theirs.ID, "")
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("status = %d, want a refusal", resp.StatusCode)
	}
	var sb bytes.Buffer
	_, _ = sb.ReadFrom(resp.Body)
	if strings.Contains(sb.String(), "sleep score") || strings.Contains(sb.String(), "readiness_rest") {
		t.Errorf("refusal leaks the row: %s", sb.String())
	}
	list := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts", "")
	var rows []whyWorkoutOut
	_ = json.NewDecoder(list.Body).Decode(&rows)
	if len(rows) != 0 {
		t.Errorf("wilant's list holds %+v", rows)
	}
}

func TestALevelCarriesItsRecalibrationWhyAndOtherwiseOnlyItsReason(t *testing.T) {
	h := newTrainingHarness(t)
	ctx := context.Background()
	for _, zone := range []workout.Zone{workout.ZoneThreshold, workout.ZoneTempo} {
		if err := h.store.SaveLevel(ctx, workout.ProgressionLevel{Rider: "wilant", Sport: model.SportCycling, Zone: zone, Level: 4, Reason: "the level's own reason"}); err != nil {
			t.Fatal(err)
		}
	}
	rec := why.NewRecord(why.LevelRecalibration, "FTP 250 → 280 W — lowered", why.LevelRecalibrationInputs{
		FTPFrom: 250, FTPTo: 280, Zone: "threshold", LevelFrom: 5, LevelTo: 4, Trigger: "auto_applied",
	})
	if err := h.store.RecordAdjustment(ctx, "wilant", workout.SubjectLevel, "cycling:threshold", rec, "2026-09-30"); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/progression", "")
	var out struct {
		Levels []struct {
			Zone   string  `json:"zone"`
			Reason string  `json:"reason"`
			Why    *whyOut `json:"why"`
		} `json:"levels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, l := range out.Levels {
		switch l.Zone {
		case "threshold":
			seen++
			if l.Why == nil || l.Why.Rule != "level_recalibration" || l.Why.Title != "Level recalibrated" || len(l.Why.Facts) != 4 {
				t.Errorf("threshold why = %+v", l.Why)
			}
		case "tempo":
			seen++
			if l.Why != nil || l.Reason != "the level's own reason" {
				t.Errorf("tempo = %+v, want only its reason", l)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("levels = %+v", out.Levels)
	}
}
