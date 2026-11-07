package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// 20 weeks after the harness clock (2026-10-07), a Wednesday.
const raceTwentyWeeksOut = "2027-02-24"

// twoOnADay returns a date holding more than one session, or "".
func twoOnADay(ws []workout.Workout, from string) string {
	seen := map[string]bool{}
	for _, w := range ws {
		if w.Date < from {
			continue
		}
		if seen[w.Date] {
			return w.Date
		}
		seen[w.Date] = true
	}
	return ""
}

func (h *seasonHarness) stayFitThenRace(t *testing.T) (stay, race workout.Goal) {
	t.Helper()
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	stay = h.createGoal(t, `{"name":"Stay fit"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	if len(h.goalSessions(t, stay.ID)) == 0 {
		t.Fatal("the stay-fit goal has no sessions before the race")
	}
	race = h.createGoal(t, `{"name":"Race","eventDate":"`+raceTwentyWeeksOut+`"}`)
	h.srv.WaitForBackground()
	return stay, race
}

// A race added after a stay-fit goal filled its weeks owns every available day
// to its event: before, the stay-fit goal's recorded weeks held the days and
// the race was recorded empty in them for good.
func TestARaceAddedLaterTakesOverTheStayFitGoalsDays(t *testing.T) {
	h := newSeasonHarness(t)
	stay, race := h.stayFitThenRace(t)
	today := h.now.Format("2006-01-02")

	for _, w := range h.goalSessions(t, stay.ID) {
		if w.Date >= today && w.Date <= raceTwentyWeeksOut {
			t.Errorf("stay-fit session on %s survived the takeover", w.Date)
		}
	}
	// Every available day from today to the event is the race's.
	want := 0
	for d := h.now; d.Format("2006-01-02") <= raceTwentyWeeksOut; d = d.AddDate(0, 0, 1) {
		switch d.Weekday() {
		case time.Tuesday, time.Thursday, time.Saturday, time.Sunday:
			want++
		}
	}
	got := 0
	for _, w := range h.goalSessions(t, race.ID) {
		if w.Date >= today && w.Date <= raceTwentyWeeksOut {
			got++
		}
	}
	// This Wednesday's Tuesday has gone, and the event week's tail is taper: allow
	// the race to leave the past Tuesday out but not a whole week.
	if got < want-2 {
		t.Errorf("the race has %d sessions to its event, want about %d", got, want)
	}
	if d := twoOnADay(h.workouts(t), today); d != "" {
		t.Errorf("two sessions on %s", d)
	}
	// Weeks the takeover emptied are not recorded for the stay-fit goal.
	weeks, err := h.store.ScheduledWeeks(context.Background(), stay.ID)
	if err != nil {
		t.Fatal(err)
	}
	thisMon, _ := weekBounds(h.now)
	for w := range weeks {
		if w >= thisMon.Format("2006-01-02") && w <= raceTwentyWeeksOut {
			t.Errorf("stay-fit week %s is still recorded", w)
		}
	}
}

// After the race the stay-fit goal fills again through the normal season pass.
func TestTheUndatedGoalComesBackAfterTheRace(t *testing.T) {
	h := newSeasonHarness(t)
	stay, _ := h.stayFitThenRace(t)

	h.now = utcNoon(2027, time.March, 3) // the Wednesday after the race
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(context.Background())
	h.srv.WaitForBackground()

	after := 0
	for _, w := range h.goalSessions(t, stay.ID) {
		if w.Date > raceTwentyWeeksOut {
			after++
		}
	}
	if after == 0 {
		t.Fatal("the stay-fit goal has no sessions after the race")
	}
}

// A session the rider edited on a stay-fit day is theirs: the takeover leaves
// it, and the race does not plan on top of it.
func TestARiderEditedStayFitSessionSurvivesTheTakeover(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	stay := h.createGoal(t, `{"name":"Stay fit"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	thisMon, _ := weekBounds(h.now)
	edited := h.week(t, thisMon.AddDate(0, 0, 14))[0]
	name := "My own name"
	if _, err := h.store.UpdateWorkout(context.Background(), edited.ID, workout.UpdateWorkoutRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}

	h.createGoal(t, `{"name":"Race","eventDate":"`+raceTwentyWeeksOut+`"}`)
	h.srv.WaitForBackground()

	var kept bool
	for _, w := range h.goalSessions(t, stay.ID) {
		if w.ID == edited.ID && w.Name == name {
			kept = true
		}
	}
	if !kept {
		t.Error("the rider's edited stay-fit session was removed")
	}
	if d := twoOnADay(h.workouts(t), h.now.Format("2006-01-02")); d != "" {
		t.Errorf("two sessions on %s", d)
	}
}

// Deleting the race frees its days: the stay-fit goal's weeks were forgotten,
// so the season pass refills them.
func TestDeletingTheRaceLetsTheStayFitGoalRefill(t *testing.T) {
	h := newSeasonHarness(t)
	stay, race := h.stayFitThenRace(t)
	// A tick while the race holds the days must not record the stay-fit weeks
	// empty: they would stay empty after the race is gone.
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(context.Background())
	h.srv.WaitForBackground()
	if n := len(h.goalSessions(t, stay.ID)); n != 0 {
		t.Fatalf("the stay-fit goal has %d sessions while the race holds its days", n)
	}
	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/goals/"+race.ID, ""); resp.StatusCode >= 300 {
		t.Fatalf("delete race: status %d", resp.StatusCode)
	}
	h.srv.AutoScheduleTick(context.Background())
	h.srv.WaitForBackground()

	if n := len(h.goalSessions(t, stay.ID)); n == 0 {
		t.Fatal("the stay-fit goal did not refill after the race was deleted")
	}
}

// Two dated goals do not take over from each other: the first to fill keeps
// its days, and the second does not double-book them.
func TestTwoDatedGoalsKeepTodaysFirstComeFirstServed(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	first := h.createGoal(t, `{"name":"First","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	before := len(h.goalSessions(t, first.ID))
	second := h.createGoal(t, `{"name":"Second","eventDate":"`+raceTwentyWeeksOut+`"}`)
	h.srv.WaitForBackground()

	if after := len(h.goalSessions(t, first.ID)); after != before {
		t.Errorf("the first goal has %d sessions after a second dated goal, had %d", after, before)
	}
	if n := len(h.goalSessions(t, second.ID)); n != 0 {
		t.Errorf("the second goal planned %d sessions on days the first holds", n)
	}
	if d := twoOnADay(h.workouts(t), h.now.Format("2006-01-02")); d != "" {
		t.Errorf("two sessions on %s", d)
	}
}

// An undated goal edited to be dated, or a dated goal's date moved later, takes
// over the same way.
func TestSettingADateOnAGoalTakesTheStayFitDays(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	stay := h.createGoal(t, `{"name":"Stay fit"}`)
	other := h.createGoal(t, `{"name":"Later race"}`)
	h.srv.WaitForBackground()

	h.patchGoal(t, other.ID, `{"eventDate":"`+raceTwentyWeeksOut+`"}`)

	for _, w := range h.goalSessions(t, stay.ID) {
		if w.Date >= h.now.Format("2006-01-02") && w.Date <= raceTwentyWeeksOut {
			t.Errorf("stay-fit session on %s survived", w.Date)
		}
	}
	got := 0
	for _, w := range h.goalSessions(t, other.ID) {
		if w.Date >= h.now.Format("2006-01-02") && w.Date <= raceTwentyWeeksOut {
			got++
		}
	}
	if got == 0 {
		t.Error("the newly dated goal received no sessions on the days it took")
	}
}

// fillStayFitWeek asks for the undated goal's week to be filled, the way its
// Fill button or a tick would.
func (h *seasonHarness) fillStayFitWeek(t *testing.T, id string, monday time.Time) {
	t.Helper()
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals/"+id+"/schedule",
		`{"weekStart":"`+monday.Format("2006-01-02")+`"}`)
}

// The takeover and the race's own fill are two lock holds. An undated goal's
// fill that lands between them must not take the freed weeks back, or the race
// finds its days held and stays empty.
func TestAnUndatedFillBetweenTheTakeoverAndTheRaceFillLeavesTheDaysToTheRace(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	stay := h.createGoal(t, `{"name":"Stay fit"}`)
	h.srv.WaitForBackground()
	h.backdate(t)

	gate := make(chan struct{})
	h.srv.BeforeSeasonFill = func() { <-gate }
	race := h.createGoal(t, `{"name":"Race","eventDate":"`+raceTwentyWeeksOut+`"}`)

	thisMon, _ := weekBounds(h.now)
	for i := 1; i <= 6; i++ {
		h.fillStayFitWeek(t, stay.ID, thisMon.AddDate(0, 0, 7*i))
	}
	close(gate)
	h.srv.WaitForBackground()

	for _, w := range h.goalSessions(t, stay.ID) {
		if w.Date >= h.now.Format("2006-01-02") && w.Date <= raceTwentyWeeksOut {
			t.Errorf("stay-fit session on %s inside the race span", w.Date)
		}
	}
	got := 0
	for _, w := range h.goalSessions(t, race.ID) {
		if w.Date >= thisMon.AddDate(0, 0, 7).Format("2006-01-02") && w.Date < thisMon.AddDate(0, 0, 49).Format("2006-01-02") {
			got++
		}
	}
	if got < 6*3 {
		t.Errorf("the race has %d sessions in weeks 1-6, want its days", got)
	}
}

// A race's recovery week may leave available days free; they are still the
// race's weeks, not room for the stay-fit goal.
func TestARaceRecoveryWeekDoesNotGetStayFitSessions(t *testing.T) {
	h := newSeasonHarness(t)
	stay, race := h.stayFitThenRace(t)
	var rec string
	for _, wk := range h.planWeeks(t, race) {
		if wk.Recovery {
			rec = wk.StartDate
			break
		}
	}
	if rec == "" {
		t.Fatal("no recovery week in the race plan")
	}
	mon, _ := time.Parse("2006-01-02", rec)
	h.fillStayFitWeek(t, stay.ID, mon)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(context.Background())
	h.srv.WaitForBackground()
	if n := len(datesIn(t, h.goalSessions(t, stay.ID), mon, mon.AddDate(0, 0, 6))); n != 0 {
		t.Errorf("the stay-fit goal has %d sessions in the race's recovery week", n)
	}
}

// A week already covered by the goal's own sessions is still recorded when it
// is filled again (adopted): only other goals' sessions make a week unfilled.
func TestAWeekCoveredByTheGoalsOwnSessionsIsStillRecorded(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Stay fit"}`)
	h.srv.WaitForBackground()
	thisMon, _ := weekBounds(h.now)
	week := thisMon.AddDate(0, 0, 14)
	if err := h.store.DeleteScheduledWeeksAfter(context.Background(), g.ID, week.AddDate(0, 0, -7).Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	h.fillStayFitWeek(t, g.ID, week)
	weeks, err := h.store.ScheduledWeeks(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := weeks[week.Format("2006-01-02")]; !ok {
		t.Error("a week covered by the goal's own sessions was not recorded")
	}
}

// weekCount counts a goal's sessions in the week starting monday.
func (h *seasonHarness) weekCount(t *testing.T, goalID string, monday time.Time) int {
	t.Helper()
	n := 0
	end := monday.AddDate(0, 0, 6).Format("2006-01-02")
	for _, w := range h.goalSessions(t, goalID) {
		if w.Date >= monday.Format("2006-01-02") && w.Date <= end {
			n++
		}
	}
	return n
}

// The days a race gave up are the stay-fit goal's again: a week where it kept a
// session the rider edited is filled around it, not recorded as already filled
// with that one session.
func TestGivingUpARaceRefillsTheWeekAroundAKeptSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		free func(t *testing.T, h *seasonHarness, race workout.Goal)
	}{
		{"delete", func(t *testing.T, h *seasonHarness, race workout.Goal) { h.deleteGoal(t, race.ID) }},
		{"undated", func(t *testing.T, h *seasonHarness, race workout.Goal) {
			h.patchGoal(t, race.ID, `{"eventDate":""}`)
			h.srv.WaitForBackground()
		}},
		{"moved earlier", func(t *testing.T, h *seasonHarness, race workout.Goal) {
			h.patchGoal(t, race.ID, `{"eventDate":"2026-10-14"}`)
			h.srv.WaitForBackground()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSeasonHarness(t)
			h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
			stay := h.createGoal(t, `{"name":"Stay fit"}`)
			h.srv.WaitForBackground()
			h.backdate(t)
			thisMon, _ := weekBounds(h.now)
			target := thisMon.AddDate(0, 0, 14)
			edited := h.week(t, target)[0]
			name := "My own name"
			if _, err := h.store.UpdateWorkout(context.Background(), edited.ID, workout.UpdateWorkoutRequest{Name: &name}); err != nil {
				t.Fatal(err)
			}
			race := h.createGoal(t, `{"name":"Race","eventDate":"`+raceTwentyWeeksOut+`"}`)
			h.srv.WaitForBackground()

			tc.free(t, h, race)

			// The undated stay-fit goal owns the week again: the edited session
			// plus a new one on each of the other three days.
			if got := h.weekCount(t, stay.ID, target); got != 4 {
				t.Errorf("stay-fit sessions in the freed week = %d, want 4", got)
			}
			if d := twoOnADay(h.workouts(t), h.now.Format("2006-01-02")); d != "" {
				t.Errorf("two sessions on %s", d)
			}
		})
	}
}
