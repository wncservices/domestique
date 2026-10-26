package llm

import (
	"errors"
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }
func int0(v int) *int        { return &v }

func weekFigures() WeekFigures {
	return WeekFigures{
		WeekStart:   "2026-10-19",
		Phase:       "build",
		TargetTSS:   f64(320),
		RiddenTSS:   f64(290),
		DebtTSS:     nil,
		ForgivenTSS: nil,
		Done:        int0(4),
		Partial:     int0(1),
		Missed:      int0(2),
		CTLStart:    nil,
		CTLEnd:      f64(55),
	}
}

func TestWeeklySummaryOmitsUnknownFigures(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"You rode 290 TSS against a target of 320."}`))
	c := newClient(t, f.srv.URL, "")

	if _, err := c.WeeklySummary(t.Context(), weekFigures()); err != nil {
		t.Fatalf("WeeklySummary: %v", err)
	}
	_, user := sentMessages(t, f)
	for _, want := range []string{"2026-10-19", "320", "290", "4 done"} {
		if !strings.Contains(user, want) {
			t.Errorf("user message does not carry %q:\n%s", want, user)
		}
	}
	for _, absent := range []string{"CTL", "Debt", "Forgiven"} {
		if strings.Contains(user, absent) {
			t.Errorf("user message carries %q, but the figure is unknown:\n%s", absent, user)
		}
	}
}

func TestWeeklySummaryRejectsAnInventedNumber(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"You rode 350 TSS this week."}`))
	c := newClient(t, f.srv.URL, "")

	_, err := c.WeeklySummary(t.Context(), weekFigures())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestWeeklySummaryAcceptsFiguresFromTheInput(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"Four sessions done. Load is on track for 320 TSS."}`))
	c := newClient(t, f.srv.URL, "")

	text, err := c.WeeklySummary(t.Context(), weekFigures())
	if err != nil {
		t.Fatalf("WeeklySummary: %v", err)
	}
	if text == "" {
		t.Error("expected text")
	}
}

func TestWeeklySummaryKeepsRiderDataOutOfTheSystemMessage(t *testing.T) {
	f := newFake(t, answer(t, `{"text":"A steady week."}`))
	c := newClient(t, f.srv.URL, "")

	w := weekFigures()
	w.Phase = "Quinoa phase"
	if _, err := c.WeeklySummary(t.Context(), w); err != nil {
		t.Fatalf("WeeklySummary: %v", err)
	}
	system, user := sentMessages(t, f)
	if !strings.Contains(user, "Quinoa phase") {
		t.Errorf("user message does not carry the phase:\n%s", user)
	}
	if strings.Contains(system, "Quinoa") || strings.Contains(system, "320") {
		t.Errorf("system message carries rider data")
	}
}
