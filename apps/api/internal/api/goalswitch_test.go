package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A dated goal made undated is the rolling twelve-week plan again: nothing past
// that window stays, and its event-week taper is not left behind.
func TestMakingAGoalUndatedTrimsItToTwelveWeeksAndDropsTheTaper(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	thisMon, _ := weekBounds(h.now)
	// An event five weeks out: the dated plan has a taper before it and nothing after.
	g := h.createGoal(t, `{"name":"Race","eventDate":"2026-11-11"}`)
	h.srv.WaitForBackground()
	h.backdate(t)

	h.patchGoal(t, g.ID, `{"eventDate":""}`)

	if got := h.goal(t, g.ID).EventDate; got != "" {
		t.Fatalf("event date is %q after clearing it", got)
	}
	last := thisMon.AddDate(0, 0, 7*12).Format("2006-01-02") // the Monday after the 12 weeks
	for _, w := range h.goalSessions(t, g.ID) {
		if w.Date >= last {
			t.Errorf("session on %s is past the twelve rolling weeks", w.Date)
		}
	}
	for i := 0; i < 12; i++ {
		want := 4
		if i == 0 {
			want = 3
		}
		if n := len(h.week(t, thisMon.AddDate(0, 0, 7*i))); n != want {
			t.Errorf("week +%d has %d sessions, want %d", i, n, want)
		}
	}
	seen := map[string]bool{}
	for _, w := range h.goalSessions(t, g.ID) {
		if seen[w.Date] {
			t.Errorf("two sessions on %s", w.Date)
		}
		seen[w.Date] = true
	}
}

// An undated goal given a date plans up to the event, one session a day at most.
func TestDatingAnUndatedGoalFillsUpToTheEvent(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	thisMon, _ := weekBounds(h.now)
	g := h.createGoal(t, `{"name":"Keep riding"}`)
	h.srv.WaitForBackground()
	h.backdate(t)

	h.patchGoal(t, g.ID, `{"eventDate":"`+raceTwentyWeeksOut+`"}`)

	byDate := map[string]int{}
	lastDate := ""
	for _, w := range h.goalSessions(t, g.ID) {
		byDate[w.Date]++
		if w.Date > lastDate {
			lastDate = w.Date
		}
	}
	for d, n := range byDate {
		if n > 1 {
			t.Errorf("%d sessions on %s", n, d)
		}
	}
	if lastDate == "" || lastDate > raceTwentyWeeksOut || lastDate < "2027-02-20" {
		t.Errorf("last session is on %q, want the days up to the event %s", lastDate, raceTwentyWeeksOut)
	}
	// Every week to the event is planned, not just the old twelve.
	for i := 0; i < 20; i++ {
		if n := len(h.week(t, thisMon.AddDate(0, 0, 7*i))); n == 0 {
			t.Errorf("week +%d is empty", i)
		}
	}
}

// A sport change over a week where the rider had edited one session fills
// every day of that week with the new sport: the edited cycling session goes
// with the rest, and the rebuild forgot the week, so the season pass fills it
// rather than adopting it as already filled.
func TestASportChangeFillsAWeekThatHadAnEditedSession(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	h.backdate(t)

	thisMon, _ := weekBounds(h.now)
	monday := thisMon.AddDate(0, 0, 14)
	edited := h.week(t, monday)[0]
	name := "My own name"
	if _, err := h.store.UpdateWorkout(ctx, edited.ID, workout.UpdateWorkoutRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}

	h.patchGoal(t, g.ID, `{"sport":"running"}`)

	week := h.week(t, monday)
	if len(week) != 4 {
		t.Fatalf("the week has %d sessions, want 4 running ones", len(week))
	}
	for _, w := range week {
		if w.Sport != model.SportRunning || w.Name == name {
			t.Errorf("session on %s is %s %q, want a new running session", w.Date, w.Sport, w.Name)
		}
	}
	if d := twoOnADay(h.workouts(t), h.now.Format("2006-01-02")); d != "" {
		t.Errorf("two sessions on %s", d)
	}
}

// No plan session is dated after the event: an event on a Wednesday leaves the
// rest of that week empty.
func TestADatedGoalPlansNothingAfterTheEvent(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"`+raceTwentyWeeksOut+`"}`) // a Wednesday
	h.srv.WaitForBackground()

	got := 0
	for _, w := range h.goalSessions(t, g.ID) {
		got++
		if w.Date > raceTwentyWeeksOut {
			t.Errorf("session on %s is after the event on %s", w.Date, raceTwentyWeeksOut)
		}
	}
	if got == 0 {
		t.Fatal("no sessions planned")
	}
}

// Moving the event earlier removes the untouched sessions of the event week that
// now fall after it, not only those of later weeks.
func TestMovingTheEventEarlierClearsTheNewEventWeeksTail(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-02-27"}`) // a Saturday
	h.srv.WaitForBackground()
	h.backdate(t)

	h.patchGoal(t, g.ID, `{"eventDate":"`+raceTwentyWeeksOut+`"}`)

	for _, w := range h.goalSessions(t, g.ID) {
		if w.Date > raceTwentyWeeksOut {
			t.Errorf("session on %s survived past the new event %s", w.Date, raceTwentyWeeksOut)
		}
	}
}

// The event week belongs to the dated goal whole: the stay-fit goal resumes the
// Monday after, so the days between the event and Sunday stay empty rather than
// being split between two goals.
func TestTheStayFitGoalResumesTheWeekAfterTheEvent(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	stay := h.createGoal(t, `{"name":"Stay fit"}`)
	race := h.createGoal(t, `{"name":"Race","eventDate":"`+raceTwentyWeeksOut+`"}`)
	h.srv.WaitForBackground()
	h.now = utcNoon(2027, time.March, 3)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(context.Background())
	h.srv.WaitForBackground()

	for _, w := range h.goalSessions(t, stay.ID) {
		if w.Date >= "2027-02-22" && w.Date <= "2027-02-28" {
			t.Errorf("stay-fit session on %s, inside the race's event week", w.Date)
		}
	}
	after := 0
	for _, w := range h.goalSessions(t, stay.ID) {
		if w.Date > "2027-02-28" {
			after++
		}
	}
	if after == 0 {
		t.Error("the stay-fit goal has nothing after the event week")
	}
	_ = race
}
