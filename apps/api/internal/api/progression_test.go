package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// TestGetProgressionInitialisesLevelsFromAGoal drives Task 5's
// GET /api/training/progression: a rider with a cycling goal but nothing
// scheduled yet (levelsFor's own seeding in scheduleGoal has never run) gets
// every cycling zone's starting level initialised on first read, from their
// profile's experience level — the same seedLevelsForRidersGoals/levelsFor
// path scheduleGoal uses.
func TestGetProgressionInitialisesLevelsFromAGoal(t *testing.T) {
	h := newTrainingHarness(t)
	ctx := context.Background()

	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{Rider: "wilant", ExperienceLevel: "advanced"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", Sport: model.SportCycling}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/progression", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Levels []struct {
			Sport string  `json:"sport"`
			Zone  string  `json:"zone"`
			Level float64 `json:"level"`
		} `json:"levels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Levels) != 5 {
		t.Fatalf("levels = %+v, want the 5 cycling structured zones", out.Levels)
	}
	for _, l := range out.Levels {
		if l.Sport != "cycling" || l.Level != 6.0 {
			t.Errorf("level %+v: want sport=cycling level=6.0 (advanced)", l)
		}
	}
}

// TestGetProgressionReturnsExistingLevelsWithoutReseeding proves a rider who
// already has levels saved gets exactly those back — no re-initialisation
// overwriting a level that has already moved.
func TestGetProgressionReturnsExistingLevelsWithoutReseeding(t *testing.T) {
	h := newTrainingHarness(t)
	ctx := context.Background()

	if err := h.store.SaveLevel(ctx, workout.ProgressionLevel{
		Rider: "wilant", Sport: model.SportCycling, Zone: workout.ZoneThreshold, Level: 7.3, Reason: "already moved",
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/progression", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Levels []struct {
			Zone   string  `json:"zone"`
			Level  float64 `json:"level"`
			Reason string  `json:"reason"`
		} `json:"levels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Levels) != 1 || out.Levels[0].Level != 7.3 || out.Levels[0].Reason != "already moved" {
		t.Fatalf("levels = %+v, want exactly the one saved level, untouched", out.Levels)
	}
}

func TestGetProgressionIsOwnerOnly(t *testing.T) {
	h := newTrainingHarness(t)
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/progression", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// A different rider's request must never see wilant's data — there is
	// nothing to seed for them (no goal, no profile), so this only proves
	// the response is scoped to the caller, not shared.
	resp2 := h.as("someoneelse", "cyclists", http.MethodGet, "/api/training/progression", "")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp2.StatusCode)
	}
	var out struct {
		Levels []struct{} `json:"levels"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Levels) != 0 {
		t.Errorf("levels = %+v, want none — someoneelse has no goals to seed from", out.Levels)
	}
}

// seedThresholdWorkoutAndAnalysis creates a structured threshold workout at
// the given level and a matching analysis with the given outcome, returning
// the session id feel tests rate.
func seedThresholdWorkoutAndAnalysis(t *testing.T, h *trainingHarness, rider, sessionID, outcome string, workoutLevel float64) {
	t.Helper()
	ctx := context.Background()
	wk, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: rider, Sport: model.SportCycling, Name: "Threshold 3x12",
		Zone: workout.ZoneThreshold, Level: workoutLevel,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveLevel(ctx, workout.ProgressionLevel{
		Rider: rider, Sport: model.SportCycling, Zone: workout.ZoneThreshold, Level: 5.0, Reason: "starting point",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: sessionID, Rider: rider, WorkoutID: wk.ID, Outcome: outcome,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSetSessionFeelReAppliesTheLevelChange is the spec's own "re-rate
// replaces the change rather than stacking a second one": rating the same
// nailed session first with feel 1 (easy) then with feel 5 (all-out) must
// leave the rider's level exactly where feel 5 alone would have put it, not
// wherever feel-1's bump plus feel-5's bump would land.
func TestSetSessionFeelReAppliesTheLevelChange(t *testing.T) {
	h := newTrainingHarness(t)
	seedThresholdWorkoutAndAnalysis(t, h, "wilant", "sess-1", "nailed", 5.0)

	rate := func(feel int) (int, float64) {
		resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/sessions/sess-1/feel",
			fmt.Sprintf(`{"feel":%d}`, feel))
		var out struct {
			Feel int `json:"feel"`
		}
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(resp.Body).Decode(&out)
		}
		levels, err := h.store.ListLevels(context.Background(), "wilant")
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range levels {
			if l.Zone == workout.ZoneThreshold {
				return resp.StatusCode, l.Level
			}
		}
		return resp.StatusCode, 0
	}

	status, afterEasy := rate(1)
	if status != http.StatusOK {
		t.Fatalf("rate(1) status = %d, want 200", status)
	}
	// nailed, diff=0 (5.0 workout level vs cur 5.0): bump = 0.3+0.2=0.5 -> 5.5
	if afterEasy != 5.5 {
		t.Fatalf("level after feel=1 = %v, want 5.5", afterEasy)
	}

	status, afterHard := rate(5)
	if status != http.StatusOK {
		t.Fatalf("rate(5) status = %d, want 200", status)
	}
	// Re-rate must undo the 0.5 bump and reapply from the same cur=5.0:
	// bump = 0.3-0.2=0.1 -> 5.1, not 5.5+0.1=5.6.
	if afterHard != 5.1 {
		t.Fatalf("level after re-rating feel=5 = %v, want 5.1 (replaced, not stacked)", afterHard)
	}
}

func TestSetSessionFeelRejectsOutOfRangeValues(t *testing.T) {
	h := newTrainingHarness(t)
	seedThresholdWorkoutAndAnalysis(t, h, "wilant", "sess-2", "nailed", 5.0)

	for _, body := range []string{`{"feel":0}`, `{"feel":6}`, `{"feel":-1}`, `not json`} {
		resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/sessions/sess-2/feel", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestSetSessionFeel404sForAnotherRidersSession(t *testing.T) {
	h := newTrainingHarness(t)
	seedThresholdWorkoutAndAnalysis(t, h, "wilant", "sess-3", "nailed", 5.0)

	resp := h.as("someoneelse", "cyclists", http.MethodPut, "/api/training/sessions/sess-3/feel", `{"feel":3}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}

	resp2 := h.as("wilant", "cyclists", http.MethodPut, "/api/training/sessions/no-such-session/feel", `{"feel":3}`)
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for an unknown session", resp2.StatusCode)
	}
}
