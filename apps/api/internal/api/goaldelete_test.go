package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/pgtest"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func (h *seasonHarness) deleteGoal(t *testing.T, id string) {
	t.Helper()
	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/goals/"+id, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete goal: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()
}

// Deleting a goal takes its untouched sessions from today on with it; before,
// they stayed as the rider's own and the calendar kept a plan for nothing.
func TestDeletingAGoalRemovesItsUntouchedFutureSessions(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	if len(h.goalSessions(t, g.ID)) == 0 {
		t.Fatal("fixture: the goal has no sessions")
	}

	h.deleteGoal(t, g.ID)

	today := h.now.Format("2006-01-02")
	for _, w := range h.workouts(t) {
		if w.Date >= today {
			t.Errorf("session %s on %s survived the goal's deletion", w.ID, w.Date)
		}
	}
	weeks, err := h.store.ScheduledWeeks(context.Background(), g.ID)
	if err != nil || len(weeks) != 0 {
		t.Errorf("scheduled weeks = %v, %v, want none", weeks, err)
	}
}

// Changed sessions go too: a renamed future session of a deleted goal is a
// plan for nothing, and the stay-fit goal that takes the race's freed days back
// plans those days as well.
func TestDeletingAGoalRemovesItsChangedFutureSessions(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	stay, race := h.stayFitThenRace(t)
	h.backdate(t)

	thisMon, _ := weekBounds(h.now)
	edited := h.week(t, thisMon.AddDate(0, 0, 14))[0]
	newName := "My own name"
	if _, err := h.store.UpdateWorkout(ctx, edited.ID, workout.UpdateWorkoutRequest{Name: &newName}); err != nil {
		t.Fatal(err)
	}

	h.deleteGoal(t, race.ID)

	today := h.now.Format("2006-01-02")
	for _, w := range h.workouts(t) {
		if w.Name == newName {
			t.Errorf("the changed session on %s survived the goal's deletion", w.Date)
		}
	}
	for _, w := range h.workouts(t) {
		if w.GoalID == race.ID {
			t.Errorf("session %s still names the deleted goal", w.ID)
		}
	}
	// The stay-fit goal comes back on the race's freed days, without a tick.
	back := 0
	for _, w := range h.goalSessions(t, stay.ID) {
		if w.Date >= today {
			back++
		}
	}
	if back == 0 {
		t.Error("the stay-fit goal did not refill the freed days")
	}
	if d := twoOnADay(h.workouts(t), today); d != "" {
		t.Errorf("two sessions on %s", d)
	}
}

func TestDeletingAGoalWithdrawsTodaysGarminCopy(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	goal := h.scheduleSetup(250)
	today, err := h.training.CreateWorkout(ctx, api.LegacyFlagged(generated("wilant", goal.ID, "Endurance ride", replanToday, 3600)))
	if err != nil {
		t.Fatal(err)
	}
	h.push(today.ID)
	h.garmin.workoutCalls = nil

	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/goals/"+goal.ID, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete goal: status %d", resp.StatusCode)
	}
	if _, err := h.training.GetWorkout(ctx, today.ID); err == nil {
		t.Error("today's untouched session survived the goal's deletion")
	}
	if calls := h.calls(); strings.Count(calls, "delete garmin-workout-1") != 1 {
		t.Errorf("calls = %q, want today's copy removed from Garmin exactly once", calls)
	}
}

// A session ridden today and a past plan session are history: both outlive the
// goal, as the rider's own.
func TestDeletingAGoalKeepsWhatWasRiddenAndWhatIsPast(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	today := h.now.Format("2006-01-02")
	yesterday := h.now.AddDate(0, 0, -1).Format("2006-01-02")
	mk := func(date string) workout.Workout {
		w, err := h.store.CreateWorkout(ctx, api.LegacyFlagged(generated("wilant", g.ID, "Endurance ride", date, 3600)))
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	riddenToday, past := mk(today), mk(yesterday)
	h.backdate(t)
	if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden-today", Sport: string(riddenToday.Sport),
		Date: today, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}

	h.deleteGoal(t, g.ID)

	for name, id := range map[string]string{"ridden today": riddenToday.ID, "past": past.ID} {
		w, err := h.store.GetWorkout(ctx, id)
		if err != nil {
			t.Errorf("the %s session was deleted: %v", name, err)
			continue
		}
		if w.GoalID != "" {
			t.Errorf("the %s session still names goal %q", name, w.GoalID)
		}
	}
}

// With scheduling busy nothing is deleted: the goal and its sessions stay, so
// the rider's retry finds them by goal id.
func TestDeletingAGoalWhileSchedulingIsBusyChangesNothing(t *testing.T) {
	db, err := source.OpenDB(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	appSettings, err := settings.UseDB(db.Conn(), db.DSN(), nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.New(auth.Config{Mode: auth.ModeProxy, Roles: auth.RoleMapping{Rider: []string{"cyclists"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := utcNoon(2026, time.October, 7)
	srv := &api.Server{Source: db, Auth: authenticator, Training: store, Settings: appSettings,
		Clock: func() time.Time { return now }, SeasonFillRetry: 20 * time.Millisecond}
	followServerClock(srv, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Remote-User", "wilant")
		req.Header.Set("Remote-Groups", "cyclists")
		req.Header.Set("Content-Type", "application/json")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	if _, err := store.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 3, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	if resp := do(http.MethodPost, "/api/training/goals", `{"name":"Race","eventDate":"2027-04-14"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create goal: status %d", resp.StatusCode)
	}
	srv.WaitForBackground()
	goals, _ := store.ListGoals(context.Background(), "wilant")
	if len(goals) != 1 {
		t.Fatalf("goals = %v", goals)
	}
	before, _ := store.ListWorkouts(context.Background(), "wilant")

	holding, release := make(chan struct{}), make(chan struct{})
	go api.HoldSchedulingLockForTest(context.Background(), db.Conn(), func() { close(holding); <-release })
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("never took the lock")
	}
	// Held longer than the handler's wait for the lock.
	resp := do(http.MethodDelete, "/api/training/goals/"+goals[0].ID, "")
	close(release)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	after, _ := store.ListWorkouts(context.Background(), "wilant")
	if len(after) != len(before) || len(before) == 0 {
		t.Errorf("%d sessions before, %d after a refused delete", len(before), len(after))
	}
	if _, err := store.GetGoal(context.Background(), goals[0].ID); err != nil {
		t.Errorf("the goal is gone: %v", err)
	}

	// Once free, the same request succeeds.
	if resp := do(http.MethodDelete, "/api/training/goals/"+goals[0].ID, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", resp.StatusCode)
	}
}
