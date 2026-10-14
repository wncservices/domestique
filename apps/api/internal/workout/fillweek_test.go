package workout

import (
	"context"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

func weekRequests(goalID string, names ...string) []CreateWorkoutRequest {
	out := make([]CreateWorkoutRequest, 0, len(names))
	for i, n := range names {
		out = append(out, CreateWorkoutRequest{
			Rider: "wilant", GoalID: goalID, Sport: model.SportCycling, Name: n,
			Date: "2026-10-1" + string(rune('0'+i)), Description: "Generated from the periodization plan.", Steps: exampleSteps(),
		})
	}
	return out
}

// A week's sessions and the marker that says the week was filled land together
// or not at all: a failure on the third session must leave no trace, or the
// half-built week is later read as a week that was filled.
func TestFillWeekIsAllOrNothing(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			goal, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", Sport: model.SportCycling, EventDate: "2027-06-01", Priority: PriorityA})
			if err != nil {
				t.Fatal(err)
			}

			// The third request cannot be stored (no name).
			bad := weekRequests(goal.ID, "Endurance ride", "Tempo ride", "", "Long ride")
			if _, err := db.FillWeek(ctx, goal.ID, "2026-10-12", bad, false); err == nil {
				t.Fatal("FillWeek accepted a request that cannot be stored")
			}
			if all, _ := db.ListWorkouts(ctx, "wilant"); len(all) != 0 {
				t.Errorf("%d sessions survived a failed fill, want none", len(all))
			}
			if done, _ := db.WeekScheduled(ctx, goal.ID, "2026-10-12"); done {
				t.Error("a failed fill left the week marked as filled")
			}

			// The retry completes the week, and two sessions of the same name
			// in one batch still get distinct ids.
			good := weekRequests(goal.ID, "Endurance ride", "Endurance ride", "Long ride")
			created, err := db.FillWeek(ctx, goal.ID, "2026-10-12", good, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(created) != 3 || created[0].ID == created[1].ID {
				t.Fatalf("created = %+v, want three sessions with distinct ids", created)
			}
			weeks, _ := db.ScheduledWeeks(ctx, goal.ID)
			if refreshed, ok := weeks["2026-10-12"]; !ok || !refreshed {
				t.Errorf("scheduled weeks = %v, want 2026-10-12 recorded as refreshed", weeks)
			}
		})
	}
}

// DeleteGoal unlinks the workouts and forgets the filled weeks in the same
// breath as removing the goal. Goal ids are slugs of the name, so a goal made
// again under the same name must not inherit "filled" markers.
func TestDeleteGoalIsAllOrNothing(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	goal, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", Sport: model.SportCycling, EventDate: "2027-06-01", Priority: PriorityA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.FillWeek(ctx, goal.ID, "2026-10-12", weekRequests(goal.ID, "Endurance ride"), false); err != nil {
		t.Fatal(err)
	}

	// Make the last statement fail: nothing may have changed.
	if _, err := db.db.Exec(`CREATE TRIGGER no_forgetting BEFORE DELETE ON scheduled_weeks BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteGoal(ctx, goal.ID); err == nil {
		t.Fatal("DeleteGoal swallowed a failure")
	}
	if _, err := db.GetGoal(ctx, goal.ID); err != nil {
		t.Errorf("the goal was deleted although the delete failed: %v", err)
	}
	all, _ := db.ListWorkouts(ctx, "wilant")
	if len(all) != 1 || all[0].GoalID != goal.ID {
		t.Errorf("workouts = %+v, want the session still linked to the goal", all)
	}

	if _, err := db.db.Exec(`DROP TRIGGER no_forgetting`); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteGoal(ctx, goal.ID); err != nil {
		t.Fatal(err)
	}
	again, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", Sport: model.SportCycling, EventDate: "2027-06-01", Priority: PriorityA})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != goal.ID {
		t.Skipf("goal ids no longer repeat (%s, %s)", again.ID, goal.ID)
	}
	if weeks, _ := db.ScheduledWeeks(ctx, again.ID); len(weeks) != 0 {
		t.Errorf("a re-created goal inherited filled weeks: %v", weeks)
	}
}

// Once the transaction has committed the week exists. A failure to read the
// sessions back (here, the context ends right after the commit) must not turn
// into an error that makes the caller treat a stored week as unfilled.
func TestFillWeekReturnsTheCommittedWeekEvenIfTheContextEndsAfterTheCommit(t *testing.T) {
	db := openTestDB(t)
	goal, err := db.CreateGoal(t.Context(), CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", Sport: model.SportCycling, EventDate: "2027-06-01", Priority: PriorityA})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fillWeekAfterCommit = cancel
	defer func() { fillWeekAfterCommit = func() {} }()

	created, err := db.FillWeek(ctx, goal.ID, "2026-10-12", weekRequests(goal.ID, "Endurance ride", "Long ride"), false)
	if err != nil {
		t.Fatalf("FillWeek = %v after a successful commit", err)
	}
	if len(created) != 2 {
		t.Errorf("created = %d sessions, want the 2 that were committed", len(created))
	}
	if done, _ := db.WeekScheduled(t.Context(), goal.ID, "2026-10-12"); !done {
		t.Error("the week was not recorded")
	}
}
