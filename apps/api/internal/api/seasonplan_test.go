package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// All of these run on a fixed UTC clock (Wednesday 2026-10-07) and build every
// date in UTC, so they pass under any TZ.

// seasonHarness is a rider with a four-day profile and an FTP of zero, on a
// clock the test moves. Auto-schedule is off unless a test turns it on.
type seasonHarness struct {
	*autoScheduleHarness
	now time.Time
}

func newSeasonHarness(t *testing.T) *seasonHarness {
	t.Helper()
	h := &seasonHarness{autoScheduleHarness: newAutoScheduleHarness(t), now: utcNoon(2026, time.October, 7)}
	h.srv.Clock = func() time.Time { return h.now }
	return h
}

func (h *seasonHarness) profile(t *testing.T, days []string, ftp float64) workout.RiderProfile {
	t.Helper()
	p, err := h.store.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 3, AvailableDays: days, FTPWatts: ftp,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (h *seasonHarness) workouts(t *testing.T) []workout.Workout {
	t.Helper()
	all, err := h.store.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// fromNextWeek counts the workouts dated next Monday or later. The tick also
// adapts this week (a session missed earlier in it is made up on an easy day),
// which is not what these tests are about.
func (h *seasonHarness) fromNextWeek(t *testing.T) int {
	t.Helper()
	_, nextMon := weekBounds(h.now)
	n := 0
	for _, w := range h.workouts(t) {
		if w.Date >= nextMon.Format("2006-01-02") {
			n++
		}
	}
	return n
}

func (h *seasonHarness) week(t *testing.T, monday time.Time) []workout.Workout {
	t.Helper()
	return datesIn(t, h.workouts(t), monday, monday.AddDate(0, 0, 6))
}

func (h *seasonHarness) createGoal(t *testing.T, body string) workout.Goal {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create goal: status %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	g, err := h.store.GetGoal(context.Background(), out.ID)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// planWeeks is every week the plan covers as of the harness clock.
func (h *seasonHarness) planWeeks(t *testing.T, g workout.Goal) []periodization.Week {
	t.Helper()
	profile, _, err := h.store.GetProfile(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := periodization.Build(g, profile, h.now)
	if err != nil {
		t.Fatal(err)
	}
	return plan.Weeks
}

// backdate makes every workout look as old as one filled months ago, with no
// edit since: created_at == updated_at, in the past. A rider's later edit then
// shows as updated_at moving on.
func (h *seasonHarness) backdate(t *testing.T) {
	t.Helper()
	if _, err := h.conn.Exec(`UPDATE workouts SET created_at = '2026-01-01T00:00:00Z', updated_at = '2026-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
}

// Creating a goal with an event 27 weeks out answers after this week is
// filled, and the rest of the season follows in the background — with
// auto-schedule off, because the rider asked for the goal themselves.
func TestCreatingAGoalPlansTheWholeSeasonInTheBackground(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	thisMon, _ := weekBounds(h.now)

	gate := make(chan struct{})
	h.srv.BeforeSeasonFill = func() { <-gate }

	g := h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14"}`)

	if n := len(h.week(t, thisMon)); n != 4 {
		t.Errorf("this week has %d sessions when the response returned, want 4", n)
	}
	if n := len(h.workouts(t)); n != 4 {
		t.Errorf("%d workouts exist when the response returned, want only this week's 4 — the rest is for the background", n)
	}

	close(gate)
	h.srv.WaitForBackground()

	weeks := h.planWeeks(t, g)
	if len(weeks) < 27 {
		t.Fatalf("the plan has %d weeks, want the event 27+ weeks out", len(weeks))
	}
	for _, wk := range weeks {
		start, _ := time.Parse("2006-01-02", wk.StartDate)
		if n := len(h.week(t, start)); n != 4 {
			t.Errorf("week %s has %d sessions, want 4", wk.StartDate, n)
		}
	}
	if got, want := len(h.workouts(t)), 4*len(weeks); got != want {
		t.Errorf("%d workouts in all, want %d (four a week, nothing past the event)", got, want)
	}
}

// Editing a goal, or a tick, finds every week recorded and creates nothing.
func TestASecondSeasonPassCreatesNothing(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	before := h.fromNextWeek(t)
	if before == 0 {
		t.Fatal("nothing was planned")
	}

	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"notes":"hills"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update goal: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(context.Background())

	if after := h.fromNextWeek(t); after != before {
		t.Errorf("workouts after this week = %d after an edit and a tick, want still %d", after, before)
	}
}

// A goal with no date is a rolling twelve-week plan.
func TestAnUndatedGoalPlansTwelveWeeks(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	thisMon, _ := weekBounds(h.now)

	h.createGoal(t, `{"name":"Keep riding"}`)
	h.srv.WaitForBackground()

	for i := 0; i < 12; i++ {
		if n := len(h.week(t, thisMon.AddDate(0, 0, 7*i))); n != 4 {
			t.Errorf("week +%d has %d sessions, want 4", i, n)
		}
	}
	if n := len(h.week(t, thisMon.AddDate(0, 0, 7*12))); n != 0 {
		t.Errorf("week +12 has %d sessions, want none — twelve weeks is the window", n)
	}
}

// The tick is what keeps a rolling plan twelve weeks deep, and it fills only
// when auto-schedule is on.
func TestTheTickFillsTheSeasonOnlyWhenAutoScheduleIsOn(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g, err := h.store.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Race", EventDate: "2027-04-14"})
	if err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(context.Background())
	if n := len(h.workouts(t)); n != 0 {
		t.Fatalf("the tick planned %d workouts with auto-schedule off", n)
	}

	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(context.Background())
	if got, want := h.fromNextWeek(t), 4*(len(h.planWeeks(t, g))-1); got != want {
		t.Errorf("after one tick: %d workouts from next week on, want %d — every week to the event", got, want)
	}
}

// A week far ahead comes from the plan's own week, not a copy of this one: a
// recovery week is easy however far out it is built.
func TestARecoveryWeekFarAheadHasRecoveryContent(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()

	var recovery periodization.Week
	for _, wk := range h.planWeeks(t, g)[3:] {
		if wk.Recovery {
			recovery = wk
			break
		}
	}
	if recovery.StartDate == "" {
		t.Fatal("no recovery week found beyond next week")
	}
	start, _ := time.Parse("2006-01-02", recovery.StartDate)
	sessions := h.week(t, start)
	if len(sessions) == 0 {
		t.Fatalf("recovery week %s is empty", recovery.StartDate)
	}
	for _, s := range sessions {
		if workout.IsStructuredZone(s.Zone) || s.Name == "Long ride" {
			t.Errorf("recovery week %s holds %q (%s), want easy sessions only", recovery.StartDate, s.Name, s.Zone)
		}
	}
}

// With no availability yet there is nothing to plan, and that must not be
// recorded as done: filling the profile in later still gets the season.
func TestAnEmptyPlanIsNotRecordedAsFilled(t *testing.T) {
	h := newSeasonHarness(t)
	h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14"}`) // no profile yet
	h.srv.WaitForBackground()
	if n := len(h.workouts(t)); n != 0 {
		t.Fatalf("%d workouts planned for a rider with no profile", n)
	}

	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(context.Background())
	if n := len(h.workouts(t)); n < 4*20 {
		t.Errorf("%d workouts after the profile was filled in and a tick, want the whole season", n)
	}
}

// When a far week becomes next week its untouched sessions are rebuilt from
// today's FTP and levels. Everything the rider or the app has touched, and
// every empty date, stays as it was.
func TestARolledOverWeekIsRefreshedForUntouchedSessionsOnly(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	days := []string{"mon", "tue", "wed", "thu", "fri", "sat"}
	h.profile(t, days, 0)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	g, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)
	h.backdate(t)

	thisMon, _ := weekBounds(h.now)
	target := thisMon.AddDate(0, 0, 7*10) // built ten weeks ahead
	after := target.AddDate(0, 0, 7)
	week := h.week(t, target)
	if len(week) != 6 {
		t.Fatalf("target week has %d sessions, want 6", len(week))
	}
	untouched, moved, edited, eased, replaced, deleted := week[0], week[1], week[2], week[3], week[4], week[5]

	// Moved by the rider, through the API, so it carries the real marker.
	sunday := target.AddDate(0, 0, 6).Format("2006-01-02")
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+moved.ID, `{"date":"`+sunday+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("move: status %d", resp.StatusCode)
	}
	newName := "My own name"
	if _, err := h.store.UpdateWorkout(ctx, edited.ID, workout.UpdateWorkoutRequest{Name: &newName}); err != nil {
		t.Fatal(err)
	}
	easedDesc := eased.Description + " " + scheduler.AdjustedMarker + " eased."
	if _, err := h.store.UpdateWorkout(ctx, eased.ID, workout.UpdateWorkoutRequest{Description: &easedDesc}); err != nil {
		t.Fatal(err)
	}
	// An FTP test scheduled onto a day takes that day's plan-made session's place.
	test, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Name: "FTP test", GoalID: g.ID, Date: replaced.Date, Description: "A test.",
		TestProtocol: "twenty_minute", Steps: replaced.Steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.DeleteWorkout(ctx, replaced.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DeleteWorkout(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	farBefore := h.week(t, after)

	// FTP is now known, so a rebuilt session carries power targets it did not.
	h.profile(t, days, 250)
	h.now = utcNoon(2026, time.December, 9) // the Wednesday before target
	if periodization.MondayOf(h.now).AddDate(0, 0, 7).Format("2006-01-02") != target.Format("2006-01-02") {
		t.Fatalf("clock %s does not put %s next", h.now, target)
	}
	h.srv.AutoScheduleTick(ctx)

	byID := map[string]workout.Workout{}
	for _, w := range h.workouts(t) {
		byID[w.ID] = w
	}
	if got := byID[untouched.ID]; !hasPowerTarget(got) || hasPowerTarget(untouched) {
		t.Errorf("the untouched session was not rebuilt with today's FTP: before power=%v after power=%v", hasPowerTarget(untouched), hasPowerTarget(got))
	} else if got.ID != untouched.ID || got.Date != untouched.Date || got.Name != untouched.Name || got.Zone != untouched.Zone {
		t.Errorf("a refresh must keep the id, date, slot name and zone: %+v -> %+v", untouched, got)
	}
	if got := byID[moved.ID]; got.Date != sunday || hasPowerTarget(got) {
		t.Errorf("the moved session was touched: %+v", got)
	}
	if got := byID[edited.ID]; got.Name != newName || hasPowerTarget(got) {
		t.Errorf("the edited session was touched: %+v", got)
	}
	if got := byID[eased.ID]; got.Description != easedDesc || hasPowerTarget(got) {
		t.Errorf("the eased session was touched: %+v", got)
	}
	if got := byID[test.ID]; got.Name != "FTP test" || got.Description != "A test." {
		t.Errorf("the FTP test was touched: %+v", got)
	}
	if _, ok := byID[replaced.ID]; ok {
		t.Error("the session an FTP test replaced came back")
	}
	if _, ok := byID[deleted.ID]; ok {
		t.Error("a deleted session came back")
	}
	// Untouched, moved (now on the Sunday), edited, eased and the test: the
	// deleted day stays empty.
	if n := len(h.week(t, target)); n != 5 {
		t.Errorf("target week has %d sessions, want 5 (nothing created on an empty date)", n)
	}
	// A week further out than next is left for its own turn.
	for i, w := range h.week(t, after) {
		if hasPowerTarget(w) != hasPowerTarget(farBefore[i]) {
			t.Errorf("week after next was refreshed early: %+v", w)
		}
	}

	// Once: a later FTP change does not rebuild a week already refreshed.
	rebuilt := byID[untouched.ID]
	h.profile(t, days, 300)
	h.now = h.now.Add(time.Hour)
	h.srv.AutoScheduleTick(ctx)
	again, err := h.store.GetWorkout(ctx, untouched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.UpdatedAt != rebuilt.UpdatedAt || !sameSteps(again, rebuilt) {
		t.Error("a week already refreshed was rebuilt a second time")
	}
}

func hasPowerTarget(w workout.Workout) bool {
	var walk func([]workout.WorkoutStep) bool
	walk = func(steps []workout.WorkoutStep) bool {
		for _, s := range steps {
			if s.Target == workout.TargetPower || walk(s.Steps) {
				return true
			}
		}
		return false
	}
	return walk(w.Steps)
}

func sameSteps(a, b workout.Workout) bool {
	x, _ := json.Marshal(a.Steps)
	y, _ := json.Marshal(b.Steps)
	return string(x) == string(y)
}

// An FTP test scheduled onto a far week that is already pre-filled takes that
// day's plan-made session's place, and only that day's.
func TestAnFTPTestReplacesThatDaysPreFilledSession(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 250)
	g := h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14","priority":"A"}`)
	h.srv.WaitForBackground()
	thisMon, _ := weekBounds(h.now)
	far := thisMon.AddDate(0, 0, 7*8)
	week := h.week(t, far)
	if len(week) != 4 {
		t.Fatalf("far week has %d sessions, want 4", len(week))
	}
	day := week[1].Date

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/tests/ftp", `{"protocol":"ramp","date":"`+day+`"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("schedule test: status %d", resp.StatusCode)
	}

	after := h.week(t, far)
	if len(after) != 4 {
		t.Fatalf("far week has %d sessions after scheduling a test, want 4", len(after))
	}
	for _, w := range after {
		if w.Date == day && (w.TestProtocol == "" || w.GoalID != g.ID) {
			t.Errorf("the test day holds %+v, want the FTP test", w)
		}
		if w.Date != day && w.TestProtocol != "" {
			t.Errorf("a test appeared on %s", w.Date)
		}
	}
}

// Moving the event earlier clears the sessions the old plan put beyond the new
// end, but only the untouched ones: a session the rider moved or edited stays.
func TestMovingTheEventEarlierClearsUntouchedSessionsBeyondTheNewEnd(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14"}`) // 28 weeks, event in week 28
	h.srv.WaitForBackground()
	h.backdate(t)

	thisMon, _ := weekBounds(h.now)
	weekN := func(n int) time.Time { return thisMon.AddDate(0, 0, 7*(n-1)) }
	// Event in week 20 (n counts this week as 1): the Wednesday of that week.
	newEvent := weekN(20).AddDate(0, 0, 2).Format("2006-01-02")
	lastKept := weekN(20).AddDate(0, 0, 6).Format("2006-01-02")

	w24 := h.week(t, weekN(24))
	if len(w24) != 4 {
		t.Fatalf("week 24 has %d sessions, want 4", len(w24))
	}
	moved, edited := w24[0], w24[1]
	monday := weekN(24).Format("2006-01-02")
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+moved.ID, `{"date":"`+monday+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("move: status %d", resp.StatusCode)
	}
	newName := "My own name"
	if _, err := h.store.UpdateWorkout(ctx, edited.ID, workout.UpdateWorkoutRequest{Name: &newName}); err != nil {
		t.Fatal(err)
	}

	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"eventDate":"`+newEvent+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("update goal: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()

	var beyond []workout.Workout
	for _, w := range h.workouts(t) {
		if w.Date > lastKept {
			beyond = append(beyond, w)
		}
	}
	if len(beyond) != 2 {
		t.Fatalf("%d sessions remain beyond the new end, want just the moved and the edited one: %+v", len(beyond), beyond)
	}
	for _, w := range beyond {
		if w.ID != moved.ID && w.ID != edited.ID {
			t.Errorf("untouched session %s (%s) survived beyond the new end", w.ID, w.Date)
		}
	}
	// Weeks up to the new end are intact.
	for n := 1; n <= 20; n++ {
		if got := len(h.week(t, weekN(n))); got != 4 {
			t.Errorf("week %d has %d sessions, want 4", n, got)
		}
	}
	// The dropped weeks are forgotten, the kept ones are not.
	weeks, err := h.store.ScheduledWeeks(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 28; n++ {
		_, recorded := weeks[weekN(n).Format("2006-01-02")]
		if want := n <= 20; recorded != want {
			t.Errorf("week %d recorded = %v, want %v", n, recorded, want)
		}
	}
}
