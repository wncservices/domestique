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

	// This used to be detectFatigue's own TSB < −30 swap, with a "fatigued
	// zone" reason baked into the adapter; that rule now lives in
	// internal/readiness (readiness.Assess's own form rule, wired in through
	// adaptRider's assessReadiness), which produces the "Swapped for an easy
	// ride" wording instead — intent preserved: a very negative, fresh form
	// swaps today's hard session for an easy one and says why.
	swapped, _ := h.store.GetWorkout(ctx, hard.ID)
	if swapped.Name != "Endurance ride" || !strings.Contains(swapped.Description, "Swapped for an easy ride") ||
		!strings.Contains(swapped.Description, "your form is") ||
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

// TestAStruggledKeySessionStepsTheNextSameZoneWorkoutDown is the server-side
// half of the spec's "Struggled -> step down": adapter.AdaptSessions
// decides which workout to step down (see internal/adapter's own test for
// that), this proves adaptRider/applyStepDown actually rebuild it as the
// rung one level lower on the same ladder, same date and goal, via
// workoutlib — not just leave a Change unapplied.
func TestAStruggledKeySessionStepsTheNextSameZoneWorkoutDown(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) } // Thursday

	goal, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}

	struggledWorkout, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal.ID, Sport: model.SportCycling, Name: "Threshold 3x12",
		Date: "2026-03-17", Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneThreshold, Level: 5,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// ListAnalyses (which adaptRider reads from) joins session_analyses to
	// completed_sessions for its date — an analysis with no matching
	// completed session behind it is invisible to that join, so the ride
	// itself has to be recorded too, not just its analysis.
	sess, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "6400", Sport: "cycling",
		Date: "2026-03-17", DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: sess.ID, Rider: "wilant", WorkoutID: struggledWorkout.ID, Outcome: "struggled",
	}); err != nil {
		t.Fatal(err)
	}

	nextThreshold, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal.ID, Sport: model.SportCycling, Name: "Threshold 3x8",
		Date: "2026-03-21", Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneThreshold, Level: 4,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3000}},
	})
	if err != nil {
		t.Fatal(err)
	}

	h.srv.AdaptWorkouts(ctx)

	stepped, err := h.store.GetWorkout(ctx, nextThreshold.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stepped.Level != 3 {
		t.Errorf("stepped-down workout level = %v, want 3 (one rung below its own level 4)", stepped.Level)
	}
	if stepped.Date != "2026-03-21" || stepped.GoalID != goal.ID {
		t.Errorf("stepped-down workout date/goal = %s/%s, want unchanged (2026-03-21/%s)", stepped.Date, stepped.GoalID, goal.ID)
	}
	if !strings.Contains(stepped.Description, scheduler.AdjustedMarker) || !strings.Contains(stepped.Description, "Stepped down") {
		t.Errorf("description = %q, want the adjustment marker and a step-down reason", stepped.Description)
	}
	if scheduler.IsGenerated(stepped) {
		t.Error("a stepped-down workout must not be eligible for a second automatic adjustment")
	}
	// Round 2's fix: the rider-visible text — everything after
	// scheduler.AdjustedMarker, the same slice the frontend's own
	// adjustmentNote() takes — must be just the reason, never the
	// step-down source marker or the struggled workout's own id. The
	// source note has to sit *before* the marker in the description for
	// that to hold, while hasStepDownSource can still find it anywhere.
	if visible := riderVisibleAdjustmentText(stepped.Description); strings.Contains(visible, "source:") || strings.Contains(visible, struggledWorkout.ID) {
		t.Errorf("rider-visible adjustment text = %q, want no step-down source marker or workout id leaking into it", visible)
	}

	// The struggled session's own workout is untouched — only the *next*
	// workout in its zone steps down, never the one that was actually rated.
	untouched, err := h.store.GetWorkout(ctx, struggledWorkout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Level != 5 || untouched.Name != "Threshold 3x12" {
		t.Errorf("the struggled session's own workout changed: %+v", untouched)
	}
}

// riderVisibleAdjustmentText mirrors apps/web/src/utils/workoutMath.ts's own
// adjustmentNote(): everything after scheduler.AdjustedMarker, trimmed —
// exactly what a rider actually reads for "why was this changed." Kept here
// rather than imported since it lives in the frontend, but the slicing
// logic has to match byte for byte for this test to mean anything.
func riderVisibleAdjustmentText(description string) string {
	at := strings.Index(description, scheduler.AdjustedMarker)
	if at < 0 {
		return ""
	}
	return strings.TrimSpace(description[at+len(scheduler.AdjustedMarker):])
}

// TestASecondAdaptationPassDoesNotStepDownASecondWorkoutFromTheSameStruggle
// is round 1's important fix (review finding #2): AutoScheduleTick runs
// every 30 minutes, and a struggled session's own analysis never changes
// between runs. With two untouched same-zone workouts in the 7-day window,
// a second AdaptWorkouts pass must not see the very same struggle again and
// step down the second one too, now that the first is no longer
// IsGenerated — exactly one step-down per struggle, ever, not one per pass
// until every same-zone candidate is used up.
func TestASecondAdaptationPassDoesNotStepDownASecondWorkoutFromTheSameStruggle(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) } // Thursday

	goal, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}

	struggledWorkout, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal.ID, Sport: model.SportCycling, Name: "Threshold 3x12",
		Date: "2026-03-17", Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneThreshold, Level: 5,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "6500", Sport: "cycling",
		Date: "2026-03-17", DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: sess.ID, Rider: "wilant", WorkoutID: struggledWorkout.ID, Outcome: "struggled",
	}); err != nil {
		t.Fatal(err)
	}

	// Two eligible same-zone, same-window targets — exactly the shape that
	// exposed the bug: stepDownTarget always picks "the next untouched one,"
	// so once the first is touched a naive re-run just advances to the
	// second.
	firstTarget, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal.ID, Sport: model.SportCycling, Name: "Threshold 3x8",
		Date: "2026-03-20", Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneThreshold, Level: 4,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondTarget, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal.ID, Sport: model.SportCycling, Name: "Threshold 3x6",
		Date: "2026-03-22", Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneThreshold, Level: 4,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 2400}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// First pass: steps down exactly the first (earliest) target.
	h.srv.AdaptWorkouts(ctx)
	afterFirst, err := h.store.GetWorkout(ctx, firstTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.Level != 3 || scheduler.IsGenerated(afterFirst) {
		t.Fatalf("first target after pass 1 = %+v, want stepped down to level 3 and no longer generated", afterFirst)
	}
	stillGenerated, err := h.store.GetWorkout(ctx, secondTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillGenerated.Level != 4 || !scheduler.IsGenerated(stillGenerated) {
		t.Fatalf("second target after pass 1 = %+v, want untouched", stillGenerated)
	}

	// Second pass, same struggle, nothing new synced: the second target must
	// stay untouched — this is the regression the fix guards.
	h.srv.AdaptWorkouts(ctx)
	afterSecondPass, err := h.store.GetWorkout(ctx, secondTarget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSecondPass.Level != 4 || !scheduler.IsGenerated(afterSecondPass) {
		t.Errorf("second target after pass 2 = %+v, want still untouched — one struggle steps down exactly one workout, ever", afterSecondPass)
	}
}

// TestAMissedSessionRescheduledOntoAStepDownCandidateIsNotAlsoSteppedDown is
// the end-to-end half of the final review's fix (adapter.AdaptSessions'
// internal/adapter test covers the pure decision; this proves adaptRider's
// own defensive first-Change-wins guard, and the fix in stepDownTarget
// itself, hold together through the real store): a missed key session that
// gets rescheduled this same pass would — before the fix — also have been
// picked up as stepDownTarget's own "next untouched same-zone workout after
// a struggled session" candidate, since its still-missed original date
// already sits after the struggled source and in its zone. It must come out
// of one AdaptWorkouts pass moved, at its original level, never stepped
// down.
func TestAMissedSessionRescheduledOntoAStepDownCandidateIsNotAlsoSteppedDown(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) } // Thursday

	goal, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"mon", "tue", "fri"},
	}); err != nil {
		t.Fatal(err)
	}

	struggledWorkout, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal.ID, Sport: model.SportCycling, Name: "Threshold 3x12",
		Date: "2026-03-16", Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneThreshold, Level: 5,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "7000", Sport: "cycling",
		Date: "2026-03-16", DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: sess.ID, Rider: "wilant", WorkoutID: struggledWorkout.ID, Outcome: "struggled",
	}); err != nil {
		t.Fatal(err)
	}

	// Missed: Tuesday, same zone as the struggle, within the step-down
	// window, dated after the struggled session, and never ridden — every
	// condition stepDownTarget's own candidate search checks, on top of
	// being this week's own missed key session.
	missed, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal.ID, Sport: model.SportCycling, Name: "Threshold 3x8",
		Date: "2026-03-17", Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneThreshold, Level: 4,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3000}},
	})
	if err != nil {
		t.Fatal(err)
	}

	h.srv.AdaptWorkouts(ctx)

	after, err := h.store.GetWorkout(ctx, missed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Date != "2026-03-20" {
		t.Errorf("missed session date = %q, want moved to Friday 2026-03-20", after.Date)
	}
	if after.Level != 4 {
		t.Errorf("missed session level = %v, want unchanged at 4 — it was rescheduled, not stepped down", after.Level)
	}
	if !strings.Contains(after.Description, "missed") || strings.Contains(after.Description, "Stepped down") {
		t.Errorf("description = %q, want the missed-session reason and no step-down text", after.Description)
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
