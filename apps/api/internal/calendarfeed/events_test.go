package calendarfeed

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/ics"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var brussels = func() *time.Location {
	l, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		panic(err)
	}
	return l
}()

const appURL = "https://domestique.example.com"

func wk(id, date string) workout.Workout {
	return workout.Workout{
		ID: id, Rider: "wilant", Sport: model.SportCycling, Name: "Threshold 3 x 10", Date: date,
		Zone: workout.ZoneThreshold, UpdatedAt: "2026-09-28T12:30:00Z",
		Steps: []workout.WorkoutStep{{
			Repeat: 3, Steps: []workout.WorkoutStep{
				{Intensity: workout.IntensityInterval, Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetPower, TargetLow: 250, TargetHigh: 260},
				{Intensity: workout.IntensityRecovery, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
			},
		}},
	}
}

func dates(evs []ics.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Start.In(brussels).Format("2006-01-02"))
	}
	return out
}

func TestEventsWindowStartsFourteenDaysBackInclusive(t *testing.T) {
	// 00:30 Brussels on 10 October is still 9 October in UTC: the cut-off is
	// the deployment's date, not the process's or the instant's.
	now := time.Date(2026, 10, 9, 22, 30, 0, 0, time.UTC)
	ws := []workout.Workout{
		wk("a", "2026-09-25"), // 15 days back: out
		wk("b", "2026-09-26"), // exactly 14: in
		wk("c", "2026-10-10"),
		wk("d", "2027-02-01"), // no upper bound
	}
	got := dates(Events(ws, now, brussels, nil, appURL))
	want := []string{"2026-09-26", "2026-10-10", "2027-02-01"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("dates = %v, want %v", got, want)
	}
}

func TestEventsWindowAcrossADSTChange(t *testing.T) {
	// The clocks go back on 2026-10-25; 14 days before 2026-11-08 is 10-25.
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, brussels)
	got := dates(Events([]workout.Workout{wk("a", "2026-10-24"), wk("b", "2026-10-25")}, now, brussels, nil, appURL))
	if fmt.Sprint(got) != "[2026-10-25]" {
		t.Fatalf("dates = %v, want only 2026-10-25", got)
	}
}

func TestEventsSkipsUnscheduledAndBadDates(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, brussels)
	got := Events([]workout.Workout{wk("a", ""), wk("b", "not-a-date"), wk("c", "2026-10-11")}, now, brussels, nil, appURL)
	if len(got) != 1 || got[0].UID != "workout-c@domestique" {
		t.Fatalf("events = %+v, want only workout c", got)
	}
}

func TestEventsAreCappedAtFiveHundredNearestFirst(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, brussels)
	var ws []workout.Workout
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 520; i++ {
		ws = append(ws, wk(fmt.Sprintf("w%03d", i), start.AddDate(0, 0, i).Format("2006-01-02")))
	}
	got := Events(ws, now, brussels, nil, appURL)
	if len(got) != 500 {
		t.Fatalf("len = %d, want 500", len(got))
	}
	if got[0].UID != "workout-w000@domestique" || got[499].UID != "workout-w499@domestique" {
		t.Fatalf("the cap must keep the nearest 500: first %s last %s", got[0].UID, got[499].UID)
	}
}

func TestEventUIDIsStableWhenAWorkoutMoves(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, brussels)
	before := Events([]workout.Workout{wk("42", "2026-10-12")}, now, brussels, nil, appURL)[0]
	after := Events([]workout.Workout{wk("42", "2026-10-15")}, now, brussels, nil, appURL)[0]
	if before.UID != "workout-42@domestique" || after.UID != before.UID {
		t.Fatalf("UIDs %q and %q must be the same constant-host id", before.UID, after.UID)
	}
	if before.Start.Equal(after.Start) {
		t.Fatal("the moved workout kept its old date")
	}
}

func TestEventTimestampsAreTheWorkoutsUpdatedAt(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, brussels)
	e := Events([]workout.Workout{wk("1", "2026-10-12")}, now, brussels, nil, appURL)[0]
	want := time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)
	if !e.Stamp.Equal(want) || !e.Modified.Equal(want) {
		t.Fatalf("Stamp %v Modified %v, want %v", e.Stamp, e.Modified, want)
	}
	if len(e.Categories) != 1 || e.Categories[0] != "TRAINING" {
		t.Fatalf("categories = %v", e.Categories)
	}
}

func TestEventsAreAllDayWithoutAStartHour(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, brussels)
	for name, hour := range map[string]func(string) (int, bool){
		"nil seam":  nil,
		"no window": func(string) (int, bool) { return 0, false },
	} {
		e := Events([]workout.Workout{wk("1", "2026-10-12")}, now, brussels, hour, appURL)[0]
		if !e.AllDay {
			t.Errorf("%s: want an all-day event", name)
		}
		if got := e.Start.Format("2006-01-02"); got != "2026-10-12" {
			t.Errorf("%s: start date %s", name, got)
		}
		if got := e.End.Format("2006-01-02"); got != "2026-10-13" {
			t.Errorf("%s: exclusive end %s", name, got)
		}
	}
}

func TestEventsStartAtTheRidersWindowHourInTheDeploymentZone(t *testing.T) {
	now := time.Date(2026, 9, 20, 9, 0, 0, 0, brussels)
	hour := func(rider string) (int, bool) {
		if rider != "wilant" {
			t.Errorf("startHour asked about %q", rider)
		}
		return 7, true
	}
	summer := wk("1", "2026-10-01") // CEST
	winter := wk("2", "2026-11-03") // CET
	winter.Steps = nil
	winter.Steps = []workout.WorkoutStep{{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 5400}}
	got := Events([]workout.Workout{summer, winter}, now, brussels, hour, appURL)

	if got[0].AllDay || got[1].AllDay {
		t.Fatal("a rider with a window gets timed events")
	}
	if want := time.Date(2026, 10, 1, 5, 0, 0, 0, time.UTC); !got[0].Start.Equal(want) {
		t.Errorf("summer start %v, want 07:00 Brussels = %v", got[0].Start, want)
	}
	if want := time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC); !got[1].Start.Equal(want) {
		t.Errorf("winter start %v, want 07:00 Brussels = %v", got[1].Start, want)
	}
	// The length is PlannedSeconds: 3 x (10 + 5) min, and 90 min.
	if d := got[0].End.Sub(got[0].Start); d != 45*time.Minute {
		t.Errorf("summer length %v, want 45m", d)
	}
	if d := got[1].End.Sub(got[1].Start); d != 90*time.Minute {
		t.Errorf("winter length %v, want 90m", d)
	}
}

func TestTimedEventWithUnknownLengthLastsAnHour(t *testing.T) {
	now := time.Date(2026, 9, 20, 9, 0, 0, 0, brussels)
	w := wk("1", "2026-10-01")
	w.Steps = nil
	e := Events([]workout.Workout{w}, now, brussels, func(string) (int, bool) { return 6, true }, appURL)[0]
	if d := e.End.Sub(e.Start); d != time.Hour {
		t.Fatalf("length %v, want the 1h default", d)
	}
}

func TestEventDescriptionCarriesOnlyThePlan(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, brussels)
	w := wk("1", "2026-10-12")
	w.Description = "Stay seated on the climbs."
	e := Events([]workout.Workout{w}, now, brussels, nil, appURL)[0]
	for _, want := range []string{
		"Zone: Threshold", "Duration: 45 min", "Target: 3 x 10 min at 250-260 W",
		"Stay seated on the climbs.", appURL + "/training/plan",
	} {
		if !strings.Contains(e.Description, want) {
			t.Errorf("description lacks %q:\n%s", want, e.Description)
		}
	}
	if e.Summary != "Threshold 3 x 10" {
		t.Errorf("summary = %q", e.Summary)
	}
}

func TestEventCarriesNoNumberThatIsNotTheWorkouts(t *testing.T) {
	// Nothing but the workout goes in: no level, no load, no result, no place.
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, brussels)
	w := workout.Workout{
		ID: "1", Rider: "wilant", Sport: model.SportCycling, Name: "Endurance", Date: "2026-10-12",
		Zone: workout.ZoneEndurance, Level: 3.7, TestResultWatts: 287, UpdatedAt: "2026-09-28T12:30:00Z",
		Indoor: true, GoalID: "goal-99",
	}
	e := Events([]workout.Workout{w}, now, brussels, nil, "https://domestique.example.com")[0]
	body := e.Summary + "\n" + e.Description + "\n" + e.UID
	for _, banned := range []string{"3.7", "287", "goal-99", "indoor"} {
		if strings.Contains(strings.ToLower(body), banned) {
			t.Errorf("%q leaked into the event:\n%s", banned, body)
		}
	}
	if strings.ContainsAny(e.Description, "0123456789") {
		t.Errorf("a workout with no steps and no description has no digits to show:\n%s", e.Description)
	}
	if strings.Contains(strings.ToUpper(e.Description), "LOCATION") {
		t.Error("an event never carries a place")
	}
}

func TestFTPTestKeepsItsName(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, brussels)
	w := wk("1", "2026-10-12")
	w.Name, w.TestProtocol, w.Zone = "FTP Test (ramp)", "ramp", ""
	e := Events([]workout.Workout{w}, now, brussels, nil, appURL)[0]
	if e.Summary != "FTP Test (ramp)" {
		t.Fatalf("summary = %q", e.Summary)
	}
}

func TestEventsAreDeterministic(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, brussels)
	ws := []workout.Workout{wk("b", "2026-10-13"), wk("a", "2026-10-12"), wk("c", "2026-10-12")}
	one := ics.Calendar{Name: "x", Events: Events(ws, now, brussels, nil, appURL)}.Bytes()
	two := ics.Calendar{Name: "x", Events: Events(ws, now, brussels, nil, appURL)}.Bytes()
	if string(one) != string(two) {
		t.Fatal("two builds of the same feed differ")
	}
}
