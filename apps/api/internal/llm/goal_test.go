package llm

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var goalToday = time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)

func TestProposeGoalParsesAWellFormedProposal(t *testing.T) {
	f := newFake(t, answer(t, `{"name":"Gran Fondo","sport":"cycling","eventDate":"2026-06-14","priority":"A","targetDistanceKm":180,"targetElevationM":2400,"explanation":"A mid-June century."}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	p, err := c.ProposeGoal(t.Context(), "gran fondo, 180km, 2400m of climbing, mid June", goalToday)
	if err != nil {
		t.Fatalf("ProposeGoal: %v", err)
	}
	if p.Name != "Gran Fondo" || p.Sport != "cycling" || p.EventDate != "2026-06-14" || p.Priority != "A" ||
		p.TargetDistanceM != 180000 || p.TargetElevationM != 2400 {
		t.Errorf("proposal = %+v", p)
	}
	// The model has no clock: today must be in the prompt or relative dates
	// are guesses.
	_, user := sentMessages(t, f)
	if !strings.Contains(user, "2026-03-18") {
		t.Errorf("user message does not carry today's date:\n%s", user)
	}
}

func TestProposeGoalDropsWhatItCannotTrust(t *testing.T) {
	f := newFake(t, answer(t, `{"name":"Mystery Ride","sport":"swimming","eventDate":"2024-06-14","priority":"Z","targetDistanceKm":99999,"targetElevationM":-5,"explanation":""}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	p, err := c.ProposeGoal(t.Context(), "something", goalToday)
	if err != nil {
		t.Fatalf("ProposeGoal: %v", err)
	}
	if p.Sport != "cycling" || p.Priority != "B" {
		t.Errorf("sport/priority = %q/%q, want the safe defaults", p.Sport, p.Priority)
	}
	if p.EventDate != "" {
		t.Errorf("eventDate = %q, want empty: a date in the past is a year-guessing mistake", p.EventDate)
	}
	if p.TargetDistanceM != 0 || p.TargetElevationM != 0 {
		t.Errorf("targets = %v/%v, want none: both were implausible", p.TargetDistanceM, p.TargetElevationM)
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1: corrected fields are not retried", n)
	}
}

func TestProposeGoalCutsALongNameTo80Characters(t *testing.T) {
	long := strings.Repeat("a", 120)
	f := newFake(t, answer(t, `{"name":"`+long+`","sport":"running","eventDate":"","priority":"C","targetDistanceKm":0,"targetElevationM":0,"explanation":""}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	p, err := c.ProposeGoal(t.Context(), "x", goalToday)
	if err != nil {
		t.Fatalf("ProposeGoal: %v", err)
	}
	if len(p.Name) != 80 || p.Sport != "running" || p.Priority != "C" {
		t.Errorf("name length %d, sport %q, priority %q", len(p.Name), p.Sport, p.Priority)
	}
}

func TestProposeGoalWithAnEmptyNameIsInvalidAfterOneRetry(t *testing.T) {
	f := newFake(t, answer(t, `{"name":"  ","sport":"cycling","eventDate":"","priority":"B","targetDistanceKm":0,"targetElevationM":0,"explanation":""}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	_, err := c.ProposeGoal(t.Context(), "x", goalToday)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", n)
	}
}

func TestProposeGoalRejectsProseAsInvalid(t *testing.T) {
	f := newFake(t, answer(t, "sorry, I can't help with that"))
	c := newClient(t, f.srv.URL+"/v1", "")

	if _, err := c.ProposeGoal(t.Context(), "x", goalToday); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestGoalSystemMessageHasNoRiderNote(t *testing.T) {
	f := newFake(t, answer(t, `{"name":"Race","sport":"cycling","eventDate":"","priority":"B","targetDistanceKm":0,"targetElevationM":0,"explanation":""}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	const note = "UNIQUE-NOTE-4410 gran fondo"
	if _, err := c.ProposeGoal(t.Context(), note, goalToday); err != nil {
		t.Fatalf("ProposeGoal: %v", err)
	}
	system, user := sentMessages(t, f)
	if strings.Contains(system, "UNIQUE-NOTE-4410") {
		t.Errorf("system message carries the rider's note:\n%s", system)
	}
	if !strings.Contains(user, "UNIQUE-NOTE-4410") {
		t.Errorf("user message does not carry the rider's note:\n%s", user)
	}
}

// Every enum the model may choose from is fixed in the schema, so a translated
// value such as "zaterdag" or "B-prioriteit" cannot pass as a choice.
func TestEnumsAreInTheSchema(t *testing.T) {
	days := profileSchema["properties"].(map[string]any)["availableDays"].(map[string]any)["items"].(map[string]any)["enum"]
	if !reflect.DeepEqual(days, []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}) {
		t.Errorf("profile day enum = %v, want exactly mon..sun", days)
	}
	props := goalSchema["properties"].(map[string]any)
	if got := props["priority"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Errorf("goal priority enum = %v, want exactly A, B, C", got)
	}
	if got := props["sport"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []string{"cycling", "running"}) {
		t.Errorf("goal sport enum = %v, want exactly cycling, running", got)
	}
}
