package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// ownSession posts a session of the rider's own, with no goal, on date.
func (h *seasonHarness) ownSession(t *testing.T, date, extra string) string {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts",
		`{"name":"Gym","sport":"running","date":"`+date+`","steps":[]`+extra+`}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create workout: status %d", resp.StatusCode)
	}
	var out struct {
		ID            string `json:"id"`
		PlanAlongside bool   `json:"planAlongside"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if want := extra != ""; out.PlanAlongside != want {
		t.Fatalf("planAlongside = %v, want %v", out.PlanAlongside, want)
	}
	return out.ID
}

// A session of the rider's own keeps the plan off its day unless the rider
// lets the plan share it.
func TestARidersOwnSessionKeepsThePlanOffItsDayUnlessShared(t *testing.T) {
	const tuesday = "2026-10-13" // next week, an available day
	for name, tc := range map[string]struct {
		extra    string
		wantPlan bool
	}{
		"default": {"", false},
		"shared":  {`,"planAlongside":true`, true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSeasonHarness(t)
			h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
			h.ownSession(t, tuesday, tc.extra)

			g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
			h.srv.WaitForBackground()

			planned := false
			for _, w := range h.goalSessions(t, g.ID) {
				if w.Date == tuesday {
					planned = true
				}
			}
			if planned != tc.wantPlan {
				t.Errorf("plan session on the rider's day = %v, want %v", planned, tc.wantPlan)
			}
		})
	}
}

func TestPlanAlongsideCanBeSwitchedOnAnEdit(t *testing.T) {
	h := newSeasonHarness(t)
	id := h.ownSession(t, "2026-10-13", "")
	for _, want := range []bool{true, false} {
		body := `{"planAlongside":false}`
		if want {
			body = `{"planAlongside":true}`
		}
		if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+id, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("update: status %d", resp.StatusCode)
		}
		w, err := h.store.GetWorkout(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if w.PlanAlongside != want {
			t.Errorf("stored planAlongside = %v, want %v", w.PlanAlongside, want)
		}
	}
	// An edit that does not name it leaves it alone.
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+id, `{"planAlongside":true}`); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+id, `{"name":"Gym session"}`); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	if w, _ := h.store.GetWorkout(context.Background(), id); !w.PlanAlongside {
		t.Error("an edit of the name cleared planAlongside")
	}
}
