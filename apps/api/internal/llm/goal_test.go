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
	if got := props["sport"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []string{"cycling", "running", "other"}) {
		t.Errorf("goal sport enum = %v, want exactly cycling, running, other", got)
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

// proposeAt asks for a goal with the given reply fields, as of today.
func proposeAt(t *testing.T, today time.Time, fields string) (GoalProposal, error) {
	t.Helper()
	f := newFake(t, answer(t, `{"name":"Ride","sport":"cycling","priority":"B","explanation":"x",`+fields+`}`))
	return newClient(t, f.srv.URL+"/v1", "").ProposeGoal(t.Context(), "note", today)
}

func TestProposeGoalPlacesDaysWeekdaysAndYears(t *testing.T) {
	sat := time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC)   // a Saturday
	late := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC) // near a year end
	jan31 := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC) // 31st is today
	cases := []struct {
		name   string
		today  time.Time
		fields string
		want   string
	}{
		{"this weekend, Saturday from a Wednesday", goalToday, `"eventWeekday":"sat"`, "2026-03-21"},
		{"this weekend, Sunday from a Wednesday", goalToday, `"eventWeekday":"sun"`, "2026-03-22"},
		{"Sunday from a Saturday", sat, `"eventWeekday":"sun"`, "2026-03-22"},
		{"weekend across a year end", time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC), `"eventWeekday":"sat"`, "2027-01-02"},
		{"the 20th, still ahead this month", goalToday, `"eventDayOfMonth":20`, "2026-03-20"},
		{"the 14th, already past: next month", goalToday, `"eventDayOfMonth":14`, "2026-04-14"},
		{"today's own day number: next month", goalToday, `"eventDayOfMonth":18`, "2026-04-18"},
		{"the 14th across a year end", late, `"eventDayOfMonth":14`, "2027-01-14"},
		{"the 31st skips a short month", time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), `"eventDayOfMonth":31`, "2026-05-31"},
		{"the 31st in January", jan31, `"eventDayOfMonth":31`, "2026-03-31"},
		{"14 June, ahead", goalToday, `"eventDayOfMonth":14,"eventMonth":6`, "2026-06-14"},
		{"14 February, behind: next year", goalToday, `"eventDayOfMonth":14,"eventMonth":2`, "2027-02-14"},
		{"14 June next year", goalToday, `"eventDayOfMonth":14,"eventMonth":6,"eventYearOffset":1`, "2027-06-14"},
		{"30 February never exists: the month stands", goalToday, `"eventDayOfMonth":30,"eventMonth":2`, "2027-02-15"},
		{"29 February waits for a leap year", goalToday, `"eventDayOfMonth":29,"eventMonth":2`, "2028-02-29"},
		{"a day out of range is nothing", goalToday, `"eventDayOfMonth":32`, ""},
		{"end of next year", goalToday, `"eventMonth":12,"eventPartOfMonth":"late","eventYearOffset":1`, "2027-12-25"},
		{"next spring", goalToday, `"eventMonth":4,"eventPartOfMonth":"mid","eventYearOffset":1`, "2027-04-15"},
		{"next year from December", late, `"eventMonth":3,"eventYearOffset":1`, "2027-03-15"},
		{"a month already behind, no offset, near a year end", late, `"eventMonth":3`, "2027-03-15"},
		{"next year alone means nothing", goalToday, `"eventYearOffset":1`, ""},
		{"an explicit date beats a day of month", goalToday, `"eventDate":"2026-07-01","eventDayOfMonth":14,"eventWeekday":"sat"`, "2026-07-01"},
		// Fields combine: the weekday snaps a base day, it never replaces it.
		// 15 June 2026 is a Monday: Saturday the 13th is nearer than the 20th.
		{"a Saturday in June", goalToday, `"eventWeekday":"sat","eventMonth":6`, "2026-06-13"},
		{"a Sunday in June", goalToday, `"eventWeekday":"sun","eventMonth":6`, "2026-06-14"},
		{"a Saturday in late June", goalToday, `"eventWeekday":"sat","eventMonth":6,"eventPartOfMonth":"late"`, "2026-06-27"},
		{"a Saturday in early June", goalToday, `"eventWeekday":"sat","eventMonth":6,"eventPartOfMonth":"early"`, "2026-06-06"},
		// 15 March is behind 18 March, so the same month next year; 15 March 2027 is a Monday.
		{"a Saturday in a month already behind", goalToday, `"eventWeekday":"sat","eventMonth":3`, "2027-03-13"},
		{"a Saturday in June next year", goalToday, `"eventWeekday":"sat","eventMonth":6,"eventYearOffset":1`, "2027-06-12"},
		// Today is that Saturday, so the snapped day is not after today: next year's.
		{"a Saturday in a month, snapped day after today", time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC), `"eventWeekday":"sat","eventMonth":6`, "2027-06-12"},
		{"a Saturday three weeks out", goalToday, `"eventWeekday":"sat","eventInWeeks":3`, "2026-04-11"},
		{"a Sunday nine months out", goalToday, `"eventWeekday":"sun","eventInMonths":9`, "2026-12-20"},
		{"a Saturday span that already lands on a Saturday stays", sat, `"eventWeekday":"sat","eventInWeeks":1`, "2026-03-28"},
		{"the 14th, nine months out", goalToday, `"eventDayOfMonth":14,"eventInMonths":9`, "2027-01-14"},
		{"the 20th, nine months out lands on it", goalToday, `"eventDayOfMonth":18,"eventInMonths":9`, "2026-12-18"},
		{"the 31st, three months out skips a short month", goalToday, `"eventDayOfMonth":31,"eventInMonths":3`, "2026-07-31"},
		{"late March 2028", goalToday, `"eventMonth":3,"eventPartOfMonth":"late","eventYear":2028`, "2028-03-25"},
		{"a year wins over an offset", goalToday, `"eventMonth":3,"eventYear":2028,"eventYearOffset":1`, "2028-03-15"},
		{"a past year is ignored", goalToday, `"eventMonth":6,"eventYear":2024`, "2026-06-15"},
		{"this year pins this year", goalToday, `"eventMonth":6,"eventYear":2026`, "2026-06-15"},
		{"a pinned month already behind is nothing", goalToday, `"eventMonth":3,"eventYear":2026`, ""},
		{"14 June 2028", goalToday, `"eventDayOfMonth":14,"eventMonth":6,"eventYear":2028`, "2028-06-14"},
		// A bare Saturday asked on a Saturday is that weekend's Sunday.
		{"this weekend on a Saturday", sat, `"eventWeekday":"sat"`, "2026-03-22"},
		{"a Sunday asked on a Sunday is next Sunday", time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC), `"eventWeekday":"sun"`, "2026-03-29"},
		{"a named day in a pinned year that is gone is nothing", goalToday, `"eventDayOfMonth":10,"eventMonth":3,"eventYear":2026`, ""},
		{"this weekend on a Sunday", time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC), `"eventWeekday":"sat"`, "2026-03-28"},
		{"a month beats a span", goalToday, `"eventMonth":6,"eventInMonths":9`, "2026-06-15"},
		{"a span still works", goalToday, `"eventInMonths":9`, "2026-12-18"},
		{"a past explicit date falls through to the day of month", goalToday, `"eventDate":"2024-01-01","eventDayOfMonth":20`, "2026-03-20"},
	}
	for _, c := range cases {
		p, err := proposeAt(t, c.today, c.fields)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p.EventDate != c.want {
			t.Errorf("%s: eventDate = %q, want %q", c.name, p.EventDate, c.want)
		}
	}
}

func TestProposeGoalConvertsUnitsBeforeTheSanityBounds(t *testing.T) {
	cases := []struct {
		name         string
		fields       string
		wantM, wantE float64
	}{
		{"62 miles", `"targetDistanceKm":62,"targetDistanceUnit":"mi"`, 99800, 0},
		{"a hundred miles", `"targetDistanceKm":100,"targetDistanceUnit":"mi"`, 160900, 0},
		{"km stays", `"targetDistanceKm":21.1,"targetDistanceUnit":"km"`, 21100, 0},
		{"no unit is km", `"targetDistanceKm":180`, 180000, 0},
		{"2500 feet", `"targetElevationM":2500,"targetElevationUnit":"ft"`, 0, 762},
		{"metres stay", `"targetElevationM":2400,"targetElevationUnit":"m"`, 0, 2400},
		// 2000 miles is 3219 km, past the bound, though the number alone is not.
		{"too many miles", `"targetDistanceKm":2000,"targetDistanceUnit":"mi"`, 0, 0},
		// 190000 ft is 57912 m, inside; 200000 ft is 60960 m, outside.
		{"many feet, inside", `"targetElevationM":190000,"targetElevationUnit":"ft"`, 0, 57912},
		{"many feet, outside", `"targetElevationM":200000,"targetElevationUnit":"ft"`, 0, 0},
	}
	for _, c := range cases {
		p, err := proposeAt(t, goalToday, c.fields)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p.TargetDistanceM != c.wantM || p.TargetElevationM != c.wantE {
			t.Errorf("%s: distance/elevation = %v/%v, want %v/%v", c.name, p.TargetDistanceM, p.TargetElevationM, c.wantM, c.wantE)
		}
	}
}

// A triathlon is not a cycling goal. It is an answer, not a model failure: no
// retry, and the caller can tell it from ErrInvalid.
func TestProposeGoalRefusesAnUnsupportedSport(t *testing.T) {
	f := newFake(t, answer(t, `{"name":"Sprint triathlon","sport":"other","priority":"B","explanation":"x"}`))
	_, err := newClient(t, f.srv.URL+"/v1", "").ProposeGoal(t.Context(), "a sprint triathlon", goalToday)
	if !errors.Is(err, ErrUnsupportedSport) {
		t.Fatalf("err = %v, want ErrUnsupportedSport", err)
	}
	if errors.Is(err, ErrInvalid) {
		t.Errorf("an unsupported sport must not look like an invalid answer")
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1: a refusal is not retried", n)
	}
}

func TestGoalSystemMentionsTheNewFields(t *testing.T) {
	for _, want := range []string{"eventDayOfMonth", "eventWeekday", "eventYearOffset", "targetDistanceUnit", "targetElevationUnit", `"other"`} {
		if !strings.Contains(goalSystem, want) {
			t.Errorf("goalSystem does not mention %q", want)
		}
	}
}
