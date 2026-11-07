package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A rebuild withdraws today's Garmin copy with the session it belonged to, and
// the tick that would send the new one may be half an hour away: every
// rebuild's request pushes today's new session when the rider has auto-push on.

func (h *todayHarness) tickedToday(t *testing.T) {
	t.Helper()
	h.srv.AutoScheduleTick(context.Background())
	h.srv.WaitForBackground()
	if got := h.calendarDates(); len(got) != 1 || got[0] != "2026-10-07" {
		t.Fatalf("fixture: calendar = %v, want today only", got)
	}
	h.garmin.workoutCalls = nil
}

func TestChangingHoursPushesTodaysNewSessionWhenAutoPushIsOn(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), true)
	h.tickedToday(t)

	body := `{"hoursPerAvailableDay":2.5,"availableDays":` + allDaysJSON + `,"autoPushWorkouts":true}`
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("save profile: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()

	if !strings.Contains(h.calls(), "delete ") {
		t.Errorf("calls = %q, want the old copy withdrawn", h.calls())
	}
	if got := h.calendarDates(); len(got) != 1 || got[0] != "2026-10-07" {
		t.Errorf("calendar = %v, want today's new session on it", got)
	}
}

func TestChangingHoursPushesNothingWhenAutoPushIsOff(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), false)
	h.srv.AutoScheduleTick(context.Background())
	h.srv.WaitForBackground()
	h.garmin.workoutCalls = nil

	body := `{"hoursPerAvailableDay":2.5,"availableDays":` + allDaysJSON + `}`
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("save profile: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()

	if strings.Contains(h.calls(), "create ") {
		t.Errorf("calls = %q, want nothing sent with auto-push off", h.calls())
	}
}

func TestDeletingAGoalPushesTheSessionThatTakesItsPlace(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), true)
	h.srv.AutoScheduleTick(context.Background())
	h.srv.WaitForBackground()
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Second"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create goal: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()
	h.tickedToday(t)
	goals, err := h.training.ListGoals(context.Background(), "wilant")
	if err != nil || len(goals) != 2 {
		t.Fatalf("goals = %v, %v", goals, err)
	}

	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/goals/stay-fit", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete goal: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()

	if !strings.Contains(h.calls(), "delete ") {
		t.Errorf("calls = %q, want the deleted goal's copy withdrawn", h.calls())
	}
	today := h.workoutsOn(t, "2026-10-07")
	if len(today) != 1 || today[0].GoalID != "second" {
		t.Fatalf("today = %+v, want the second goal's session", today)
	}
	if got := h.calendarDates(); len(got) != 1 || got[0] != "2026-10-07" {
		t.Errorf("calendar = %v, want the replacement on it", got)
	}
}

func TestAddingARaceChangesTodaysCopyToTheRaceSession(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), true)
	h.tickedToday(t)

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/goals", `{"name":"Race","eventDate":"2027-02-24"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create race: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()

	today := h.workoutsOn(t, "2026-10-07")
	if len(today) != 1 || today[0].GoalID != "race" {
		t.Fatalf("today = %+v, want the race's session", today)
	}
	if got := h.calendarDates(); len(got) != 1 || got[0] != "2026-10-07" {
		t.Errorf("calendar = %v, want the race's session sent", got)
	}
}
