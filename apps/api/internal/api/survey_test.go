package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type surveyOut struct {
	Feel   int    `json:"feel"`
	Legs   string `json:"legs"`
	Stress string `json:"stress"`
}

func rateSurvey(t *testing.T, h *trainingHarness, rider, session, body string) (int, surveyOut) {
	t.Helper()
	resp := h.as(rider, "cyclists", http.MethodPut, "/api/training/sessions/"+session+"/feel", body)
	var out surveyOut
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp.StatusCode, out
}

func TestTheSurveyIsAFullReplace(t *testing.T) {
	h := newTrainingHarness(t)
	seedThresholdWorkoutAndAnalysis(t, h, "wilant", "s1", "nailed", 5.0)

	status, out := rateSurvey(t, h, "wilant", "s1", `{"feel":3,"legs":"heavy","stress":"high"}`)
	if status != http.StatusOK || out != (surveyOut{Feel: 3, Legs: "heavy", Stress: "high"}) {
		t.Fatalf("first save = %d %+v", status, out)
	}
	stored, _, _ := h.store.GetAnalysis(context.Background(), "s1")
	if stored.Legs != "heavy" || stored.Stress != "high" {
		t.Errorf("stored = %+v", stored)
	}

	// Sending only the effort clears the rest.
	status, out = rateSurvey(t, h, "wilant", "s1", `{"feel":3}`)
	if status != http.StatusOK || out != (surveyOut{Feel: 3}) {
		t.Errorf("effort only = %d %+v, want legs and stress cleared", status, out)
	}
	status, out = rateSurvey(t, h, "wilant", "s1", `{"feel":3,"legs":"","stress":"low"}`)
	if status != http.StatusOK || out != (surveyOut{Feel: 3, Stress: "low"}) {
		t.Errorf("empty legs = %d %+v", status, out)
	}
}

func TestTheSurveyRejectsWhatItDoesNotKnow(t *testing.T) {
	h := newTrainingHarness(t)
	seedThresholdWorkoutAndAnalysis(t, h, "wilant", "s1", "nailed", 5.0)
	for _, body := range []string{
		`{"legs":"heavy"}`,              // effort is required: the survey starts with it
		`{"feel":3,"legs":"wobbly"}`,    // unknown legs
		`{"feel":3,"stress":"extreme"}`, // unknown stress
		`{"feel":3,"legs":"HEAVY"}`,     // values are exact
		`{"feel":3,"legs":"low"}`,       // a stress word is not a legs word
		`{"feel":0,"legs":"heavy"}`,
	} {
		if status, _ := rateSurvey(t, h, "wilant", "s1", body); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, status)
		}
	}
	if a, _, _ := h.store.GetAnalysis(context.Background(), "s1"); a.Feel != 0 || a.Legs != "" {
		t.Errorf("a rejected survey changed the analysis: %+v", a)
	}
}

func TestTheSurveyIsOwnerOnly(t *testing.T) {
	h := newTrainingHarness(t)
	seedThresholdWorkoutAndAnalysis(t, h, "wilant", "s1", "nailed", 5.0)
	if status, _ := rateSurvey(t, h, "someoneelse", "s1", `{"feel":3,"legs":"heavy"}`); status != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for another rider's session", status)
	}
	if a, _, _ := h.store.GetAnalysis(context.Background(), "s1"); a.Legs != "" || a.Feel != 0 {
		t.Errorf("another rider's survey landed: %+v", a)
	}
}

// TestAnAllOutNailedRideEarnsNoLevelBump: effort 5 turns a nailed ride into a
// struggle for progression too, so the level stays where it was rather than
// taking the +0.1 floor; and re-rating undoes exactly what the last rating did.
func TestAnAllOutNailedRideEarnsNoLevelBump(t *testing.T) {
	h := newTrainingHarness(t)
	seedThresholdWorkoutAndAnalysis(t, h, "wilant", "s1", "nailed", 5.0)
	level := func() float64 {
		levels, _ := h.store.ListLevels(context.Background(), "wilant")
		for _, l := range levels {
			if l.Zone == workout.ZoneThreshold {
				return l.Level
			}
		}
		t.Fatal("no threshold level")
		return 0
	}
	rateSurvey(t, h, "wilant", "s1", `{"feel":5}`)
	if got := level(); got != 5.0 {
		t.Errorf("level after all-out = %v, want 5.0 (no bump)", got)
	}
	rateSurvey(t, h, "wilant", "s1", `{"feel":1}`)
	if got := level(); got != 5.5 {
		t.Errorf("level after re-rating easy = %v, want 5.5", got)
	}
	rateSurvey(t, h, "wilant", "s1", `{"feel":4}`)
	if got := level(); got != 5.2 {
		t.Errorf("level after re-rating very hard = %v, want 5.2 (the table's 0.3 - 0.1 on 5.0)", got)
	}
}

func rideOn(t *testing.T, h *tomorrowHarness, ext, date string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: ext, Sport: "cycling", Date: date, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	sessions, err := h.store.ListSessions(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.ExternalID == ext {
			return s.ID
		}
	}
	t.Fatalf("no session %s", ext)
	return ""
}

func TestHeavyLegsTwoDaysRunningEasesTodaysHardSessionLikeAnHRVCaution(t *testing.T) {
	h := newTomorrowHarness(t) // today is 2026-03-19
	ctx := context.Background()
	for _, day := range []string{"2026-03-18", "2026-03-17"} {
		id := rideOn(t, h, "ride-"+day, day)
		if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{SessionID: id, Rider: "wilant", Outcome: "nailed"}); err != nil {
			t.Fatal(err)
		}
		if err := h.store.SetAnalysisSurvey(ctx, id, 3, "heavy", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	orig := h.hard(tmToday)

	h.srv.AdaptWorkouts(ctx)

	got, _ := h.store.GetWorkout(ctx, orig.ID)
	if got.Level != 4 {
		t.Fatalf("level = %v, want stepped down one rung from 5", got.Level)
	}
	a, ok := adjustmentFor(t, h.store, orig.ID)
	if !ok || a.Rule != why.ReadinessCaution {
		t.Fatalf("adjustment = %+v (found %v), want readiness_caution, never rest", a, ok)
	}
	in := decodeInputs[why.ReadinessInputs](t, a)
	if in.Verdict != "caution" || len(in.Signals) != 1 || in.Signals[0].Kind != "survey_legs" {
		t.Errorf("inputs = %+v", in)
	}
}

func TestOneHeavyReportChangesNothing(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	id := rideOn(t, h, "ride-yesterday", "2026-03-18")
	_ = h.store.SaveAnalysis(ctx, workout.SessionAnalysis{SessionID: id, Rider: "wilant", Outcome: "nailed"})
	_ = h.store.SetAnalysisSurvey(ctx, id, 3, "heavy", "high", 0)
	orig := h.hard(tmToday)

	h.srv.AdaptWorkouts(ctx)

	if got, _ := h.store.GetWorkout(ctx, orig.ID); got.Level != 5 {
		t.Errorf("level = %v, want the session untouched", got.Level)
	}
}

func TestAnAllOutNailedRideStepsTheNextSameZoneSessionDownThroughTheAdapter(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	prev := h.hard("2026-03-18")
	id := rideOn(t, h, "ride-all-out", "2026-03-18")
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{SessionID: id, Rider: "wilant", WorkoutID: prev.ID, Outcome: "nailed"}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetAnalysisSurvey(ctx, id, 5, "", "", 0); err != nil {
		t.Fatal(err)
	}
	next := h.hard(tmToday)

	h.srv.AdaptWorkouts(ctx)

	got, _ := h.store.GetWorkout(ctx, next.ID)
	if got.Level != 4 {
		t.Fatalf("level = %v, want stepped down: the ride was all-out", got.Level)
	}
	a, ok := adjustmentFor(t, h.store, next.ID)
	if !ok || a.Rule != why.StruggleStepDown {
		t.Fatalf("adjustment = %+v (found %v)", a, ok)
	}
	in := decodeInputs[why.StruggleStepDownInputs](t, a)
	if !in.FeltAllOut || in.LevelFrom != 5 || in.LevelTo != 4 {
		t.Errorf("inputs = %+v, want all-out and levels 5 -> 4", in)
	}
}
