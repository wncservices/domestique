package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func (h *seasonHarness) putProfile(t *testing.T, body string) {
	t.Helper()
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("save profile %s: status %d", body, resp.StatusCode)
	}
	h.srv.WaitForBackground()
}

func weekdayOf(t *testing.T, date string) time.Weekday {
	t.Helper()
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	return d.Weekday()
}

// The plan is recorded as filled, so without a rebuild the old days would stay.
func TestChangingAvailableDaysMovesUntouchedSessionsToTheNewDays(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	other := h.createGoal(t, `{"name":"Keep going"}`)
	h.srv.WaitForBackground()
	h.backdate(t)

	h.putProfile(t, `{"availableDays":["sat","sun"],"hoursPerAvailableDay":3}`)

	today := h.now.Format("2006-01-02") // a Wednesday
	thisMon, _ := weekBounds(h.now)
	var sat, sun int
	for _, w := range h.workouts(t) {
		if w.Date <= today {
			continue
		}
		switch wd := weekdayOf(t, w.Date); wd {
		case time.Tuesday, time.Thursday:
			t.Errorf("untouched session %s left on %s %s", w.ID, wd, w.Date)
		case time.Saturday:
			sat++
		case time.Sunday:
			sun++
		}
	}
	if sat == 0 || sun == 0 {
		t.Fatalf("%d Saturday and %d Sunday sessions after the change", sat, sun)
	}
	// This week is filled from today, not left empty until next Monday.
	var thisWeek int
	for _, w := range h.week(t, thisMon) {
		if w.Date > today {
			thisWeek++
		}
	}
	if thisWeek == 0 {
		t.Error("this week has no sessions after today on the new days")
	}
	// The undated goal stays out of the dated goal's span.
	for _, w := range h.goalSessions(t, other.ID) {
		if w.Date > today && w.Date <= g.EventDate {
			t.Errorf("undated goal holds %s inside the race's span", w.Date)
		}
	}
}

func TestChangingAvailableDaysKeepsTouchedSessions(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	h.backdate(t)

	thisMon, _ := weekBounds(h.now)
	var edited workout.Workout
	for _, w := range h.week(t, thisMon.AddDate(0, 0, 14)) {
		if weekdayOf(t, w.Date) == time.Tuesday {
			edited = w
		}
	}
	if edited.ID == "" {
		t.Fatal("no Tuesday session to edit")
	}
	name := "My own name"
	if _, err := h.store.UpdateWorkout(ctx, edited.ID, workout.UpdateWorkoutRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}
	var past workout.Workout
	for _, w := range h.goalSessions(t, g.ID) {
		if w.Date < h.now.Format("2006-01-02") {
			past = w
		}
	}

	h.putProfile(t, `{"availableDays":["sat","sun"],"hoursPerAvailableDay":3}`)

	found := map[string]bool{}
	for _, w := range h.workouts(t) {
		found[w.ID] = true
		if w.ID == edited.ID && w.Name != name {
			t.Errorf("edited session renamed to %q", w.Name)
		}
	}
	if !found[edited.ID] {
		t.Error("the rider's edited session was deleted")
	}
	if past.ID != "" && !found[past.ID] {
		t.Error("a past session was deleted")
	}
}

// FTP reaches the plan through refresh and recalibration; it is not a reason
// to replace sessions. Pinned so a later change is deliberate.
func TestChangingOnlyFTPDoesNotRebuild(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	ids := func() map[string]bool {
		m := map[string]bool{}
		for _, w := range h.workouts(t) {
			m[w.ID] = true
		}
		return m
	}
	before := ids()
	h.putProfile(t, `{"availableDays":["sun","sat","thu","tue"],"hoursPerAvailableDay":3,"ftpWatts":250}`)
	after := ids()
	if len(before) == 0 || len(after) != len(before) {
		t.Fatalf("%d sessions before, %d after an FTP-only save", len(before), len(after))
	}
	for id := range before {
		if !after[id] {
			t.Errorf("session %s was rebuilt by an FTP-only save", id)
		}
	}
}

// A goal made before the rider said when they can train has nothing planned.
// Saving the first days plans it, with auto-schedule off, no Fill and no tick.
func TestSavingTheFirstAvailableDaysPlansExistingGoals(t *testing.T) {
	h := newSeasonHarness(t)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	if n := len(h.workouts(t)); n != 0 {
		t.Fatalf("%d workouts planned before any days", n)
	}

	h.putProfile(t, `{"availableDays":["tue","thu","sat","sun"],"hoursPerAvailableDay":3}`)

	today := h.now.Format("2006-01-02")
	_, nextMon := weekBounds(h.now)
	var thisWeek, later int
	for _, w := range h.goalSessions(t, g.ID) {
		switch {
		case w.Date > today && w.Date < nextMon.Format("2006-01-02"):
			thisWeek++
		case w.Date >= nextMon.Format("2006-01-02"):
			later++
		}
	}
	if thisWeek == 0 || later == 0 {
		t.Errorf("%d sessions this week and %d later after the first days were saved", thisWeek, later)
	}
}

func untouchedFutureIDs(h *seasonHarness, t *testing.T) map[string]bool {
	t.Helper()
	today := h.now.Format("2006-01-02")
	m := map[string]bool{}
	for _, w := range h.workouts(t) {
		if w.Date > today {
			m[w.ID] = true
		}
	}
	return m
}

func TestChangingOnlyHoursRebuildsUntouchedSessions(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	thisMon, _ := weekBounds(h.now)
	kept := h.week(t, thisMon.AddDate(0, 0, 14))[0]
	name := "Mine"
	if _, err := h.store.UpdateWorkout(ctx, kept.ID, workout.UpdateWorkoutRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}
	before := untouchedFutureIDs(h, t)

	h.putProfile(t, `{"availableDays":["thu","tue","sun","sat"],"hoursPerAvailableDay":2}`)

	after := untouchedFutureIDs(h, t)
	if !after[kept.ID] {
		t.Error("the rider's edited session was deleted")
	}
	replaced := 0
	for id := range before {
		if id != kept.ID && !after[id] {
			replaced++
		}
	}
	if replaced == 0 {
		t.Error("no untouched session was replaced after the hours changed")
	}
}

// A save that omits hoursPerAvailableDay stores 0 (SaveProfileOn writes the
// whole row), so it is a change from the stored hours and rebuilds.
func TestSavingWithoutHoursClearsThemAndRebuilds(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	before := untouchedFutureIDs(h, t)

	h.putProfile(t, `{"availableDays":["tue","thu","sat","sun"]}`)

	p, _, err := h.store.GetProfile(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if p.HoursPerAvailableDay != 0 {
		t.Fatalf("stored hours = %v after a save that omitted them, want 0", p.HoursPerAvailableDay)
	}
	after := untouchedFutureIDs(h, t)
	same := 0
	for id := range before {
		if after[id] {
			same++
		}
	}
	if same == len(before) {
		t.Error("clearing the hours rebuilt nothing")
	}
}
