package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestPatchingAGoalWithABadFieldIs422AndChangesNothing(t *testing.T) {
	cases := map[string]string{
		"unknown sport":      `{"sport":"swimming"}`,
		"empty sport":        `{"sport":""}`,
		"unknown priority":   `{"priority":"D"}`,
		"lowercase priority": `{"priority":"a"}`,
		"malformed date":     `{"eventDate":"next spring"}`,
		"impossible date":    `{"eventDate":"2027-02-30"}`,
		"wrong date layout":  `{"eventDate":"14-04-2027"}`,
		"negative distance":  `{"targetDistanceM":-1}`,
		"negative elevation": `{"targetElevationM":-5}`,
		"empty name":         `{"name":"  "}`,
		"good field + bad":   `{"notes":"changed","priority":"Z"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h := newReplanHarness(t)
			ctx := context.Background()
			goal := h.scheduleSetup(250)

			resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+goal.ID, body)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", resp.StatusCode)
			}
			after, err := h.training.GetGoal(ctx, goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after != goal {
				t.Errorf("goal changed by a refused edit:\n before %+v\n after  %+v", goal, after)
			}
		})
	}
}

func TestPatchingAGoalWithGoodFieldsStillWorks(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.scheduleSetup(250)
	body := `{"name":"Renamed","priority":"C","eventDate":"2027-04-14","targetDistanceM":0,"notes":"x"}`
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+goal.ID, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+goal.ID, `{"eventDate":""}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("clearing the date: status = %d, want 200", resp.StatusCode)
	}
	h.srv.WaitForBackground()
}

// today's cycling session is on the head unit; the rider switches the goal to
// running. The tick is not run: the request itself swaps the copy.
func (h *pushHarness) todayPushedGoal(autoPush bool) (workout.Goal, workout.Workout) {
	h.t.Helper()
	ctx := context.Background()
	goal := h.scheduleSetup(250)
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", FTPWatts: 250, HoursPerAvailableDay: 1.5, AvailableDays: []string{"wed", "fri", "sat"},
		AutoPushWorkouts: autoPush,
	}); err != nil {
		h.t.Fatal(err)
	}
	today, err := h.training.CreateWorkout(ctx, api.LegacyFlagged(generated("wilant", goal.ID, "Endurance ride", replanToday, 3600)))
	if err != nil {
		h.t.Fatal(err)
	}
	h.push(today.ID)
	h.garmin.workoutCalls = nil
	return goal, today
}

func TestASportChangeWithTodayPushedWithdrawsTheCopyAndPushesTheNewSession(t *testing.T) {
	h := newReplanHarness(t)
	goal, today := h.todayPushedGoal(true)

	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+goal.ID, `{"sport":"running"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	h.srv.WaitForBackground()

	calls := h.calls()
	if strings.Count(calls, "delete garmin-workout-1") != 1 {
		t.Errorf("calls = %q, want the old copy deleted once", calls)
	}
	if !strings.Contains(calls, "create garmin-workout-2") || !strings.Contains(calls, "schedule garmin-workout-2 "+replanToday) {
		t.Errorf("calls = %q, want the new session created and scheduled for today in the same request", calls)
	}
	if h.garmin.pushedWorkoutSport != "running" {
		t.Errorf("pushed sport = %q, want running", h.garmin.pushedWorkoutSport)
	}
	if _, err := h.training.GetWorkout(context.Background(), today.ID); err == nil {
		t.Error("the old cycling session survived the sport change")
	}
}

func TestASportChangeWithAutoPushOffWithdrawsTheCopyAndPushesNothing(t *testing.T) {
	h := newReplanHarness(t)
	goal, _ := h.todayPushedGoal(false)

	if resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+goal.ID, `{"sport":"running"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	h.srv.WaitForBackground()

	calls := h.calls()
	if strings.Count(calls, "delete garmin-workout-1") != 1 {
		t.Errorf("calls = %q, want the old copy deleted once", calls)
	}
	if strings.Contains(calls, "create ") || strings.Contains(calls, "; schedule ") || strings.HasPrefix(calls, "schedule ") {
		t.Errorf("calls = %q, want nothing new pushed with auto-push off", calls)
	}
}
