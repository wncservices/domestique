package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// onceHarness is a rider with a dated goal and auto-schedule on, on a clock
// the test moves.
type onceHarness struct {
	*autoScheduleHarness
	now  time.Time
	goal workout.Goal
}

func newOnceHarness(t *testing.T) *onceHarness {
	t.Helper()
	h := &onceHarness{autoScheduleHarness: newAutoScheduleHarness(t), now: utcNoon(2026, time.October, 7)} // a Wednesday
	h.srv.Clock = func() time.Time { return h.now }
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.goal = seedWeeksRider(t, h.autoScheduleHarness, "wilant")
	return h
}

func (h *onceHarness) week(t *testing.T, monday time.Time) []workout.Workout {
	t.Helper()
	all, err := h.store.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	return datesIn(t, all, monday, monday.AddDate(0, 0, 6))
}

// A session the rider deleted from next week is gone for good: it is not
// refilled when the week arrives, because it is not a gap the tick may fill.
func TestDeletedNextWeekSessionIsNotRefilledWhenItBecomesThisWeek(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	_, nextMon := weekBounds(h.now)

	h.srv.AutoScheduleTick(ctx)
	next := h.week(t, nextMon)
	if len(next) < 2 {
		t.Fatalf("next week workouts = %d, want at least 2", len(next))
	}
	if err := h.store.DeleteWorkout(ctx, next[0].ID); err != nil {
		t.Fatal(err)
	}

	h.now = utcNoon(2026, time.October, 12) // that Monday: next week is now this week
	h.srv.AutoScheduleTick(ctx)
	h.srv.AutoScheduleTick(ctx)

	if got := len(h.week(t, nextMon)); got != len(next)-1 {
		t.Errorf("the week has %d workouts after rolling over, want %d — the deleted session must stay deleted", got, len(next)-1)
	}
}

// The same holds inside the current week: a later tick is not a top-up.
func TestDeletedCurrentWeekSessionIsNotRefilledByTheNextTick(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	thisMon, _ := weekBounds(h.now)

	h.srv.AutoScheduleTick(ctx)
	this := h.week(t, thisMon)
	if len(this) < 2 {
		t.Fatalf("this week workouts = %d, want at least 2", len(this))
	}
	if err := h.store.DeleteWorkout(ctx, this[len(this)-1].ID); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(ctx)

	if got := len(h.week(t, thisMon)); got != len(this)-1 {
		t.Errorf("this week has %d workouts, want %d — the tick refilled a session the rider deleted", got, len(this)-1)
	}
}

// A next-week session the rider rewrote and moved to another day is theirs:
// rolling over neither restores the day it left nor touches the session.
func TestHandEditedNextWeekSessionSurvivesRollover(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	_, nextMon := weekBounds(h.now)

	h.srv.AutoScheduleTick(ctx)
	next := h.week(t, nextMon)
	edited := next[0]
	wednesday := nextMon.AddDate(0, 0, 2).Format("2006-01-02") // not one of the plan's days
	desc := "My own session, my own day"
	if _, err := h.store.UpdateWorkout(ctx, edited.ID, workout.UpdateWorkoutRequest{Description: &desc, Date: &wednesday}); err != nil {
		t.Fatal(err)
	}

	h.now = utcNoon(2026, time.October, 12)
	h.srv.AutoScheduleTick(ctx)

	after := h.week(t, nextMon)
	if len(after) != len(next) {
		t.Errorf("the week has %d workouts after rolling over, want %d — the day the session left was refilled", len(after), len(next))
	}
	got, err := h.store.GetWorkout(ctx, edited.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != desc || got.Date != wednesday {
		t.Errorf("the rider's session was changed: %+v", got)
	}
}

// A week the rider emptied completely stays empty under the tick, but the
// Fill button — an explicit ask — refills it.
func TestRiderClearedWeekIsNotRefilledByTheTickButFillRefillsIt(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	_, nextMon := weekBounds(h.now)

	h.srv.AutoScheduleTick(ctx)
	next := h.week(t, nextMon)
	for _, w := range next {
		if err := h.store.DeleteWorkout(ctx, w.ID); err != nil {
			t.Fatal(err)
		}
	}

	h.srv.AutoScheduleTick(ctx)
	h.now = utcNoon(2026, time.October, 12)
	h.srv.AutoScheduleTick(ctx)
	if got := len(h.week(t, nextMon)); got != 0 {
		t.Fatalf("the tick refilled a week the rider cleared: %d workouts", got)
	}

	resp := h.as("wilant", "cyclists", http.MethodPost,
		"/api/training/goals/"+h.goal.ID+"/schedule", `{"weekStart":"`+nextMon.Format("2006-01-02")+`"}`)
	var out scheduleResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(out.Created) != len(next) {
		t.Errorf("fill: status %d, created %d, want 200 and %d", resp.StatusCode, len(out.Created), len(next))
	}
}

// A deployment that predates scheduled_weeks has weeks already filled and no
// rows for them. Those weeks count as filled: the tick leaves the gaps alone.
func TestAWeekWithGoalWorkoutsAndNoRecordIsNotTopUp(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	thisMon, nextMon := weekBounds(h.now)

	// One session in this week, tagged with the goal, as an old tick left it.
	if _, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: h.goal.ID, Name: "Old plan session",
		Date:        thisMon.AddDate(0, 0, 1).Format("2006-01-02"),
		Description: "Generated by your plan.",
		Steps: []workout.WorkoutStep{
			{Name: "Ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetOpen},
		},
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(ctx)

	if got := len(h.week(t, thisMon)); got != 1 {
		t.Errorf("this week has %d workouts, want the 1 that was there — a week with goal workouts and no record is not a gap to fill", got)
	}
	if got := len(h.week(t, nextMon)); got == 0 {
		t.Error("next week, which had nothing, was not filled")
	}
}
