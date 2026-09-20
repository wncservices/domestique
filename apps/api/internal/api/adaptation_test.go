package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func generated(rider, goal, name, date string, seconds float64) workout.CreateWorkoutRequest {
	return workout.CreateWorkoutRequest{
		Rider: rider, GoalID: goal, Sport: model.SportCycling, Name: name, Date: date,
		Description: scheduler.GeneratedDescription,
		Steps:       []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: seconds, Target: workout.TargetOpen}},
	}
}

func TestAMissedKeySessionIsMadeUpInPlaceOfTheNextEasyDay(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	// Thursday. The plan's Tuesday tempo was never ridden.
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC) }
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}

	goal, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"tue", "fri", "sat"},
	}); err != nil {
		t.Fatal(err)
	}
	tempo, _ := h.store.CreateWorkout(ctx, generated("wilant", goal.ID, "Tempo ride", "2026-03-24", 3600))
	easy, _ := h.store.CreateWorkout(ctx, generated("wilant", goal.ID, "Endurance ride", "2026-03-27", 3600))
	_, _ = h.store.CreateWorkout(ctx, generated("wilant", goal.ID, "Long ride", "2026-03-28", 5400))

	h.srv.AdaptWorkouts(ctx)

	moved, err := h.store.GetWorkout(ctx, tempo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Date != "2026-03-27" || !strings.Contains(moved.Description, scheduler.AdjustedMarker) {
		t.Errorf("tempo = %s %q, want moved to Friday with an explanation", moved.Date, moved.Description)
	}
	if _, err := h.store.GetWorkout(ctx, easy.ID); err == nil {
		t.Error("the easy ride whose day was taken should be gone")
	}

	// Once is enough: a second pass changes nothing.
	before, _ := h.store.ListWorkouts(ctx, "wilant")
	h.srv.AdaptWorkouts(ctx)
	after, _ := h.store.ListWorkouts(ctx, "wilant")
	if len(before) != len(after) || len(after) != 2 {
		t.Errorf("workouts before/after a second pass = %d/%d, want 2/2", len(before), len(after))
	}

	// And the next scheduling pass must not read Tuesday's gap and plan the
	// session again on top of the one that moved.
	h.srv.AutoScheduleTick(ctx)
	all, _ := h.store.ListWorkouts(ctx, "wilant")
	for _, wk := range all {
		if wk.Date == "2026-03-24" {
			t.Errorf("Tuesday was re-planned as %q — the vacated day must stay vacated", wk.Name)
		}
	}
}

func TestAHardSessionIsSwappedForAnEasyOneWhenTheRiderIsExhausted(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()

	goal, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	// Six brutal days just behind the rider: fatigue (ATL) outruns fitness
	// (CTL) by a wide margin.
	for d := 1; d <= 6; d++ {
		date := time.Now().AddDate(0, 0, -d).Format("2006-01-02")
		if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
			Rider: "wilant", Provider: "garmin", ExternalID: fmt.Sprintf("s%d", d), Sport: "cycling",
			Date: date, DurationSeconds: 5400, TrainingLoad: 300,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.RecomputeFitnessSnapshots(ctx, "wilant"); err != nil {
		t.Fatal(err)
	}

	today := time.Now().Format("2006-01-02")
	inThreeDays := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	hard, _ := h.store.CreateWorkout(ctx, generated("wilant", goal.ID, "VO2max intervals", today, 3600))
	later, _ := h.store.CreateWorkout(ctx, generated("wilant", goal.ID, "VO2max intervals", inThreeDays, 3600))

	h.srv.AdaptWorkouts(ctx)

	swapped, _ := h.store.GetWorkout(ctx, hard.ID)
	if swapped.Name != "Endurance ride" || !strings.Contains(swapped.Description, "fatigued") ||
		!strings.Contains(swapped.Description, "VO2max intervals") {
		t.Errorf("today's workout = %q / %q, want an easy ride with the reason and what it replaced", swapped.Name, swapped.Description)
	}
	if secs := workout.PlannedSeconds(swapped.Steps); secs != 2880 {
		t.Errorf("planned = %v s, want 2880: a fifth shorter than the hour it replaces", secs)
	}
	if untouched, _ := h.store.GetWorkout(ctx, later.ID); untouched.Name != "VO2max intervals" {
		t.Errorf("a session three days out is %q, want it left alone — the rider may well have recovered by then", untouched.Name)
	}
}

func TestARidersOwnWorkoutsAreNeverAdapted(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC) }

	goal, _ := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{Rider: "wilant", AvailableDays: []string{"tue", "fri"}}); err != nil {
		t.Fatal(err)
	}
	mine := generated("wilant", goal.ID, "Tempo ride", "2026-03-24", 3600)
	mine.Description = "my own session"
	own, _ := h.store.CreateWorkout(ctx, mine)
	_, _ = h.store.CreateWorkout(ctx, generated("wilant", goal.ID, "Endurance ride", "2026-03-27", 3600))

	h.srv.AdaptWorkouts(ctx)

	if wk, _ := h.store.GetWorkout(ctx, own.ID); wk.Date != "2026-03-24" || wk.Description != "my own session" {
		t.Errorf("a rider's own workout was changed to %s %q", wk.Date, wk.Description)
	}
}
