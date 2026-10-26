package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestProposeProfileChangeParsesAWellFormedProposal(t *testing.T) {
	f := newFake(t, answer(t, `{"availableDays": ["sat", "sun"], "hoursPerAvailableDay": 3, "explanation": "Only free on weekends while traveling."}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	profile := workout.RiderProfile{AvailableDays: []string{"mon", "wed", "fri"}, HoursPerAvailableDay: 1.5}
	note := "I'm traveling for work next week, only free on the weekend"
	proposal, err := c.ProposeProfileChange(t.Context(), note, profile)
	if err != nil {
		t.Fatalf("ProposeProfileChange: %v", err)
	}
	if len(proposal.AvailableDays) != 2 || proposal.AvailableDays[0] != "sat" || proposal.AvailableDays[1] != "sun" {
		t.Errorf("AvailableDays = %v", proposal.AvailableDays)
	}
	if proposal.HoursPerAvailableDay != 3 {
		t.Errorf("HoursPerAvailableDay = %v", proposal.HoursPerAvailableDay)
	}
	if proposal.Explanation == "" {
		t.Error("expected a non-empty explanation")
	}
	_, user := sentMessages(t, f)
	if !strings.Contains(user, "mon, wed, fri") {
		t.Errorf("user message did not include the current profile:\n%s", user)
	}
	if !strings.Contains(user, note) {
		t.Errorf("user message does not carry the rider's note:\n%s", user)
	}
}

func TestProposeProfileChangeLowercasesAndDropsUnknownDays(t *testing.T) {
	f := newFake(t, answer(t, `{"availableDays": ["SAT", "funday", "sun"], "hoursPerAvailableDay": 2, "explanation": "ok"}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	proposal, err := c.ProposeProfileChange(t.Context(), "note", workout.RiderProfile{})
	if err != nil {
		t.Fatalf("ProposeProfileChange: %v", err)
	}
	if len(proposal.AvailableDays) != 2 || proposal.AvailableDays[0] != "sat" || proposal.AvailableDays[1] != "sun" {
		t.Errorf("AvailableDays = %v", proposal.AvailableDays)
	}
}

// The schema forbids fences, and a server that ignores the schema and fences
// its JSON gets the same treatment as prose: one retry, then ErrInvalid. A
// fenced answer is never half-parsed.
func TestFencedJSONIsInvalidAfterOneRetry(t *testing.T) {
	f := newFake(t, answer(t, "```json\n{\"availableDays\": [\"tue\"], \"hoursPerAvailableDay\": 2, \"explanation\": \"ok\"}\n```"))
	c := newClient(t, f.srv.URL+"/v1", "")

	_, err := c.ProposeProfileChange(t.Context(), "note", workout.RiderProfile{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestProposeProfileChangeFallsBackToCurrentProfileOnInvalidValues(t *testing.T) {
	// "friday-ish" is not a day and 40 hours a day fails the sanity bound. Both
	// are corrected to the rider's own values, not retried.
	f := newFake(t, answer(t, `{"availableDays": ["friday-ish"], "hoursPerAvailableDay": 40, "explanation": "no real change"}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	profile := workout.RiderProfile{AvailableDays: []string{"mon", "thu"}, HoursPerAvailableDay: 2}
	proposal, err := c.ProposeProfileChange(t.Context(), "note", profile)
	if err != nil {
		t.Fatalf("ProposeProfileChange: %v", err)
	}
	if len(proposal.AvailableDays) != 2 || proposal.AvailableDays[0] != "mon" || proposal.AvailableDays[1] != "thu" {
		t.Errorf("AvailableDays = %v, want fallback to profile's own %v", proposal.AvailableDays, profile.AvailableDays)
	}
	if proposal.HoursPerAvailableDay != 2 {
		t.Errorf("HoursPerAvailableDay = %v, want fallback to profile's own 2", proposal.HoursPerAvailableDay)
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1: a corrected field is not retried", n)
	}
}

func TestProposeProfileChangeRejectsNonJSONText(t *testing.T) {
	f := newFake(t, answer(t, "I think you should take it easy next week."))
	c := newClient(t, f.srv.URL+"/v1", "")

	_, err := c.ProposeProfileChange(t.Context(), "note", workout.RiderProfile{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestProfileSystemMessageHasNoRiderNote(t *testing.T) {
	f := newFake(t, answer(t, `{"availableDays": [], "hoursPerAvailableDay": 1, "explanation": "ok"}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	const note = "UNIQUE-NOTE-7731 I am travelling"
	if _, err := c.ProposeProfileChange(t.Context(), note, workout.RiderProfile{}); err != nil {
		t.Fatalf("ProposeProfileChange: %v", err)
	}
	system, user := sentMessages(t, f)
	if strings.Contains(system, "UNIQUE-NOTE-7731") {
		t.Errorf("system message carries the rider's note:\n%s", system)
	}
	if !strings.Contains(user, "UNIQUE-NOTE-7731") {
		t.Errorf("user message does not carry the rider's note:\n%s", user)
	}
}
