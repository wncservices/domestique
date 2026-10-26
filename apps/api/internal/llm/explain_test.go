package llm

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// sentMessages returns the system and user messages of the last request f
// received, so a test can check what the model was told and what it was shown.
func sentMessages(t *testing.T, f *fake) (system, user string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs, _ := f.body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v, want a system and a user message", msgs)
	}
	s, _ := msgs[0].(map[string]any)
	u, _ := msgs[1].(map[string]any)
	system, _ = s["content"].(string)
	user, _ = u["content"].(string)
	return system, user
}

func TestExplainPlanSendsTheGoalAndPlanShapeAndReturnsTheText(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"This is your base phase, building steadily."}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	goal := workout.Goal{Name: "Local Century", EventDate: "2026-11-11"}
	plan := periodization.Plan{
		Adjustment: 0.7,
		Weeks: []periodization.Week{
			{Number: 1, StartDate: "2026-09-07", Phase: periodization.PhaseBase, TargetHours: 3},
			{Number: 2, StartDate: "2026-09-14", Phase: periodization.PhaseBase, TargetHours: 2.1, Adjusted: true},
			{Number: 3, StartDate: "2026-09-21", Phase: periodization.PhaseBase, Recovery: true, TargetHours: 1.2, Adjusted: true},
		},
	}

	text, err := c.ExplainPlan(t.Context(), goal, plan)
	if err != nil {
		t.Fatalf("ExplainPlan: %v", err)
	}
	if text != "This is your base phase, building steadily." {
		t.Errorf("text = %q", text)
	}

	_, user := sentMessages(t, f)
	for _, want := range []string{"Local Century", "2026-11-11", "base", "recovery", "70%"} {
		if !strings.Contains(user, want) {
			t.Errorf("user message does not mention %q:\n%s", want, user)
		}
	}
}

func TestExplainPlanSaysAnEmptyPlanHasNoWeeks(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"Set an event date to get started."}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	text, err := c.ExplainPlan(t.Context(), workout.Goal{Name: "Someday Race"}, periodization.Plan{})
	if err != nil {
		t.Fatalf("ExplainPlan: %v", err)
	}
	if text == "" {
		t.Error("expected non-empty text")
	}
	_, user := sentMessages(t, f)
	if !strings.Contains(user, "no weeks yet") {
		t.Errorf("user message does not say the plan is empty:\n%s", user)
	}
}

func TestExplainPlanTrimsTheText(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"  Steady.  \n"}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	text, err := c.ExplainPlan(t.Context(), workout.Goal{Name: "x"}, periodization.Plan{})
	if err != nil {
		t.Fatalf("ExplainPlan: %v", err)
	}
	if text != "Steady." {
		t.Errorf("text = %q, want the trimmed text", text)
	}
}

func TestEmptyExplanationIsInvalidAfterOneRetry(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"   "}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	_, err := c.ExplainPlan(t.Context(), workout.Goal{Name: "x"}, periodization.Plan{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", n)
	}
}

func TestExplainPlanEchoesNoBodyOnNon200(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error": "invalid x-api-key, my note says I am ill"}`)
	})
	c := newClient(t, f.srv.URL+"/v1", "")

	_, err := c.ExplainPlan(t.Context(), workout.Goal{Name: "x"}, periodization.Plan{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want it to name the 401 status", err)
	}
	if strings.Contains(err.Error(), "my note") {
		t.Errorf("err = %v echoes the reply body", err)
	}
}
