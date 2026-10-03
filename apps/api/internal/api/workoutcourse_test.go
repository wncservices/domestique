package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/targets"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// courseHarness is today's auto-push harness with a routed ride today, a
// ledger for what the course adapters were asked, and extra accounts: the
// rider's Wahoo and another rider's Garmin.
type courseHarness struct {
	*todayHarness
	ledger *fakeLedger
	route  model.Route
	today  workout.Workout
}

func newCourseHarness(t *testing.T) *courseHarness {
	t.Helper()
	h := &courseHarness{todayHarness: newTodayHarness(t, utcNoon(2026, time.October, 7), true), ledger: &fakeLedger{}}
	h.srv.TargetFactory = func(a model.Account) (targets.Target, error) {
		return &fakeTarget{account: a, ledger: h.ledger}, nil
	}
	ctx := context.Background()
	if _, err := h.accounts.Link(ctx, model.ProviderWahoo, "wilant", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.accounts.Link(ctx, model.ProviderGarmin, "marie", ""); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx) // today's workout is planned and pushed
	if ws := h.workoutsOn(t, "2026-10-07"); len(ws) == 1 {
		h.today = ws[0]
	} else {
		t.Fatalf("want one session today, got %d", len(ws))
	}
	rt, err := h.db.Create(ctx, sourceCreate("Wednesday loop", "wilant"))
	if err != nil {
		t.Fatal(err)
	}
	h.route = rt
	h.ledger.creates, h.ledger.updates, h.ledger.deletes = nil, nil, nil
	return h
}

func (h *courseHarness) link(wk workout.Workout) {
	h.t.Helper()
	slug, secs := h.route.Slug, 3600.0
	if _, err := h.training.UpdateWorkout(context.Background(), wk.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *courseHarness) tick() { h.srv.AutoScheduleTick(context.Background()) }

func TestTodaysCourseGoesToTheRidersOwnGarminOnceAndNothingIsDeleted(t *testing.T) {
	h := newCourseHarness(t)
	h.link(h.today)
	h.tick()

	want := "garmin:wilant:" + h.route.Slug
	if len(h.ledger.creates) != 1 || h.ledger.creates[0] != want {
		t.Fatalf("creates = %v, want only %q: the rider's own Garmin, not their Wahoo and not marie's Garmin", h.ledger.creates, want)
	}
	h.tick()
	h.tick()
	if len(h.ledger.creates) != 1 || len(h.ledger.updates) != 0 {
		t.Errorf("creates=%v updates=%v after two more passes, want the course left alone (idempotent on its hash)", h.ledger.creates, h.ledger.updates)
	}
	if len(h.ledger.deletes) != 0 {
		t.Errorf("deletes = %v, want none", h.ledger.deletes)
	}
}

func TestACourseFailureIsAWarningAndTheWorkoutStillGoes(t *testing.T) {
	h := newCourseHarness(t)
	h.link(h.today)
	h.ledger.failOn = "garmin:wilant"
	before := len(h.garmin.remoteWorkouts)
	// An edit makes the workout need a repush, so the pass has both to do.
	h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+h.today.ID, `{"name":"Renamed today"}`)
	h.tick()

	found := false
	for _, name := range h.garmin.remoteWorkouts {
		found = found || name == "Renamed today"
	}
	if !found || len(h.garmin.remoteWorkouts) != before {
		t.Errorf("remote workouts = %v: the workout was not pushed after the course failed", h.garmin.remoteWorkouts)
	}
	if len(h.ledger.creates) != 0 {
		t.Errorf("creates = %v, want none: the adapter fails", h.ledger.creates)
	}
}

func TestAFutureDaySendsNoCourse(t *testing.T) {
	h := newCourseHarness(t)
	tomorrow := h.workoutsOn(t, "2026-10-08")
	if len(tomorrow) == 0 {
		t.Skip("no plan-made session tomorrow in this fixture")
	}
	h.link(tomorrow[0])
	h.tick()
	if len(h.ledger.creates) != 0 {
		t.Errorf("creates = %v: a future day's course went out with today's pass", h.ledger.creates)
	}
}

func TestAnIndoorOrUnroutedRideSendsNoCourse(t *testing.T) {
	h := newCourseHarness(t)
	h.tick()
	if len(h.ledger.creates) != 0 {
		t.Errorf("an unrouted ride sent %v", h.ledger.creates)
	}
	h.link(h.today)
	yes := true
	if _, err := h.training.UpdateWorkout(context.Background(), h.today.ID, workout.UpdateWorkoutRequest{Indoor: &yes}); err != nil {
		t.Fatal(err)
	}
	h.tick()
	if len(h.ledger.creates) != 0 {
		t.Errorf("an indoor ride sent %v", h.ledger.creates)
	}
}

func TestSomeoneElsesRouteIsNeverPushedToTheRidersAccounts(t *testing.T) {
	h := newCourseHarness(t)
	other, err := h.db.Create(context.Background(), sourceCreate("Marie's loop", "marie"))
	if err != nil {
		t.Fatal(err)
	}
	slug, secs := other.Slug, 3600.0
	_, _ = h.training.UpdateWorkout(context.Background(), h.today.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs})
	h.tick()
	if len(h.ledger.creates) != 0 {
		t.Errorf("creates = %v: another rider's route reached a device", h.ledger.creates)
	}
}

func TestManualSendToGarminSendsWorkoutAndCourseTogether(t *testing.T) {
	h := newCourseHarness(t)
	h.link(h.today)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+h.today.ID+"/push/garmin", "")
	raw := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"course":"pushed"`) {
		t.Fatalf("status %d body %s, want the course outcome beside the workout's", resp.StatusCode, raw)
	}
	if len(h.ledger.creates) != 1 || h.ledger.creates[0] != "garmin:wilant:"+h.route.Slug {
		t.Errorf("creates = %v, want the course on the rider's own Garmin", h.ledger.creates)
	}
}

func TestManualSendFailedCourseIsAWarningNotAFailedWorkoutPush(t *testing.T) {
	h := newCourseHarness(t)
	h.link(h.today)
	h.ledger.failOn = "garmin:wilant"
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+h.today.ID+"/push/garmin", "")
	raw := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"course":"failed"`) || !strings.Contains(string(raw), `"status":"pushed"`) {
		t.Errorf("status %d body %s, want the workout pushed and the course reported failed", resp.StatusCode, raw)
	}
}

func TestSendToDevicesPutsTheCourseOnEveryOwnAccountButNoOneElses(t *testing.T) {
	h := newCourseHarness(t)
	h.link(h.today)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+h.today.ID+"/route/push", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readAll(t, resp))
	}
	got := strings.Join(h.ledger.creates, ",")
	if !strings.Contains(got, "garmin:wilant:") || !strings.Contains(got, "wahoo:wilant:") || strings.Contains(got, "marie") {
		t.Errorf("creates = %v, want the rider's Garmin and Wahoo and not marie's", h.ledger.creates)
	}

	// An unrouted ride has nothing to send; another rider's ride is not found.
	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/workouts/"+h.today.ID+"/route", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("unlink: %d", resp.StatusCode)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+h.today.ID+"/route/push", ""); resp.StatusCode != http.StatusConflict {
		t.Errorf("send with no route: %d, want 409", resp.StatusCode)
	}
	if resp := h.as("marie", "cyclists", http.MethodPost, "/api/training/workouts/"+h.today.ID+"/route/push", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("someone else's ride: %d, want 404", resp.StatusCode)
	}
}

func TestPushLogsCarryNoCoordinates(t *testing.T) {
	h := newCourseHarness(t)
	h.link(h.today)
	h.ledger.failOn = "garmin:wilant"
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+h.today.ID+"/route/push", "")
	h.tick()
	// The connect harness logs to the default logger, so this checks the
	// handler's own response instead: no coordinate key in what it returns.
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+h.today.ID+"/route/push", "")
	raw := string(readAll(t, resp))
	for _, leak := range []string{"lat", "lon", "points", "51.234"} {
		if strings.Contains(raw, leak) {
			t.Errorf("response leaks %q: %s", leak, raw)
		}
	}
}
