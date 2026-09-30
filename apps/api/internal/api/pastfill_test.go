package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Filling the week that contains today must never put a session on a day that
// has already gone by. The adapter reads a dated-but-unridden session as
// missed and "makes it up" by replacing a later easy day, so a rider who
// created a goal on a Wednesday used to get a phantom missed Tuesday and a
// reshuffled week. These run on every weekday, so none of them can pass only
// on the day the suite happens to run.

// everyWeekday is Monday 2026-10-05 through Sunday 2026-10-11, at noon UTC.
func everyWeekday() []time.Time {
	days := make([]time.Time, 7)
	for i := range days {
		days[i] = utcNoon(2026, time.October, 5+i)
	}
	return days
}

// assertNothingInThePast fails for any workout dated before today, and for any
// automatic adjustment (a move or a "missed" note) made right after filling.
func assertNothingInThePast(t *testing.T, h *autoScheduleHarness, ws []workout.Workout) {
	t.Helper()
	today := h.now.Format("2006-01-02")
	for _, wk := range ws {
		if wk.Date < today {
			t.Errorf("workout %q is dated %s, before today (%s)", wk.Name, wk.Date, today)
		}
	}
	h.srv.AdaptWorkouts(context.Background())
	after, err := h.store.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	for _, wk := range after {
		if _, moved := scheduler.MovedFrom(wk.Description); moved || strings.Contains(wk.Description, "missed") {
			t.Errorf("workout %q on %s was adjusted right after filling: %q", wk.Name, wk.Date, wk.Description)
		}
	}
}

// remainingThisWeek is how many of the rider's tue/thu/sat/sun fall on or
// after today, in today's week.
func remainingThisWeek(now time.Time) int {
	today := (int(now.Weekday()) + 6) % 7 // Monday = 0
	n := 0
	for _, d := range []time.Weekday{time.Tuesday, time.Thursday, time.Saturday, time.Sunday} {
		if (int(d)+6)%7 >= today {
			n++
		}
	}
	return n
}

func TestTheTickNeverPlansDaysAlreadyPast(t *testing.T) {
	for _, now := range everyWeekday() {
		t.Run(now.Weekday().String(), func(t *testing.T) {
			h := newOnceHarness(t)
			h.now = now
			h.srv.AutoScheduleTick(context.Background())

			thisMon, _ := weekBounds(now)
			if got, want := len(h.week(t, thisMon)), remainingThisWeek(now); got != want {
				t.Errorf("this week holds %d sessions, want %d (only days from today)", got, want)
			}
			all, err := h.store.ListWorkouts(context.Background(), "wilant")
			if err != nil {
				t.Fatal(err)
			}
			assertNothingInThePast(t, h.autoScheduleHarness, all)

			// Recorded as filled: a second tick adds nothing.
			before, _ := h.store.ListWorkouts(context.Background(), "wilant")
			h.srv.AutoScheduleTick(context.Background())
			again, _ := h.store.ListWorkouts(context.Background(), "wilant")
			if len(again) != len(before) {
				t.Errorf("a second tick changed the count from %d to %d", len(before), len(again))
			}
		})
	}
}

func TestCreatingAGoalNeverPlansDaysAlreadyPast(t *testing.T) {
	for _, now := range everyWeekday() {
		t.Run(now.Weekday().String(), func(t *testing.T) {
			h := newSeasonHarness(t)
			h.now = now
			h.autoScheduleHarness.now = now
			h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
			h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14"}`)
			h.srv.WaitForBackground()

			thisMon, _ := weekBounds(now)
			if got, want := len(h.week(t, thisMon)), remainingThisWeek(now); got != want {
				t.Errorf("this week holds %d sessions, want %d", got, want)
			}
			assertNothingInThePast(t, h.autoScheduleHarness, h.workouts(t))
		})
	}
}

func TestTheFillButtonNeverPlansDaysAlreadyPast(t *testing.T) {
	for _, now := range everyWeekday() {
		t.Run(now.Weekday().String(), func(t *testing.T) {
			h := newAutoScheduleHarness(t)
			h.now = now
			g := seedWeeksRider(t, h, "wilant")
			resp := h.as("wilant", "cyclists", http.MethodPost, fmt.Sprintf("/api/training/goals/%s/schedule", g.ID), "")
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
				t.Fatalf("status %d", resp.StatusCode)
			}
			all, err := h.store.ListWorkouts(context.Background(), "wilant")
			if err != nil {
				t.Fatal(err)
			}
			if got, want := len(all), remainingThisWeek(now); got != want {
				t.Errorf("filled %d sessions, want %d", got, want)
			}
			assertNothingInThePast(t, h, all)
		})
	}
}
