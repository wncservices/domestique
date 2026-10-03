package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A life event touches a rider's Garmin account in exactly one way: a session
// it removes comes off the account, as every deletion does. A session it moves
// keeps its copy: the calendar entry follows on the next push (the existing
// date-change path), and nothing is sent by the life event itself.

func (h *pushHarness) planMade(date string) string {
	h.t.Helper()
	w, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: "g", Sport: "cycling", Name: "Endurance ride", Date: date,
		Description: scheduler.GeneratedDescription, Zone: workout.ZoneEndurance,
		Steps: []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetOpen}},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return w.ID
}

func daysAhead(n int) string { return time.Now().AddDate(0, 0, n).Format("2006-01-02") }

func TestALifeEventMoveLeavesTheGarminCopyToTheNextPush(t *testing.T) {
	h := newPushHarness(t)
	ctx := context.Background()
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", AvailableDays: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	day := daysAhead(3)
	id := h.planMade(day)
	h.push(id)
	before, have, _ := h.training.GetPush(ctx, id, "garmin")
	if !have || before.RemoteID == "" {
		t.Fatal("setup: nothing was pushed")
	}

	h.garmin.workoutCalls = nil
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/life-events",
		`{"kind":"travel","startDate":"`+day+`","endDate":"`+day+`","option":"no_bike"}`)
	if resp.StatusCode >= 300 {
		t.Fatalf("life event: status %d", resp.StatusCode)
	}
	moved, err := h.training.GetWorkout(ctx, id)
	if err != nil || moved.Date == day {
		t.Fatalf("the session did not move: %+v, %v", moved, err)
	}
	if got := h.calls(); got != "" {
		t.Errorf("a life event move made Garmin calls %q, want none", got)
	}
	after, have, _ := h.training.GetPush(ctx, id, "garmin")
	if !have || after.RemoteID != before.RemoteID || after.ScheduleID != before.ScheduleID {
		t.Errorf("the push row changed: %+v -> %+v", before, after)
	}

	// The next push moves the calendar entry, as for any date change.
	h.garmin.workoutCalls = nil
	h.push(id)
	if got := h.calls(); !strings.Contains(got, "unschedule") || !strings.Contains(got, "schedule garmin-workout-1 "+moved.Date) {
		t.Errorf("the next push made %q, want the entry moved to %s", got, moved.Date)
	}
}

func TestALifeEventRemovalTakesTheSessionOffGarminAndForgetsTheCopy(t *testing.T) {
	h := newPushHarness(t)
	ctx := context.Background()
	day := daysAhead(3)
	id := h.planMade(day)
	h.push(id)

	h.garmin.workoutCalls = nil
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/life-events",
		`{"kind":"illness","startDate":"`+day+`","endDate":"`+day+`","option":"proper"}`)
	if resp.StatusCode >= 300 {
		t.Fatalf("life event: status %d", resp.StatusCode)
	}
	if _, err := h.training.GetWorkout(ctx, id); err == nil {
		t.Fatal("the session was not removed")
	}
	if len(h.garmin.remoteWorkouts) != 0 || len(h.garmin.calendar) != 0 {
		t.Errorf("the account still holds %v / %v", h.garmin.remoteWorkouts, h.garmin.calendar)
	}
	if got := h.calls(); !strings.Contains(got, "unschedule") || !strings.Contains(got, "delete") {
		t.Errorf("calls = %q, want the calendar entry and the copy removed", got)
	}
	if _, have, _ := h.training.GetPush(ctx, id, "garmin"); have {
		t.Error("the push row outlived the session")
	}
}
