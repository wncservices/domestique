package scheduler

import (
	"math"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// hoursProfile is a rider who stated hours per weekday.
func hoursProfile(hours map[string]float64) workout.RiderProfile {
	p := workout.RiderProfile{HoursByDay: hours}
	p.NormaliseAvailability()
	return p
}

// weekdayOf is the lowercase weekday key of a request's date.
func weekdayOf(t *testing.T, date string) string {
	t.Helper()
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	return []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}[d.Weekday()]
}

func longDayOf(t *testing.T, out []workout.CreateWorkoutRequest) string {
	t.Helper()
	var day string
	for _, r := range out {
		if IsLongRideName(r.Name) {
			if day != "" {
				t.Fatalf("two long rides: %s and %s", day, r.Date)
			}
			day = weekdayOf(t, r.Date)
		}
	}
	return day
}

func TestLongRideGoesOnTheDayWithMostHours(t *testing.T) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 6}
	out := build(t, week, hoursProfile(map[string]float64{"tue": 1, "thu": 1, "sat": 3, "sun": 2}))
	if got := longDayOf(t, out); got != "sat" {
		t.Errorf("long ride on %q, want sat", got)
	}
}

func TestTiesKeepTheLongRideOnTheLaterDay(t *testing.T) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 6}
	out := build(t, week, hoursProfile(map[string]float64{"tue": 1, "sat": 3, "sun": 3}))
	if got := longDayOf(t, out); got != "sun" {
		t.Errorf("long ride on %q, want sun", got)
	}
}

func TestNoSessionExceedsItsDay(t *testing.T) {
	profile := hoursProfile(map[string]float64{"mon": 1, "tue": 1, "wed": 1, "thu": 1, "fri": 1, "sat": 3, "sun": 3})
	for _, phase := range []periodization.Phase{periodization.PhaseBase, periodization.PhaseBuild, periodization.PhasePeak, periodization.PhaseTaper} {
		week := periodization.Week{StartDate: "2026-01-05", Phase: phase, TargetHours: 11}
		out := build(t, week, profile)
		if len(out) == 0 {
			t.Fatalf("%s: no sessions", phase)
		}
		for _, r := range out {
			day := weekdayOf(t, r.Date)
			if got, max := workout.PlannedSeconds(r.Steps), profile.HoursOn(day)*3600+1; got > max {
				t.Errorf("%s: %s on %s lasts %v s, more than the day's %v s", phase, r.Name, day, got, max)
			}
		}
	}
}

func floatsNear(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}

func TestWaterFillSendsTheExcessToDaysWithRoom(t *testing.T) {
	if got, want := waterFill(6, []float64{1, 1, 1.8}, []float64{1, 3, 3}), []float64{1, 2, 3}; !floatsNear(got, want) {
		t.Errorf("waterFill = %v, want %v", got, want)
	}
	if got, want := waterFill(4, []float64{1, 1.8}, []float64{0, 0}), []float64{4 / 2.8, 7.2 / 2.8}; !floatsNear(got, want) {
		t.Errorf("uncapped waterFill = %v, want %v", got, want)
	}
}

func TestWaterFillDropsWhatNoDayHolds(t *testing.T) {
	got := waterFill(10, []float64{1, 1}, []float64{1, 2})
	sum := 0.0
	for _, h := range got {
		if h < 0 {
			t.Errorf("negative hours in %v", got)
		}
		sum += h
	}
	if math.Abs(sum-3) > 1e-9 {
		t.Errorf("waterFill = %v (sum %v), want everything the caps hold: 3", got, sum)
	}

	// A one-hour crew ride on Saturday leaves the week 9 of its 10 hours, and
	// Sunday holds one of them.
	profile := hoursProfile(map[string]float64{"sat": 1, "sun": 1})
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBase, TargetHours: 10}
	out, err := WeekWorkouts(week, profile, nil, "wilant", "goal", model.SportCycling, WithFixed([]Fixed{{Date: "2026-01-10", Seconds: 3600}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out {
		day := weekdayOf(t, r.Date)
		if got, max := workout.PlannedSeconds(r.Steps), profile.HoursOn(day)*3600+1; got > max {
			t.Errorf("%s on %s lasts %v s, more than the day's %v s", r.Name, day, got, max)
		}
	}
}

func TestHardDaysKeepClearOfAMidweekLongDay(t *testing.T) {
	profile := hoursProfile(map[string]float64{"mon": 1, "tue": 1, "wed": 4, "thu": 1, "fri": 1, "sat": 1})
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 8}
	out := build(t, week, profile)
	if got := longDayOf(t, out); got != "wed" {
		t.Errorf("long ride on %q, want wed", got)
	}
	for _, r := range out {
		day := weekdayOf(t, r.Date)
		if r.Zone != workout.ZoneEndurance && (day == "tue" || day == "wed" || day == "thu") {
			t.Errorf("structured %s on %s, beside or on the long day", r.Name, day)
		}
	}
	// The placement itself, apart from whether a rung fits an hour.
	days := []string{"mon", "tue", "wed", "thu", "fri", "sat"}
	for _, p := range placeStructuredDaysIn(days, 2, len(days), 2) {
		if p >= 1 && p <= 3 {
			t.Errorf("placed a structured slot on %s", days[p])
		}
	}
}

func TestAProfileWithoutHoursIsNeverCapped(t *testing.T) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 10}
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat", "sun"}}
	out := build(t, week, profile)
	if got, want := hoursOf(out), week.TargetHours; math.Abs(got-want) > want*0.10 {
		t.Errorf("total %v h, want %v", got, want)
	}
	if got := longDayOf(t, out); got != "sun" {
		t.Errorf("long ride on %q, want sun", got)
	}
}

// Dates and kinds, not lengths: the long ride's share here is a few seconds
// over 4 h, so the cap trims it and Saturday takes the difference.
func TestAnUncappedOldProfilePlacesAsBefore(t *testing.T) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 8}
	days := []string{"tue", "thu", "sat", "sun"}
	plain := build(t, week, workout.RiderProfile{AvailableDays: days})
	uniform := build(t, week, workout.RiderProfile{AvailableDays: days, HoursPerAvailableDay: 4})
	if len(plain) != len(uniform) {
		t.Fatalf("%d sessions with 4 h a day, %d with no hours", len(uniform), len(plain))
	}
	for i := range plain {
		a, b := plain[i], uniform[i]
		if a.Date != b.Date || a.Name != b.Name || a.Zone != b.Zone {
			t.Errorf("session %d: %s %s %s, want %s %s %s", i, b.Date, b.Name, b.Zone, a.Date, a.Name, a.Zone)
		}
	}
}

func TestMiniTaperDayIsCappedByItsHours(t *testing.T) {
	week, _, levels := miniTaperWeek()
	week.MiniTaperDayHours = 2
	profile := hoursProfile(map[string]float64{"mon": 2, "tue": 2, "wed": 1, "thu": 2, "fri": 2, "sat": 2, "sun": 2})
	profile.FTPWatts = 250
	out, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range out {
		if r.Date == "2026-01-07" {
			found = true
			if got := workout.PlannedSeconds(r.Steps); got > 3600+1 {
				t.Errorf("Wednesday's mini-taper ride lasts %v s, more than its hour", got)
			}
		}
	}
	if !found {
		t.Error("no session on Wednesday")
	}
}

// Hours mean hours: a week at its hours cap whose structured sessions leave
// room on their own days places less than its target rather than put a ride
// past a day's hours.
func TestACappedWeekMayPlaceLessThanItsTarget(t *testing.T) {
	profile := workout.RiderProfile{AvailableDays: []string{"tue", "thu", "sat", "sun"}, HoursPerAvailableDay: 3, FTPWatts: 250}
	week := periodization.Week{StartDate: "2026-11-30", Phase: periodization.PhaseBuild, TargetHours: 12}
	out := build(t, week, profile)
	if got := hoursOf(out); got >= week.TargetHours {
		t.Errorf("placed %v h, want less than the %v h target", got, week.TargetHours)
	}
	for _, r := range out {
		if got := workout.PlannedSeconds(r.Steps); got > 3*3600+1 {
			t.Errorf("%s on %s lasts %v s, more than the day's 3 h", r.Name, r.Date, got)
		}
	}
}
