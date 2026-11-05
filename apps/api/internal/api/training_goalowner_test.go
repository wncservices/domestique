package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A workout's goal id is a link into the rider's own plan. Letting a rider
// point a workout at someone else's goal would put that goal's season pass
// and its ledger on another rider's session. The answer for a goal that is
// missing and for one that belongs to somebody else is the same 404, so a
// probe cannot tell them apart. No admin bypass, as with the goal itself.

func goalNotFoundBody(t *testing.T, resp *http.Response) {
	t.Helper()
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["error"] != "goal not found" {
		t.Errorf("error = %q, want %q", out["error"], "goal not found")
	}
}

func storedWorkout(t *testing.T, h *trainingHarness, id string) workout.Workout {
	t.Helper()
	wk, err := h.store.GetWorkout(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return wk
}

func TestCreateWorkoutRefusesAnotherRidersGoal(t *testing.T) {
	h := newTrainingHarness(t)
	g := decodeGoal(t, h.as("someone-else", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Their Race"}`))

	before, err := h.store.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"sport":"cycling","name":"Sneaky","date":"2026-10-03","goalId":%q,"steps":[{"name":"ride","duration":"time","seconds":3600,"target":"open"}]}`, g.ID)
	for _, groups := range []string{"cyclists", "admins"} {
		resp := h.as("wilant", groups, http.MethodPost, "/api/training/workouts", body)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("groups=%s: status = %d, want 404", groups, resp.StatusCode)
		}
		goalNotFoundBody(t, resp)
	}

	after, err := h.store.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("workouts = %d after refused create, want %d", len(after), len(before))
	}
}

func TestCreateWorkoutAcceptsOwnGoal(t *testing.T) {
	h := newTrainingHarness(t)
	g := decodeGoal(t, h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"My Race"}`))

	body := fmt.Sprintf(`{"sport":"cycling","name":"Mine","date":"2026-10-03","goalId":%q,"steps":[{"name":"ride","duration":"time","seconds":3600,"target":"open"}]}`, g.ID)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	stored := storedWorkout(t, h, decodeWorkoutOut(t, resp).ID)
	if stored.GoalID != g.ID {
		t.Errorf("goalId = %q, want %q", stored.GoalID, g.ID)
	}
}

func TestUpdateWorkoutRefusesAnotherRidersGoal(t *testing.T) {
	h := newTrainingHarness(t)
	own := decodeGoal(t, h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Mine"}`))
	theirs := decodeGoal(t, h.as("someone-else", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Theirs"}`))

	wk := decodeWorkoutOut(t, h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts",
		fmt.Sprintf(`{"sport":"cycling","name":"Base","date":"2026-10-03","goalId":%q,"steps":[{"name":"ride","duration":"time","seconds":3600,"target":"open"}]}`, own.ID)))

	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+wk.ID,
		fmt.Sprintf(`{"goalId":%q}`, theirs.ID))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	goalNotFoundBody(t, resp)

	if stored := storedWorkout(t, h, wk.ID); stored.GoalID != own.ID {
		t.Errorf("stored goalId = %q, want unchanged %q", stored.GoalID, own.ID)
	}
}

func TestUpdateWorkoutAcceptsOwnGoalAndEmptyGoalClearsIt(t *testing.T) {
	h := newTrainingHarness(t)
	own := decodeGoal(t, h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Mine"}`))
	wk := decodeWorkoutOut(t, h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts",
		fmt.Sprintf(`{"sport":"cycling","name":"Base","date":"2026-10-03","goalId":%q,"steps":[{"name":"ride","duration":"time","seconds":3600,"target":"open"}]}`, own.ID)))

	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+wk.ID, `{"goalId":""}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty goalId: status = %d, want 200", resp.StatusCode)
	}
	if stored := storedWorkout(t, h, wk.ID); stored.GoalID != "" {
		t.Errorf("goalId = %q after clearing, want empty", stored.GoalID)
	}

	resp = h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+wk.ID, fmt.Sprintf(`{"goalId":%q}`, own.ID))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own goalId: status = %d, want 200", resp.StatusCode)
	}
}
