package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// autoScheduleHarness wires Training and Settings on the same underlying
// connection, matching main.go's real wiring (workout.UseDB(src.Conn(),
// src.DSN())) — AutoScheduleTick's own advisory lock only means something
// when Training and the connection dbConn() locks against are actually the
// same database.
type autoScheduleHarness struct {
	t        *testing.T
	client   *http.Client
	base     string
	store    *workout.DB
	settings *settings.Store
	srv      *api.Server
}

func newAutoScheduleHarness(t *testing.T) *autoScheduleHarness {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	trainingStore, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	// No encryption key: flags live in the plain flags table, not the
	// encrypted settings one — see AGENTS.md's own Settings section.
	appSettings, err := settings.UseDB(db.Conn(), db.DSN(), nil)
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := auth.New(auth.Config{
		Mode:  auth.ModeProxy,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{Source: db, Auth: authenticator, Training: trainingStore, Settings: appSettings}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	return &autoScheduleHarness{t: t, client: server.Client(), base: server.URL, store: trainingStore, settings: appSettings, srv: srv}
}

func (h *autoScheduleHarness) as(user, groups, method, path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Remote-User", user)
	req.Header.Set("Remote-Groups", groups)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// A disabled deployment must make zero changes, not merely skip acting on
// what it found — the same property TestAutoImportTickDoesNothingWhenDisabled
// checks for the route-import poller.
func TestAutoScheduleTickDoesNothingWhenDisabled(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()

	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{
		Rider: "wilant", Name: "Race Day", EventDate: time.Now().AddDate(0, 0, 70).Format("2006-01-02"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	// auto-schedule's flag defaults off — never explicitly enabled here.

	h.srv.AutoScheduleTick(ctx)

	workouts, err := h.store.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(workouts) != 0 {
		t.Fatalf("workouts = %+v, want none — auto-schedule is off", workouts)
	}
}

// The heart of the feature: every rider with a goal and a filled-in
// profile gets their current week scheduled, unattended, and a second tick
// does not duplicate what the first one already created.
func TestAutoScheduleTickSchedulesEveryRidersCurrentWeek(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}

	eventDate := time.Now().AddDate(0, 0, 70).Format("2006-01-02")
	for _, rider := range []string{"wilant", "other"} {
		if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{
			Rider: rider, Name: "Race Day", EventDate: eventDate,
		}); err != nil {
			t.Fatalf("create goal for %s: %v", rider, err)
		}
		if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
			Rider: rider, HoursPerAvailableDay: 1.5, AvailableDays: []string{"tue", "thu", "sat", "sun"},
		}); err != nil {
			t.Fatalf("save profile for %s: %v", rider, err)
		}
	}

	h.srv.AutoScheduleTick(ctx)

	for _, rider := range []string{"wilant", "other"} {
		workouts, err := h.store.ListWorkouts(ctx, rider)
		if err != nil {
			t.Fatal(err)
		}
		if len(workouts) != 4 {
			t.Errorf("%s: workouts = %d, want 4 (one per available day)", rider, len(workouts))
		}
	}

	// A second tick must not double the workouts already created.
	h.srv.AutoScheduleTick(ctx)
	workouts, err := h.store.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(workouts) != 4 {
		t.Errorf("after a second tick: workouts = %d, want still 4 (no duplicates)", len(workouts))
	}
}

// One rider's goal outliving its own event date must not stop every other
// rider's goal from being scheduled — the same "one bad route never aborts
// a run" rule AGENTS.md states for the route library, applied here.
func TestAutoScheduleTickSkipsAPastGoalWithoutBlockingOthers(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}

	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{
		Rider: "stale", Name: "Long Past", EventDate: "2020-01-01",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{
		Rider: "wilant", Name: "Race Day", EventDate: time.Now().AddDate(0, 0, 70).Format("2006-01-02"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(ctx)

	workouts, err := h.store.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(workouts) != 4 {
		t.Errorf("wilant's workouts = %d, want 4 — a different rider's stale goal must not block this", len(workouts))
	}
}

func TestHandleAutoScheduleGetAndSet(t *testing.T) {
	h := newAutoScheduleHarness(t)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/settings/auto-schedule", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status = %d, want 200", resp.StatusCode)
	}

	// A rider (not admin) may not flip it.
	resp = h.as("wilant", "cyclists", http.MethodPut, "/api/settings/auto-schedule", `{"enabled":true}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rider set: status = %d, want 403", resp.StatusCode)
	}

	resp = h.as("admin", "admins", http.MethodPut, "/api/settings/auto-schedule", `{"enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin set: status = %d, want 200", resp.StatusCode)
	}

	enabled, err := h.settings.Flag(api.FlagAutoSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("flag not actually set")
	}
}

// A rider with no race on the calendar still trains: a goal with no event
// date gets a rolling general-fitness plan and is scheduled like any other.
func TestAutoScheduleTickSchedulesAGoalWithNoEventDate(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"tue", "thu", "sat"},
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(ctx)

	workouts, err := h.store.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(workouts) != 3 {
		t.Errorf("workouts = %d, want 3 — one per available day from the rolling plan", len(workouts))
	}
}

// A rider with both a race and a general-fitness goal must not get two
// sessions on the same day. The dated goal is visited first and keeps the
// day; the undated one fills nothing that is already taken.
func TestAutoScheduleTickNeverDoubleBooksTwoGoals(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	// Created undated-first on purpose: order of creation must not matter.
	undated, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	dated, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{
		Rider: "wilant", Name: "Race Day", EventDate: time.Now().AddDate(0, 0, 70).Format("2006-01-02"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(ctx)

	workouts, err := h.store.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(workouts) != 4 {
		t.Fatalf("workouts = %d, want 4 — two goals must share one week, not double it", len(workouts))
	}
	for _, wk := range workouts {
		if wk.GoalID != dated.ID {
			t.Errorf("workout %q belongs to goal %q, want the dated goal %q (not %q)", wk.Name, wk.GoalID, dated.ID, undated.ID)
		}
	}
}
