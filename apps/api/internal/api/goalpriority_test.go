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
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/pgtest"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// priorityHarness is a race 70 days out (a Saturday-ish event: whatever day
// h.now+70 lands on) whose event week is its taper under priority A.
func priorityRace(t *testing.T, priority string) (*seasonHarness, workout.Goal, time.Time) {
	t.Helper()
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	// The Sunday of the tenth week, so the event week is a whole week of days.
	event := periodization.MondayOf(h.now).AddDate(0, 0, 70+6)
	g := h.createGoal(t, `{"name":"Race","priority":"`+priority+`","eventDate":"`+event.Format("2006-01-02")+`"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	return h, g, periodization.MondayOf(event)
}

// A taper week asks for far less than the week before it.
func (h *seasonHarness) eventWeekShare(t *testing.T, goalID string, eventMonday time.Time) float64 {
	t.Helper()
	before := h.weekSeconds(t, goalID, eventMonday.AddDate(0, 0, -7))
	if before == 0 {
		t.Fatal("the week before the event holds no sessions")
	}
	return h.weekSeconds(t, goalID, eventMonday) / before
}

func TestChangingPriorityToCRemovesTheTaper(t *testing.T) {
	h, g, eventMonday := priorityRace(t, "A")
	if share := h.eventWeekShare(t, g.ID, eventMonday); share > 0.7 {
		t.Fatalf("priority A event week is %.2f of the week before, want a taper", share)
	}
	anchor := h.goal(t, g.ID).PlanAnchor

	h.patchGoal(t, g.ID, `{"priority":"C"}`)

	if share := h.eventWeekShare(t, g.ID, eventMonday); share < 0.9 {
		t.Errorf("priority C event week is %.2f of the week before, want an ordinary week", share)
	}
	if got := h.goal(t, g.ID).PlanAnchor; got != anchor {
		t.Errorf("plan anchor %q after a priority change, was %q", got, anchor)
	}
	if d := twoOnADay(h.workouts(t), h.now.Format("2006-01-02")); d != "" {
		t.Errorf("two sessions on %s", d)
	}

	// And back: the taper returns.
	h.patchGoal(t, g.ID, `{"priority":"A"}`)
	if share := h.eventWeekShare(t, g.ID, eventMonday); share > 0.7 {
		t.Errorf("priority A again: event week is %.2f of the week before, want a taper", share)
	}
}

func TestChangingPriorityToBGivesAMiniTaper(t *testing.T) {
	h, g, eventMonday := priorityRace(t, "A")
	a := h.eventWeekShare(t, g.ID, eventMonday)
	h.patchGoal(t, g.ID, `{"priority":"B"}`)
	b := h.eventWeekShare(t, g.ID, eventMonday)
	h.patchGoal(t, g.ID, `{"priority":"C"}`)
	c := h.eventWeekShare(t, g.ID, eventMonday)

	// The same race: B only lightens its last days, so its event week sits
	// between a full taper and no taper at all.
	if b < a || b >= c {
		t.Errorf("event week shares: A %.2f, B %.2f, C %.2f, want A <= B < C", a, b, c)
	}
}

// A priority change on an undated goal has no taper to shape, so it rebuilds nothing.
func TestChangingPriorityOfAnUndatedGoalRebuildsNothing(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Keep going"}`)
	h.srv.WaitForBackground()
	h.backdate(t)
	before := map[string]string{}
	for _, w := range h.goalSessions(t, g.ID) {
		before[w.ID] = w.Date
	}
	h.patchGoal(t, g.ID, `{"priority":"C"}`)
	for _, w := range h.goalSessions(t, g.ID) {
		if _, ok := before[w.ID]; !ok && w.Date >= h.now.Format("2006-01-02") {
			t.Errorf("session %s on %s was rebuilt", w.ID, w.Date)
		}
	}
}

// A priority change that meets the scheduling lock held retries until it frees.
func TestChangingPriorityWhileSchedulingIsBusyRebuildsOnceItFrees(t *testing.T) {
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
	ctx := context.Background()
	if _, err := store.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 3, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	// Sunday 2026-12-13 ends the tenth week from Monday 2026-10-05.
	if resp := do(http.MethodPost, "/api/training/goals", `{"name":"Race","priority":"A","eventDate":"2026-12-13"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create goal: status %d", resp.StatusCode)
	}
	srv.WaitForBackground()
	goals, err := store.ListGoals(ctx, "wilant")
	if err != nil || len(goals) != 1 {
		t.Fatalf("goals = %v, %v", goals, err)
	}
	gid := goals[0].ID
	eventWeek := func() float64 {
		all, err := store.ListWorkouts(ctx, "wilant")
		if err != nil {
			t.Fatal(err)
		}
		total := 0.0
		for _, w := range all {
			if w.GoalID == gid && w.Date >= "2026-12-07" && w.Date <= "2026-12-13" {
				total += stepSeconds(w.Steps)
			}
		}
		return total
	}
	taper := eventWeek()
	if taper == 0 {
		t.Fatal("the event week holds no sessions")
	}

	holding, release := make(chan struct{}), make(chan struct{})
	go api.HoldSchedulingLockForTest(ctx, db.Conn(), func() { close(holding); <-release })
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("never took the lock")
	}
	if resp := do(http.MethodPatch, "/api/training/goals/"+gid, `{"priority":"C"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("update goal: status %d", resp.StatusCode)
	}
	close(release)
	srv.WaitForBackground()

	if got := eventWeek(); got <= taper {
		t.Errorf("event week holds %.0f s after the lock freed, want more than the taper's %.0f s", got, taper)
	}
}
