package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
)

func oneChange() []Change {
	return []Change{{
		Title:   "Eased one level for readiness.",
		Workout: "Endurance ride",
		Date:    "2026-10-27",
		Facts:   []why.Fact{{Label: "Duration", Value: "20"}},
	}}
}

func TestExplainChangesRejectsAnInventedNumber(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"Your ride is now 25 minutes. It was shortened for recovery."}`))
	c := newClient(t, f.srv.URL, "")

	_, err := c.ExplainChanges(t.Context(), "2026-10-21", oneChange())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestExplainChangesAcceptsNumbersFromTheInput(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"Your ride is now 20 minutes, eased for recovery."}`))
	c := newClient(t, f.srv.URL, "")

	text, err := c.ExplainChanges(t.Context(), "2026-10-21", oneChange())
	if err != nil {
		t.Fatalf("ExplainChanges: %v", err)
	}
	if text != "Your ride is now 20 minutes, eased for recovery." {
		t.Errorf("text = %q", text)
	}
	if f.requests.Load() != 1 {
		t.Errorf("requests = %d, want 1", f.requests.Load())
	}
}

func TestExplainChangesTooLongIsInvalid(t *testing.T) {
	long := strings.Repeat("a", 301)
	f := newFake(t, answer(t, `{"text":"`+long+`"}`))
	c := newClient(t, f.srv.URL, "")
	if _, err := c.ExplainChanges(t.Context(), "2026-10-21", oneChange()); !errors.Is(err, ErrInvalid) {
		t.Errorf("301 runes: err = %v, want ErrInvalid", err)
	}

	four := `{"text":"One. Two. Three. Four."}`
	f2 := newFake(t, answer(t, four))
	c2 := newClient(t, f2.srv.URL, "")
	if _, err := c2.ExplainChanges(t.Context(), "2026-10-21", oneChange()); !errors.Is(err, ErrInvalid) {
		t.Errorf("four sentences: err = %v, want ErrInvalid", err)
	}
}

func TestExplainChangesSendsTheChangesAndKeepsRiderDataOutOfTheSystemMessage(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"Eased for recovery."}`))
	c := newClient(t, f.srv.URL, "")

	if _, err := c.ExplainChanges(t.Context(), "2026-10-21", oneChange()); err != nil {
		t.Fatalf("ExplainChanges: %v", err)
	}
	system, user := sentMessages(t, f)
	for _, want := range []string{"Endurance ride", "2026-10-27", "Duration", "Eased one level"} {
		if !strings.Contains(user, want) {
			t.Errorf("user message does not carry %q:\n%s", want, user)
		}
	}
	if strings.Contains(system, "Endurance ride") || strings.Contains(system, "Duration") {
		t.Errorf("system message carries rider data:\n%s", system)
	}
}
