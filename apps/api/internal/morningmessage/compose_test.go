package morningmessage

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func session(name string, zone workout.Zone) workout.Workout {
	return workout.Workout{
		ID: "w1", Rider: "wilant", Sport: model.SportCycling, Name: name, Date: "2026-10-03", Zone: zone,
		Description: "Rider's own words about the day.",
		Steps: []workout.WorkoutStep{{
			Repeat: 3, Steps: []workout.WorkoutStep{
				{Intensity: workout.IntensityInterval, Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetPower, TargetLow: 250, TargetHigh: 260},
				{Intensity: workout.IntensityRecovery, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
			},
		}},
	}
}

func base() Input {
	return Input{
		Today:    "2026-10-03",
		Workouts: []workout.Workout{session("Threshold 3 x 10", workout.ZoneThreshold)},
	}
}

func joined(m Message) string { return m.Headline + "\n" + strings.Join(m.Lines, "\n") }

func hasLine(m Message, want string) bool {
	for _, l := range m.Lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestRideDayHeadlineHasTheDuration(t *testing.T) {
	m := Compose(base())
	if m.Headline != "Today: Threshold 3 x 10 (45 min)" {
		t.Errorf("headline = %q", m.Headline)
	}
	// An ordinary day adds nothing the session card does not say.
	if m.Show || len(m.Lines) != 0 {
		t.Errorf("an ordinary day should be hidden: show %v lines %q", m.Show, m.Lines)
	}
}

func TestLongSessionHeadlineUsesHoursAndMinutes(t *testing.T) {
	in := base()
	w := session("Threshold 3 x 10", workout.ZoneThreshold)
	w.Steps = []workout.WorkoutStep{{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3900, Target: workout.TargetPower, TargetLow: 180, TargetHigh: 200}}
	in.Workouts = []workout.Workout{w}
	if m := Compose(in); m.Headline != "Today: Threshold 3 x 10 (1h05)" {
		t.Errorf("headline = %q", m.Headline)
	}
}

func TestRestDay(t *testing.T) {
	in := base()
	in.Workouts = nil
	if m := Compose(in); m.Headline != "Rest day" {
		t.Errorf("headline = %q", m.Headline)
	}
}

func TestAlreadyRidden(t *testing.T) {
	in := base()
	in.Ridden = true
	in.Weather = []string{"Rain likely, 80%, 9 to 12h"}
	in.FTPTest = "2026-10-06"
	m := Compose(in)
	if m.Headline != "Already ridden" {
		t.Errorf("headline = %q", m.Headline)
	}
	// After the ride, advice about whether to ride and what the sky will do
	// is noise.
	if len(m.Lines) != 0 {
		t.Errorf("an already-ridden message still says %q", m.Lines)
	}
}

func TestReadinessIsNotInTheMessage(t *testing.T) {
	m := Compose(base())
	if strings.Contains(joined(m), "Readiness") {
		t.Errorf("readiness is the session card's job: %q", joined(m))
	}
}

func TestShownOnlyWhenItAddsSomething(t *testing.T) {
	cases := map[string]func(*Input){
		"weather":       func(in *Input) { in.Weather = []string{"Rain likely, 80%, 9 to 12h"} },
		"ftp hint":      func(in *Input) { in.FTPTest = "2026-10-06" },
		"planned break": func(in *Input) { in.Workouts, in.Break = nil, true },
		"extra session": func(in *Input) {
			run := session("Easy run", workout.ZoneEndurance)
			run.ID = "w2"
			in.Workouts = append(in.Workouts, run)
		},
	}
	for name, mutate := range cases {
		in := base()
		mutate(&in)
		if m := Compose(in); !m.Show {
			t.Errorf("%s: should show: %q / %q", name, m.Headline, m.Lines)
		}
	}
	for name, mutate := range map[string]func(*Input){
		"rest day":       func(in *Input) { in.Workouts = nil },
		"already ridden": func(in *Input) { in.Ridden = true; in.Weather = []string{"Rain"} },
		"far FTP test":   func(in *Input) { in.FTPTest = "2026-10-20" },
	} {
		in := base()
		mutate(&in)
		if m := Compose(in); m.Show {
			t.Errorf("%s: should be hidden: %q / %q", name, m.Headline, m.Lines)
		}
	}
}

func TestWeatherOnlyWhenBad(t *testing.T) {
	in := base()
	in.Weather = []string{"Rain likely, 80%, 9 to 12h", "Gusts to 55 km/h"}
	m := Compose(in)
	for _, want := range []string{"Rain likely, 80%, 9 to 12h", "Gusts to 55 km/h"} {
		if !hasLine(m, want) {
			t.Errorf("lines %q lack %q", m.Lines, want)
		}
	}
	in.Weather = nil
	if m := Compose(in); len(m.Lines) != 0 {
		t.Errorf("no lines expected, got %q", m.Lines)
	}
}

// The suggester never picks today (tomorrow at the earliest), so the message
// names the day it chose when that falls in the coming week.
func TestFTPTestSuggestionNamesTheComingDay(t *testing.T) {
	in := base() // Saturday 3 October
	in.FTPTest = "2026-10-06"
	if m := Compose(in); !hasLine(m, "An FTP test would help: Tuesday looks right.") {
		t.Errorf("no suggestion: %q", m.Lines)
	}
	in.FTPTest = "2026-10-04"
	if m := Compose(in); !hasLine(m, "An FTP test would help: tomorrow looks right.") {
		t.Errorf("tomorrow is not named as tomorrow: %q", m.Lines)
	}
	for _, other := range []string{"", "2026-10-03", "2026-10-02", "2026-10-10", "not a date"} {
		in.FTPTest = other
		if m := Compose(in); strings.Contains(joined(m), "FTP test") {
			t.Errorf("a suggestion dated %q is not in the coming week: %q", other, m.Lines)
		}
	}
}

// A day inside a life event is a planned break, not a rest day. Which kind of
// event (illness is health data) is never said.
func TestABreakDayIsNamedWithoutItsKind(t *testing.T) {
	in := base()
	in.Workouts = nil
	in.Break = true
	in.FTPTest = "2026-10-04"
	m := Compose(in)
	if m.Headline != "A planned break" {
		t.Errorf("headline = %q", m.Headline)
	}
	for _, banned := range []string{"Rest day", "FTP test", "illness", "travel", "busy"} {
		if strings.Contains(strings.ToLower(joined(m)), strings.ToLower(banned)) {
			t.Errorf("a break-day message says %q: %q", banned, joined(m))
		}
	}
}

// A session the rider kept inside the event (a gym session on a trip) is still
// theirs to ride, so it is the headline as usual.
func TestABreakDayStillNamesAKeptSession(t *testing.T) {
	in := base()
	in.Break = true
	if m := Compose(in); !strings.HasPrefix(m.Headline, "Today: Threshold 3 x 10") {
		t.Errorf("headline = %q", m.Headline)
	}
}

func TestSeveralSessionsHeadlineIsTheLongest(t *testing.T) {
	in := base()
	run := session("Easy run", workout.ZoneEndurance)
	run.ID, run.Sport = "w2", model.SportRunning
	run.Steps = []workout.WorkoutStep{{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 1800, Target: workout.TargetOpen}}
	in.Workouts = append(in.Workouts, run)
	m := Compose(in)
	if !strings.Contains(m.Headline, "Threshold 3 x 10") {
		t.Errorf("headline = %q", m.Headline)
	}
	if !hasLine(m, "Also planned: Easy run (30 min)") {
		t.Errorf("the second session is missing: %q", m.Lines)
	}
}

// What the verdict rests on (sleep, HRV, resting heart rate, load) is health
// data. The composer is given the verdict and nothing else: not the reasons,
// not the signals. Real reasons are generated here and must be unable to reach
// the message.
func TestNoReadinessReasonOrNumberReachesTheMessage(t *testing.T) {
	in := base()
	in.Weather = []string{"Rain likely, 80%, 9 to 12h"}
	in.FTPTest = "2026-10-06"
	all := joined(Compose(in))
	for _, banned := range []string{"score", "HRV", "resting", "bpm", "sleep", "Readiness"} {
		if strings.Contains(strings.ToLower(all), strings.ToLower(banned)) {
			t.Errorf("%q is in the message", banned)
		}
	}
}

// The compile-time shape of the guarantee above: nothing in Input can carry a
// readiness assessment or its reasons.
func TestInputHasNoReadinessReasonsField(t *testing.T) {
	typ := reflect.TypeOf(Input{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type == reflect.TypeOf(readiness.Verdict("")) || f.Type == reflect.TypeOf(readiness.Assessment{}) || f.Type == reflect.TypeOf([]readiness.Signal{}) || strings.Contains(strings.ToLower(f.Name), "reason") {
			t.Errorf("Input.%s could carry readiness reasons into the message", f.Name)
		}
	}
}

func TestHeadlineCannotCarryALineBreak(t *testing.T) {
	in := base()
	in.Workouts[0].Name = "Hill\r\nsecond line"
	if m := Compose(in); strings.ContainsAny(m.Headline, "\r\n") {
		t.Errorf("headline has a line break: %q", m.Headline)
	}
}

func TestLinesIsNeverNil(t *testing.T) {
	if m := Compose(base()); m.Lines == nil {
		t.Error("Lines should be an empty slice so it serialises as []")
	}
}

func TestDropLegacyTable(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Nothing to drop is not an error.
	if err := DropLegacyTable(db); err != nil {
		t.Fatalf("on a database without the table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE morning_summaries (rider TEXT PRIMARY KEY, email TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO morning_summaries VALUES ('wilant', 'wilant@example.com')`); err != nil {
		t.Fatal(err)
	}
	if err := DropLegacyTable(db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'morning_summaries'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("table still there: n=%d err=%v", n, err)
	}
}
