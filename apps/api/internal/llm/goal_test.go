package llm

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
	if utf8.RuneCountInString(p.Name) != 80 || p.Sport != "running" || p.Priority != "C" {
		t.Errorf("name length %d runes, sport %q, priority %q", utf8.RuneCountInString(p.Name), p.Sport, p.Priority)
	}
}

// The cut is in runes: a byte cut through a multi-byte name would keep only
// 40 letters of an accented name and can split a character.
func TestProposeGoalCutsAMultiByteNameByRunes(t *testing.T) {
	long := strings.Repeat("é", 120)
	f := newFake(t, answer(t, `{"name":"`+long+`","sport":"cycling","eventDate":"","priority":"B","targetDistanceKm":0,"targetElevationM":0,"explanation":""}`))
	c := newClient(t, f.srv.URL+"/v1", "")

	p, err := c.ProposeGoal(t.Context(), "x", goalToday)
	if err != nil {
		t.Fatalf("ProposeGoal: %v", err)
	}
	if n := utf8.RuneCountInString(p.Name); n != 80 || !utf8.ValidString(p.Name) {
		t.Errorf("name = %d runes (valid UTF-8: %v), want 80 valid runes", n, utf8.ValidString(p.Name))
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

// "In June" names no day, so the model gives the month and the server places
// it: the 15th of the next June after today, or the 5th / 25th for early / late.
func TestProposeGoalPlacesAMonthWithoutADay(t *testing.T) {
	cases := []struct {
		reply string
		want  string
	}{
		{`"eventDate":"","eventMonth":6,"eventPartOfMonth":""`, "2026-06-15"},
		{`"eventDate":"","eventMonth":6,"eventPartOfMonth":"early"`, "2026-06-05"},
		{`"eventDate":"","eventMonth":6,"eventPartOfMonth":"late"`, "2026-06-25"},
		// A month already behind today means next year's.
		{`"eventDate":"","eventMonth":2,"eventPartOfMonth":"mid"`, "2027-02-15"},
		// Mid March is before today (18 March): next year's.
		{`"eventDate":"","eventMonth":3,"eventPartOfMonth":"mid"`, "2027-03-15"},
		// A day the model did give wins over the month.
		{`"eventDate":"2026-06-07","eventMonth":6,"eventPartOfMonth":"mid"`, "2026-06-07"},
		// A month out of range is nothing.
		{`"eventDate":"","eventMonth":13,"eventPartOfMonth":""`, ""},
		{`"eventDate":"","eventMonth":0,"eventPartOfMonth":""`, ""},
	}
	for _, c := range cases {
		f := newFake(t, answer(t, `{"name":"Half marathon","sport":"running",`+c.reply+`,"priority":"B","targetDistanceKm":21.1,"targetElevationM":0,"explanation":"x"}`))
		cl := newClient(t, f.srv.URL+"/v1", "")
		p, err := cl.ProposeGoal(t.Context(), "I want to run a half marathon in june", goalToday)
		if err != nil {
			t.Fatalf("ProposeGoal: %v", err)
		}
		if p.EventDate != c.want {
			t.Errorf("reply %s: eventDate = %q, want %q", c.reply, p.EventDate, c.want)
		}
	}
}

// The prompt has to let the model place a month and fill in a standard race
// distance, or "half marathon in June" comes back with neither.
func TestGoalSystemAllowsMonthsAndStandardDistances(t *testing.T) {
	for _, want := range []string{"eventMonth", "eventInWeeks", "eventInMonths", "half marathon", "21.1", "marathon", "42.2"} {
		if !strings.Contains(goalSystem, want) {
			t.Errorf("goalSystem does not mention %q", want)
		}
	}
}

func TestProposeGoalPlacesASpanFromNow(t *testing.T) {
	cases := []struct {
		reply string
		want  string
	}{
		{`"eventDate":"","eventMonth":0,"eventInWeeks":0,"eventInMonths":9`, "2026-12-18"},
		{`"eventDate":"","eventMonth":0,"eventInWeeks":12,"eventInMonths":0`, "2026-06-10"},
		{`"eventDate":"","eventMonth":0,"eventInWeeks":0,"eventInMonths":0`, ""},
		// Out of bounds is nothing.
		{`"eventDate":"","eventMonth":0,"eventInWeeks":0,"eventInMonths":30`, ""},
		{`"eventDate":"","eventMonth":0,"eventInWeeks":200,"eventInMonths":0`, ""},
		// Months win when both are set.
		{`"eventDate":"","eventMonth":0,"eventInWeeks":12,"eventInMonths":9`, "2026-12-18"},
		// An explicit day wins over a span; a named month wins over a span.
		{`"eventDate":"2026-06-07","eventMonth":0,"eventInWeeks":0,"eventInMonths":9`, "2026-06-07"},
		{`"eventDate":"","eventMonth":6,"eventInWeeks":0,"eventInMonths":9`, "2026-06-15"},
	}
	for _, c := range cases {
		f := newFake(t, answer(t, `{"name":"Half marathon","sport":"running",`+c.reply+`,"eventPartOfMonth":"","priority":"B","targetDistanceKm":21.1,"targetElevationM":0,"explanation":"x"}`))
		cl := newClient(t, f.srv.URL+"/v1", "")
		p, err := cl.ProposeGoal(t.Context(), "I want to run a half marathon in 9 months", goalToday)
		if err != nil {
			t.Fatalf("ProposeGoal: %v", err)
		}
		if p.EventDate != c.want {
			t.Errorf("reply %s: eventDate = %q, want %q", c.reply, p.EventDate, c.want)
		}
	}
}
