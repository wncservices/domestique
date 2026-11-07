package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// touchFuture makes one session of the goal dated after today the rider's own
// edit, the way readiness easing, a move or a rename does in real use.
func (h *seasonHarness) touchFuture(t *testing.T, goalID string) workout.Workout {
	t.Helper()
	today := h.now.Format("2006-01-02")
	for _, w := range h.goalSessions(t, goalID) {
		if w.Date > today {
			name := "My own name"
			if _, err := h.store.UpdateWorkout(context.Background(), w.ID, workout.UpdateWorkoutRequest{Name: &name}); err != nil {
				t.Fatal(err)
			}
			return w
		}
	}
	t.Fatal("fixture: the goal has no future session")
	return workout.Workout{}
}

// The journey from production: a running goal whose plan the rider and
// readiness had changed here and there, deleted, then a new goal described in
// a sentence. With no goal the calendar is empty from today on; the sentence
// names a half marathon, so the new goal is running even when the model says
// cycling, and nothing of a cycling plan appears.
func TestDeleteAGoalThenProposeAHalfMarathonPlansOnlyRunning(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"mon", "wed", "fri", "sun"}, 250)
	old := h.createGoal(t, `{"name":"Half marathon","sport":"running","eventDate":"2027-04-11"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	h.touchFuture(t, old.ID)

	h.deleteGoal(t, old.ID)
	if got := h.futureBySport(t); len(got) != 0 {
		t.Fatalf("with no goal the calendar still holds %v from today on", got)
	}

	withLLM(t, h.srv, fixed(`{"name":"Half marathon of Leuven","sport":"cycling","eventDate":"2027-04-11","eventMonth":0,"eventPartOfMonth":"","eventDayOfMonth":0,"eventWeekday":"","eventYear":0,"eventYearOffset":0,"eventInWeeks":0,"eventInMonths":0,"priority":"A","targetDistanceKm":21.1,"targetDistanceUnit":"km","targetElevationM":0,"targetElevationUnit":"m","explanation":"x"}`))
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals/propose", `{"note":"Half marathon of Leuven on 11 April 2027"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("propose: status %d", resp.StatusCode)
	}
	var p struct {
		Name      string `json:"name"`
		Sport     string `json:"sport"`
		EventDate string `json:"eventDate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Sport != "running" {
		t.Fatalf("a half marathon was proposed as %q, want running", p.Sport)
	}

	body, _ := json.Marshal(map[string]string{"name": p.Name, "sport": p.Sport, "eventDate": p.EventDate})
	h.createGoal(t, string(body))
	h.srv.WaitForBackground()
	got := h.futureBySport(t)
	if got[model.SportCycling] != 0 || got[model.SportRunning] == 0 {
		t.Errorf("after the new goal the calendar holds %v from today on, want running only", got)
	}
}

// Switching a goal's sport takes every one of its sessions from today on, the
// changed ones too: a renamed or eased cycling ride is no use in a running plan.
func TestChangingAGoalsSportLeavesNoSessionOfTheOldSport(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	touched := h.touchFuture(t, g.ID)
	touched.Name = "My own name"

	h.patchGoal(t, g.ID, `{"sport":"running"}`)

	if got := h.futureBySport(t); got[model.SportCycling] != 0 || got[model.SportRunning] == 0 {
		t.Errorf("after the switch the calendar holds %v from today on, want running only", got)
	}
	for _, w := range h.workouts(t) {
		if w.Name == touched.Name && w.Sport == model.SportCycling && w.Date == touched.Date {
			t.Error("the changed cycling session survived the switch to running")
		}
	}
}
