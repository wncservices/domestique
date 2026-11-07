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
