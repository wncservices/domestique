package scheduler

import (
	"math"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A priority B race on Saturday 2026-01-10: its last three days (Wed to Fri)
// are the mini-taper.
func miniTaperWeek() (periodization.Week, workout.RiderProfile, map[string]float64) {
	week := periodization.Week{
		StartDate: "2026-01-05", Phase: periodization.PhasePeak, TargetHours: 12, PlannedCTL: 80,
		MiniTaperDays: 3, MiniTaperStart: "2026-01-07", EventDate: "2026-01-10", MiniTaperDayHours: 0.5,
	}
	profile := workout.RiderProfile{
		AvailableDays: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, FTPWatts: 250, HoursPerAvailableDay: 2,
	}
	levels := map[string]float64{"vo2max": 4, "anaerobic": 4, "threshold": 4, "sweet_spot": 4, "endurance": 4}
	return week, profile, levels
}

func TestMiniTaperDaysAreEasyAtTheTapersDailyLoad(t *testing.T) {
	week, profile, levels := miniTaperWeek()
	out, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	byDate := map[string]workout.CreateWorkoutRequest{}
	for _, r := range out {
		byDate[r.Date] = r
	}
	for _, d := range []string{"2026-01-10", "2026-01-11"} {
		if _, ok := byDate[d]; ok {
			t.Errorf("a session on %s, on or after the race", d)
		}
	}
	// 3 days at 0.5 h a day, over the three available mini-taper days.
	for _, d := range []string{"2026-01-07", "2026-01-08", "2026-01-09"} {
		r, ok := byDate[d]
		if !ok {
			t.Fatalf("no session on %s", d)
		}
		if r.Zone != workout.ZoneEndurance || r.Name == "Long ride" {
			t.Errorf("%s: %s (%s), want an easy endurance ride", d, r.Name, r.Zone)
		}
		if got := workout.PlannedSeconds(r.Steps); math.Abs(got-1800) > 1 {
			t.Errorf("%s lasts %v s, want the taper's 1800", d, got)
		}
	}
	structured := 0
	for _, d := range []string{"2026-01-05", "2026-01-06"} {
		if r, ok := byDate[d]; ok && r.Zone != workout.ZoneEndurance {
			structured++
		}
	}
	if structured == 0 {
		t.Error("Monday and Tuesday keep none of the week's structured sessions")
	}
	// Everything before the mini-taper shares what the taper days left.
	var before float64
	for _, d := range []string{"2026-01-05", "2026-01-06"} {
		before += workout.PlannedSeconds(byDate[d].Steps)
	}
	if want := (12 - 1.5) * 3600; before > want+1 {
		t.Errorf("Monday and Tuesday hold %v s, more than the week's %v s outside the taper", before, want)
	}
}

// A mini-taper week is not lifted, biased or given a third hard session.
func TestMiniTaperWeekIsNotBoostedBiasedOrGivenAThirdSession(t *testing.T) {
	week, profile, levels := miniTaperWeek()
	plain, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	got, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling,
		WithTargetTSS(5000), WithRouteDemand(&RouteDemand{Sustained: 12 * 60, Short: 5 * 60, Climbing: true}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, got) {
		t.Errorf("a boost or route demand changed a mini-taper week:\n%+v\n%+v", plain, got)
	}
	for _, r := range plain {
		if r.Zone != workout.ZoneEndurance && r.Level > levels[string(r.Zone)]+1e-9 {
			t.Errorf("%s at level %v is above the rider's %v", r.Name, r.Level, levels[string(r.Zone)])
		}
	}
	hard := 0
	for _, r := range plain {
		if r.Zone != workout.ZoneEndurance {
			hard++
		}
	}
	if hard > 2 {
		t.Errorf("%d structured sessions, want at most the phase's two", hard)
	}
}

// A rider who can only train on Saturday and Sunday, with the race on Sunday:
// Saturday is the one mini-taper day they have, and it is one day's taper load,
// not the whole window's.
func TestMiniTaperDayIsCappedAtTheTapersDailyLoad(t *testing.T) {
	week, profile, levels := miniTaperWeek()
	week.EventDate, week.MiniTaperStart = "2026-01-11", "2026-01-08"
	profile.AvailableDays = []string{"sat", "sun"}
	out, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Date != "2026-01-10" {
		t.Fatalf("sessions = %+v, want only Saturday", out)
	}
	if got := workout.PlannedSeconds(out[0].Steps); math.Abs(got-1800) > 1 {
		t.Errorf("Saturday lasts %v s, want one taper day's 1800", got)
	}
}

// The event is the session for every priority: no session on its day, for a
// plan with no mini-taper too.
func TestNoSessionOnTheEventDayWithoutAMiniTaper(t *testing.T) {
	week, profile, levels := miniTaperWeek()
	week.MiniTaperDays, week.MiniTaperStart, week.MiniTaperDayHours = 0, "", 0
	out, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 5 {
		t.Errorf("%d sessions, want Monday to Friday", len(out))
	}
	for _, r := range out {
		if r.Date >= "2026-01-10" {
			t.Errorf("a session on %s, on or after the event", r.Date)
		}
	}
}
