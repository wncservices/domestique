package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func stepSeconds(steps []workout.WorkoutStep) float64 {
	total := 0.0
	for _, st := range steps {
		if len(st.Steps) > 0 {
			n := float64(max(st.Repeat, 1))
			total += n * stepSeconds(st.Steps)
			continue
		}
		total += st.Seconds
	}
	return total
}

// weekSeconds is the planned time of the goal's sessions in the week from monday.
func (h *seasonHarness) weekSeconds(t *testing.T, goalID string, monday time.Time) float64 {
	t.Helper()
	from, to := monday.Format("2006-01-02"), monday.AddDate(0, 0, 6).Format("2006-01-02")
	total := 0.0
	for _, w := range h.goalSessions(t, goalID) {
		if w.Date >= from && w.Date <= to {
			total += stepSeconds(w.Steps)
		}
	}
	return total
}

// ageGoal makes a goal look as old as one made at created, with no re-dating.
func (h *seasonHarness) ageGoal(t *testing.T, id, created string) {
	t.Helper()
	if _, err := h.conn.Exec(`UPDATE goals SET created_at = ? WHERE id = ?`, created, id); err != nil {
		t.Fatal(err)
	}
}

func (h *seasonHarness) goal(t *testing.T, id string) workout.Goal {
	t.Helper()
	g, err := h.store.GetGoal(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// An undated goal made 30 weeks ago and given an event 10 weeks out gets the
// plan of a goal created today: the anchor was the creation Monday, so the
// first week used to land 30 weeks into the plan, in build or later, with no base.
func TestRedatingAnOldGoalStartsItsPlanFromBase(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Keep going"}`)
	h.srv.WaitForBackground()
	h.ageGoal(t, g.ID, "2026-03-11T00:00:00Z")
	h.backdate(t)

	event := h.now.AddDate(0, 0, 70).Format("2006-01-02")
	h.patchGoal(t, g.ID, `{"eventDate":"`+event+`"}`)

	got := h.planWeeks(t, h.goal(t, g.ID))
	fresh := h.planWeeks(t, workout.Goal{ID: g.ID, EventDate: event, CreatedAt: h.now.Format(time.RFC3339)})
	if len(got) != len(fresh) || len(got) == 0 {
		t.Fatalf("plan has %d weeks, want the %d of a goal created today", len(got), len(fresh))
	}
	for i := range got {
		if got[i].Number != fresh[i].Number || got[i].Phase != fresh[i].Phase || got[i].Recovery != fresh[i].Recovery {
			t.Errorf("week %d: number %d %s recovery=%v, want %d %s recovery=%v", i, got[i].Number, got[i].Phase, got[i].Recovery,
				fresh[i].Number, fresh[i].Phase, fresh[i].Recovery)
		}
	}
	if got[0].Phase != periodization.PhaseBase {
		t.Errorf("first week is %s, want base", got[0].Phase)
	}
	// The rebuilt sessions follow that plan: this week holds the goal's sessions.
	thisMon, _ := weekBounds(h.now)
	if len(h.week(t, thisMon)) == 0 {
		t.Error("no sessions this week after re-dating")
	}
}

// Moving the event later turns the weeks that were the old taper into ordinary
// training: the untouched sessions are rebuilt from today.
func TestMovingTheEventLaterRebuildsTheTaperWeeks(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	oldEvent := h.now.AddDate(0, 0, 70)
	g := h.createGoal(t, `{"name":"Race","eventDate":"`+oldEvent.Format("2006-01-02")+`"}`)
	h.srv.WaitForBackground()
	h.backdate(t)

	oldTaperMonday := periodization.MondayOf(oldEvent)
	before := h.weekSeconds(t, g.ID, oldTaperMonday)
	if before == 0 {
		t.Fatal("the old event week holds no sessions")
	}

	h.patchGoal(t, g.ID, `{"eventDate":"`+h.now.AddDate(0, 0, 140).Format("2006-01-02")+`"}`)

	if after := h.weekSeconds(t, g.ID, oldTaperMonday); after <= before {
		t.Errorf("the old event week holds %.0f s after moving the event later, want more than the taper's %.0f s", after, before)
	}
	if d := twoOnADay(h.workouts(t), h.now.Format("2006-01-02")); d != "" {
		t.Errorf("two sessions on %s", d)
	}
}

// Moving the event earlier leaves nothing after it and a taper before it.
func TestMovingTheEventEarlierKeepsATaperBeforeTheNewDate(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"`+h.now.AddDate(0, 0, 140).Format("2006-01-02")+`"}`)
	h.srv.WaitForBackground()
	h.backdate(t)

	newEvent := h.now.AddDate(0, 0, 70)
	h.patchGoal(t, g.ID, `{"eventDate":"`+newEvent.Format("2006-01-02")+`"}`)

	// The plan fills the whole event week (the days after the event too, as it
	// always has); nothing may reach the week after it.
	last := periodization.MondayOf(newEvent).AddDate(0, 0, 6).Format("2006-01-02")
	var peak float64
	for _, w := range h.goalSessions(t, g.ID) {
		if w.Date > last {
			t.Errorf("session on %s is after the new event week, which ends %s", w.Date, last)
		}
	}
	for m := periodization.MondayOf(h.now); !m.After(newEvent); m = m.AddDate(0, 0, 7) {
		peak = max(peak, h.weekSeconds(t, g.ID, m))
	}
	eventWeek := h.weekSeconds(t, g.ID, periodization.MondayOf(newEvent))
	if eventWeek == 0 || eventWeek >= peak {
		t.Errorf("event week holds %.0f s against a peak week of %.0f s, want a lighter, non-empty taper", eventWeek, peak)
	}
}

// A nudge of 14 days or less keeps the anchor (the build done so far counts) but
// still rebuilds the untouched sessions so the taper sits before the new date.
func TestANudgeOfTheEventDateKeepsTheAnchorAndMovesTheTaper(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"`+h.now.AddDate(0, 0, 70).Format("2006-01-02")+`"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	h.ageGoal(t, g.ID, "2026-08-12T00:00:00Z")
	before := h.planWeeks(t, h.goal(t, g.ID))

	newEvent := h.now.AddDate(0, 0, 73)
	h.patchGoal(t, g.ID, `{"eventDate":"`+newEvent.Format("2006-01-02")+`"}`)

	got := h.goal(t, g.ID)
	if got.PlanAnchor != "" {
		t.Errorf("anchor reset to %q by a 3-day move", got.PlanAnchor)
	}
	after := h.planWeeks(t, got)
	if after[0].Number != before[0].Number {
		t.Errorf("week numbering changed: %d, was %d", after[0].Number, before[0].Number)
	}
	var peak float64
	for m := periodization.MondayOf(h.now); !m.After(newEvent); m = m.AddDate(0, 0, 7) {
		peak = max(peak, h.weekSeconds(t, g.ID, m))
	}
	if ev := h.weekSeconds(t, g.ID, periodization.MondayOf(newEvent)); ev == 0 || ev >= peak {
		t.Errorf("event week holds %.0f s against a peak of %.0f s, want a lighter taper", ev, peak)
	}
}

func TestAMoveOfMoreThanTwoWeeksResetsTheAnchor(t *testing.T) {
	for days, want := range map[int]bool{14: false, 15: true, -15: true, -14: false} {
		h := newSeasonHarness(t)
		h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
		g := h.createGoal(t, `{"name":"Race","eventDate":"`+h.now.AddDate(0, 0, 70).Format("2006-01-02")+`"}`)
		h.srv.WaitForBackground()
		h.patchGoal(t, g.ID, `{"eventDate":"`+h.now.AddDate(0, 0, 70+days).Format("2006-01-02")+`"}`)
		if got := h.goal(t, g.ID).PlanAnchor != ""; got != want {
			t.Errorf("move of %d days: anchor reset=%v, want %v", days, got, want)
		}
	}
}
