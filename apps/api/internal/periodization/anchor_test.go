package periodization

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// anchoredGoal is a 12-week plan: created on Monday 2026-01-05, event on
// Sunday 2026-03-29. allocateWeeks(12) gives base 5, build 4, peak 2, taper 1,
// so week 4 (Monday 2026-01-26) is Base's recovery week.
var anchoredGoal = workout.Goal{ID: "anchored", EventDate: "2026-03-29", CreatedAt: "2026-01-05T09:30:00Z"}

var anchoredProfile = workout.RiderProfile{HoursPerAvailableDay: 2, AvailableDays: []string{"tue", "thu", "sat", "sun"}}

func inZones(t *testing.T, f func(t *testing.T, loc *time.Location)) {
	t.Helper()
	for _, name := range []string{"UTC", "Europe/Brussels", "America/Los_Angeles"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) { f(t, loc) })
	}
}

// The same calendar week must come out identical whichever day the plan is
// built on: the plan is rebuilt on every request, so anything else means
// the current week is always "week 1 of whatever phase is left".
func TestSameCalendarWeekIsStableAcrossRebuilds(t *testing.T) {
	inZones(t, func(t *testing.T, loc *time.Location) {
		byDate := map[string]Week{}
		for _, day := range []time.Time{
			time.Date(2026, 1, 5, 8, 0, 0, 0, loc),
			time.Date(2026, 1, 8, 23, 0, 0, 0, loc), // mid-week, same week as the first
			time.Date(2026, 1, 12, 8, 0, 0, 0, loc),
			time.Date(2026, 1, 19, 8, 0, 0, 0, loc),
			time.Date(2026, 1, 26, 8, 0, 0, 0, loc),
			time.Date(2026, 2, 2, 8, 0, 0, 0, loc),
		} {
			plan, err := BuildPlan(anchoredGoal, anchoredProfile, day)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Weeks[0].StartDate != MondayOf(day).Format("2006-01-02") {
				t.Fatalf("built on %s: Weeks[0] = %s, want this week", day.Format("2006-01-02"), plan.Weeks[0].StartDate)
			}
			if plan.TotalWeeks != 12 {
				t.Errorf("built on %s: TotalWeeks = %d, want 12", day.Format("2006-01-02"), plan.TotalWeeks)
			}
			for _, w := range plan.Weeks {
				if prev, ok := byDate[w.StartDate]; ok && prev != w {
					t.Errorf("week %s differs between rebuilds:\n first: %+v\n later: %+v", w.StartDate, prev, w)
				}
				byDate[w.StartDate] = w
			}
		}
		if len(byDate) != 12 {
			t.Errorf("saw %d distinct calendar weeks, want 12", len(byDate))
		}
	})
}

func TestRecoveryWeekArrivesAsWeeksZeroOnItsOwnWeek(t *testing.T) {
	inZones(t, func(t *testing.T, loc *time.Location) {
		plan, err := BuildPlan(anchoredGoal, anchoredProfile, time.Date(2026, 1, 28, 12, 0, 0, 0, loc))
		if err != nil {
			t.Fatal(err)
		}
		w := plan.Weeks[0]
		if w.StartDate != "2026-01-26" || w.Number != 4 || w.Phase != PhaseBase || !w.Recovery {
			t.Errorf("Weeks[0] = %+v, want week 4 (2026-01-26), base, recovery", w)
		}
		if want := 8 * baseCeiling * recoveryLoadFraction; w.TargetHours != want {
			t.Errorf("recovery TargetHours = %v, want %v", w.TargetHours, want)
		}
	})
}

func TestBaseTargetHoursRampAcrossRebuilds(t *testing.T) {
	inZones(t, func(t *testing.T, loc *time.Location) {
		var hours []float64
		for _, d := range []string{"2026-01-05", "2026-01-12", "2026-01-19"} {
			day, err := time.ParseInLocation("2006-01-02", d, loc)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := BuildPlan(anchoredGoal, anchoredProfile, day)
			if err != nil {
				t.Fatal(err)
			}
			hours = append(hours, plan.Weeks[0].TargetHours)
		}
		if !(hours[0] < hours[1] && hours[1] < hours[2]) {
			t.Errorf("this week's target across three consecutive Mondays = %v, want strictly rising", hours)
		}
	})
}

func TestNumberIsTheAnchoredWeekNumber(t *testing.T) {
	plan, err := BuildPlan(anchoredGoal, anchoredProfile, mustDate(t, "2026-02-11"))
	if err != nil {
		t.Fatal(err)
	}
	// 2026-02-09 is 5 weeks after the 2026-01-05 anchor: week 6 of 12.
	if w := plan.Weeks[0]; w.Number != 6 || w.StartDate != "2026-02-09" {
		t.Errorf("Weeks[0] = %+v, want Number 6 on 2026-02-09", w)
	}
	if last := plan.Weeks[len(plan.Weeks)-1]; last.Number != 12 || last.Phase != PhaseTaper {
		t.Errorf("last week = %+v, want Number 12, taper", last)
	}
	if len(plan.Weeks) != 7 {
		t.Errorf("len(Weeks) = %d, want the 7 weeks from this one on", len(plan.Weeks))
	}
}

func TestGoalCreatedInTheFutureIsAnchoredToToday(t *testing.T) {
	goal := workout.Goal{ID: "g", EventDate: "2026-03-29", CreatedAt: "2026-02-20T00:00:00Z"}
	plan, err := BuildPlan(goal, anchoredProfile, mustDate(t, "2026-01-07"))
	if err != nil {
		t.Fatal(err)
	}
	if w := plan.Weeks[0]; w.Number != 1 || w.StartDate != "2026-01-05" {
		t.Errorf("Weeks[0] = %+v, want week 1 of today's own Monday", w)
	}
	if plan.TotalWeeks != len(plan.Weeks) {
		t.Errorf("TotalWeeks = %d, len = %d, want equal when the anchor is this week", plan.TotalWeeks, len(plan.Weeks))
	}
}

func TestEditedEventDateReallocatesWithoutBreaking(t *testing.T) {
	today := mustDate(t, "2026-02-09")
	for _, event := range []string{"2026-02-15", "2026-02-16", "2026-03-29", "2026-09-27"} {
		g := anchoredGoal
		g.EventDate = event
		plan, err := BuildPlan(g, anchoredProfile, today)
		if err != nil {
			t.Fatalf("event %s: %v", event, err)
		}
		if len(plan.Weeks) == 0 || plan.TotalWeeks < len(plan.Weeks) {
			t.Fatalf("event %s: %d weeks, TotalWeeks %d", event, len(plan.Weeks), plan.TotalWeeks)
		}
		if plan.Weeks[0].StartDate != "2026-02-09" {
			t.Errorf("event %s: Weeks[0] = %s", event, plan.Weeks[0].StartDate)
		}
	}
}

// The Plan page header reads "week N of TotalWeeks"; a rolling plan has no
// event, so its window is the total.
func TestRollingPlanReportsItsWindowAsTotalWeeks(t *testing.T) {
	plan := BuildRollingPlan(workout.Goal{ID: "g"}, anchoredProfile, mustDate(t, "2026-02-11"))
	if plan.TotalWeeks != len(plan.Weeks) {
		t.Errorf("TotalWeeks = %d, want %d", plan.TotalWeeks, len(plan.Weeks))
	}
}

func TestYearOldGoalWithADistantEventStillAnchors(t *testing.T) {
	goal := workout.Goal{ID: "g", EventDate: "2027-05-30", CreatedAt: "2026-01-05T00:00:00Z"}
	plan, err := BuildPlan(goal, anchoredProfile, mustDate(t, "2026-12-30"))
	if err != nil {
		t.Fatal(err)
	}
	// 2026-12-28 is 51 weeks after the anchor; 2027-05-30 falls in week 73.
	if w := plan.Weeks[0]; w.Number != 52 || w.StartDate != "2026-12-28" {
		t.Errorf("Weeks[0] = %+v, want Number 52", w)
	}
	if plan.TotalWeeks != 73 {
		t.Errorf("TotalWeeks = %d, want 73", plan.TotalWeeks)
	}
	if last := plan.Weeks[len(plan.Weeks)-1]; last.Number != 73 || last.Phase != PhaseTaper {
		t.Errorf("last = %+v", last)
	}
}
