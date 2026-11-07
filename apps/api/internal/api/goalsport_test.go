package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func (h *seasonHarness) patchGoal(t *testing.T, id, body string) {
	t.Helper()
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+id, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("update goal %s: status %d", body, resp.StatusCode)
	}
	h.srv.WaitForBackground()
}

func (h *seasonHarness) goalSessions(t *testing.T, goalID string) []workout.Workout {
	t.Helper()
	var out []workout.Workout
	for _, w := range h.workouts(t) {
		if w.GoalID == goalID {
			out = append(out, w)
		}
	}
	return out
}

// Switching a goal from cycling to running replaces its untouched sessions from
// today on: the season is recorded as filled, so without a rebuild the old
// cycling plan would stay.
func TestChangingAGoalsSportRebuildsItsUntouchedSessions(t *testing.T) {
	for name, body := range map[string]string{
		"dated":   `{"name":"Race","eventDate":"2027-04-14"}`,
		"rolling": `{"name":"Keep going"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newSeasonHarness(t)
			h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
			g := h.createGoal(t, body)
			h.srv.WaitForBackground()
			h.backdate(t)

			h.patchGoal(t, g.ID, `{"sport":"running"}`)

			today := h.now.Format("2006-01-02")
			future := 0
			for _, w := range h.goalSessions(t, g.ID) {
				if w.Date < today {
					continue
				}
				future++
				if w.Sport != model.SportRunning {
					t.Errorf("session %s on %s is %s after the switch, want running", w.ID, w.Date, w.Sport)
				}
			}
			if future == 0 {
				t.Fatal("no sessions from today on after the switch")
			}
		})
	}
}

func TestChangingOtherGoalFieldsDoesNotRebuild(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	ids := func() map[string]bool {
		m := map[string]bool{}
		for _, w := range h.goalSessions(t, g.ID) {
			m[w.ID] = true
		}
		return m
	}
	before := ids()
	h.patchGoal(t, g.ID, `{"name":"Renamed"}`)
	after := ids()
	if len(before) == 0 || len(after) != len(before) {
		t.Fatalf("%d sessions before, %d after a rename", len(before), len(after))
	}
	for id := range before {
		if !after[id] {
			t.Errorf("session %s was rebuilt by a rename", id)
		}
	}
}

func TestChangingAGoalsSportLeavesPastSessionsAlone(t *testing.T) {
	h := newSeasonHarness(t)
	h.now = utcNoon(2026, time.October, 5) // Monday: this week's Tuesday is still ahead
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	h.backdate(t)

	h.now = utcNoon(2026, time.October, 7) // Wednesday: Tuesday is past
	var past workout.Workout
	for _, w := range h.goalSessions(t, g.ID) {
		if w.Date == "2026-10-06" {
			past = w
		}
	}
	if past.ID == "" {
		t.Fatal("no session on Tuesday 2026-10-06 to check")
	}

	h.patchGoal(t, g.ID, `{"sport":"running"}`)

	for _, w := range h.workouts(t) {
		if w.ID == past.ID {
			if w.Sport != model.SportCycling {
				t.Errorf("the past session is %s, want cycling", w.Sport)
			}
			return
		}
	}
	t.Error("the past session was deleted")
}
