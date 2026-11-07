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
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/pgtest"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A sport change that meets the scheduling lock held must not leave the old
// sport's plan behind: the rebuild retries in the background until it gets the
// lock, then the season fills as new.
func TestChangingAGoalsSportWhileSchedulingIsBusyRebuildsOnceItFrees(t *testing.T) {
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
	goals, err := store.ListGoals(context.Background(), "wilant")
	if err != nil || len(goals) != 1 {
		t.Fatalf("goals = %v, %v", goals, err)
	}
	gid := goals[0].ID

	holding, release := make(chan struct{}), make(chan struct{})
	go api.HoldSchedulingLockForTest(context.Background(), db.Conn(), func() { close(holding); <-release })
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("never took the lock")
	}
	if resp := do(http.MethodPatch, "/api/training/goals/"+gid, `{"sport":"running"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("update goal: status %d", resp.StatusCode)
	}
	close(release)
	srv.WaitForBackground()

	all, err := store.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	future := 0
	for _, w := range all {
		if w.GoalID != gid || w.Date < "2026-10-07" {
			continue
		}
		future++
		if w.Sport != model.SportRunning {
			t.Errorf("session on %s is %s after the lock freed, want running", w.Date, w.Sport)
		}
	}
	if future == 0 {
		t.Fatal("no sessions from today on")
	}
}

// A race added while scheduling is busy still takes the stay-fit goal's days
// once the lock frees: the takeover retries in the background.
func TestARaceAddedWhileSchedulingIsBusyTakesOverOnceItFrees(t *testing.T) {
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
		Clock: func() time.Time { return now }, SeasonFillRetry: 150 * time.Millisecond}
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
	if resp := do(http.MethodPost, "/api/training/goals", `{"name":"Stay fit"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create goal: status %d", resp.StatusCode)
	}
	srv.WaitForBackground()
	goals, err := store.ListGoals(context.Background(), "wilant")
	if err != nil || len(goals) != 1 {
		t.Fatalf("goals = %v, %v", goals, err)
	}
	stayID := goals[0].ID

	holding, release := make(chan struct{}), make(chan struct{})
	go api.HoldSchedulingLockForTest(context.Background(), db.Conn(), func() { close(holding); <-release })
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("never took the lock")
	}
	if resp := do(http.MethodPost, "/api/training/goals", `{"name":"Race","eventDate":"2027-02-24"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create race: status %d", resp.StatusCode)
	}
	close(release)
	srv.WaitForBackground()

	all, err := store.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	race, seen := 0, map[string]bool{}
	for _, w := range all {
		if w.Date < "2026-10-07" {
			continue
		}
		if w.GoalID == stayID && w.Date <= "2027-02-24" {
			t.Errorf("stay-fit session on %s survived the takeover", w.Date)
		}
		if w.GoalID != stayID {
			race++
		}
		if seen[w.Date] {
			t.Errorf("two sessions on %s", w.Date)
		}
		seen[w.Date] = true
	}
	if race == 0 {
		t.Fatal("the race has no sessions once the lock freed")
	}
}

// Taking a deleted session off the account is what keeps a cycling workout
// from staying on the head unit after the plan moved to running.
func TestChangingAGoalsSportTakesTheRemovedSessionsOffGarmin(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), false)
	ctx := context.Background()
	h.srv.AutoScheduleTick(ctx)
	h.srv.WaitForBackground()
	goals, err := h.training.ListGoals(ctx, "wilant")
	if err != nil || len(goals) != 1 {
		t.Fatalf("goals = %v, %v", goals, err)
	}
	future := h.workoutsOn(t, "2026-10-09")
	if len(future) != 1 {
		t.Fatalf("need one plan-made session on the 9th, have %d", len(future))
	}
	wk := future[0]
	remote := "legacy-" + wk.ID
	if h.garmin.remoteWorkouts == nil {
		h.garmin.remoteWorkouts = map[string]string{}
	}
	h.garmin.remoteWorkouts[remote] = wk.Name
	if err := h.training.SavePush(ctx, workout.Push{
		WorkoutID: wk.ID, Provider: "garmin", RemoteID: remote, ScheduledDate: wk.Date,
		ContentHash: workout.ContentHash(wk), Origin: workout.PushOriginManual,
	}); err != nil {
		t.Fatal(err)
	}

	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+goals[0].ID, `{"sport":"running"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("update goal: status %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()

	if !strings.Contains(h.calls(), "delete "+remote) {
		t.Errorf("calls = %q, want the removed session's copy deleted", h.calls())
	}
}
