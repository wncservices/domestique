package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// pushHarness is the connect harness with Training wired and the rider
// "wilant" already signed in to Garmin — what every push test starts from.
type pushHarness struct {
	*connectHarness
	training *workout.DB
}

func newPushHarness(t *testing.T) *pushHarness {
	t.Helper()
	h := newConnectHarness(t, true)
	training, err := workout.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Training = training
	h.connectGarmin("wilant")
	return &pushHarness{connectHarness: h, training: training}
}

func (h *pushHarness) newWorkout(date string) string {
	h.t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts",
		strings.Replace(exampleWorkoutBody, "2026-03-02", date, 1))
	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("create workout: status = %d", resp.StatusCode)
	}
	return decodeWorkoutOut(h.t, resp).ID
}

func (h *pushHarness) push(id string) {
	h.t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+id+"/push/garmin", "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("push: status = %d, body = %s", resp.StatusCode, readAll(h.t, resp))
	}
}

// calls is what the fake Garmin was asked to do since the last reset.
func (h *pushHarness) calls() string { return strings.Join(h.garmin.workoutCalls, "; ") }

func tomorrow() string { return time.Now().AddDate(0, 0, 1).Format("2006-01-02") }

func TestPushingTwiceNeverMakesASecondCopy(t *testing.T) {
	h := newPushHarness(t)
	id := h.newWorkout(tomorrow())

	h.push(id)
	if got := h.calls(); got != "create garmin-workout-1; schedule garmin-workout-1 "+tomorrow() {
		t.Fatalf("first push made %q, want one create and one schedule", got)
	}

	h.garmin.workoutCalls = nil
	h.push(id)
	if got := h.calls(); got != "" {
		t.Errorf("second push of an unchanged workout made %q, want no Garmin calls at all", got)
	}
	if len(h.garmin.remoteWorkouts) != 1 {
		t.Errorf("account holds %d workouts, want 1", len(h.garmin.remoteWorkouts))
	}
}

func TestEditingAPushedWorkoutUpdatesItsCopyInPlace(t *testing.T) {
	h := newPushHarness(t)
	id := h.newWorkout(tomorrow())
	h.push(id)

	h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+id, `{"name":"Renamed"}`)
	h.garmin.workoutCalls = nil
	h.push(id)

	if got := h.calls(); got != "update garmin-workout-1" {
		t.Errorf("calls = %q, want just an in-place update — same date, same copy", got)
	}
	if h.garmin.remoteWorkouts["garmin-workout-1"] != "Renamed" {
		t.Errorf("remote name = %q", h.garmin.remoteWorkouts["garmin-workout-1"])
	}
}

func TestAWorkoutTheRiderDeletedOnGarminIsSentAgain(t *testing.T) {
	h := newPushHarness(t)
	id := h.newWorkout(tomorrow())
	h.push(id)

	delete(h.garmin.remoteWorkouts, "garmin-workout-1") // deleted in Connect
	h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+id, `{"name":"Renamed"}`)
	h.garmin.workoutCalls = nil
	h.push(id)

	if got := h.calls(); !strings.Contains(got, "create garmin-workout-2") || !strings.Contains(got, "schedule garmin-workout-2") {
		t.Errorf("calls = %q, want a fresh copy created and scheduled", got)
	}
}

func TestMovingAWorkoutMovesItsCalendarEntry(t *testing.T) {
	h := newPushHarness(t)
	id := h.newWorkout(tomorrow())
	h.push(id)

	later := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+id, `{"date":"`+later+`"}`)
	h.garmin.workoutCalls = nil
	h.push(id)

	if got := h.calls(); got != "unschedule entry-1; schedule garmin-workout-1 "+later {
		t.Errorf("calls = %q, want the old entry removed and a new one made — and no update, the workout itself did not change", got)
	}
	if len(h.garmin.calendar) != 1 {
		t.Errorf("calendar = %v, want exactly one entry", h.garmin.calendar)
	}
}

func TestDeletingAWorkoutTakesItOffTheRidersGarmin(t *testing.T) {
	h := newPushHarness(t)
	id := h.newWorkout(tomorrow())
	h.push(id)

	h.garmin.workoutCalls = nil
	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/workouts/"+id, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status = %d", resp.StatusCode)
	}

	if len(h.garmin.remoteWorkouts) != 0 || len(h.garmin.calendar) != 0 {
		t.Errorf("account still holds workouts %v / calendar %v, want both empty", h.garmin.remoteWorkouts, h.garmin.calendar)
	}
}

func TestDeletingAWorkoutStillWorksWhenGarminIsUnreachable(t *testing.T) {
	h := newPushHarness(t)
	id := h.newWorkout(tomorrow())
	h.push(id)

	// The rider's copy is already gone from Connect; deleting here must not
	// care.
	delete(h.garmin.remoteWorkouts, "garmin-workout-1")
	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/workouts/"+id, "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("delete: status = %d, want 200 — Garmin's state must not block deleting our own workout", resp.StatusCode)
	}
}

func TestAutoScheduleTickPutsTheWeekOnTheWatchOnlyForRidersWhoOptedIn(t *testing.T) {
	h := newPushHarness(t)
	h.connectGarmin("other")
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	allDays := `["mon","tue","wed","thu","fri","sat","sun"]`
	h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile",
		`{"hoursPerAvailableDay":1.5,"availableDays":`+allDays+`,"autoPushWorkouts":true}`)
	h.as("other", "cyclists", http.MethodPut, "/api/training/profile",
		`{"hoursPerAvailableDay":1.5,"availableDays":`+allDays+`}`)
	for _, rider := range []string{"wilant", "other"} {
		if _, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: rider, Name: "Stay Fit"}); err != nil {
			t.Fatal(err)
		}
	}
	// A hand-built workout that was never pushed is the rider's own business.
	manual := h.newWorkout(tomorrow())

	h.srv.AutoScheduleTick(ctx)

	today := time.Now().Format("2006-01-02")
	planned, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, wk := range planned {
		if wk.GoalID != "" && wk.Date >= today {
			want++
		}
	}
	if want == 0 {
		t.Fatal("test needs at least one planned workout from today on")
	}
	if len(h.garmin.remoteWorkouts) != want {
		t.Errorf("account holds %d workouts, want %d: wilant's plan from today on, nothing of other's, not the hand-built one",
			len(h.garmin.remoteWorkouts), want)
	}
	if _, have, _ := h.training.GetPush(ctx, manual, "garmin"); have {
		t.Error("a hand-built, never-pushed workout was pushed")
	}

	// A second tick is a no-op: everything is already in step.
	h.garmin.workoutCalls = nil
	h.srv.AutoScheduleTick(ctx)
	if got := h.calls(); got != "" {
		t.Errorf("second tick made %q, want no Garmin calls", got)
	}
}

func TestAutoPushKeepsAnEditedWorkoutInStep(t *testing.T) {
	h := newPushHarness(t)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"autoPushWorkouts":true}`)

	// Pushed once by hand, so it is in step with what is on the account.
	id := h.newWorkout(tomorrow())
	h.push(id)

	h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+id, `{"name":"Renamed"}`)
	h.garmin.workoutCalls = nil
	h.srv.AutoScheduleTick(ctx)

	if got := h.calls(); got != "update garmin-workout-1" {
		t.Errorf("calls = %q, want the edit carried to the existing copy", got)
	}
}
