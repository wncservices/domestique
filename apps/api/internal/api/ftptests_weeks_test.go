package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A test scheduled into a week the tick has not filled yet is linked to the
// goal (so the day reads as taken), and the tick must still fill the rest of
// that week: a goal workout in the week is normally the sign the week was
// already filled, and a test is not a plan-made session.
func TestAScheduledTestDoesNotStopTheTickFillingThatWeek(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	_, nextMon := weekBounds(h.now)                          // now is Wednesday 2026-10-07
	testDay := nextMon.AddDate(0, 0, 5).Format("2006-01-02") // Saturday, one of the rider's days

	if _, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "FTP Test (ramp)", GoalID: h.goal.ID, Date: testDay,
		Description: "The ramp test.", TestProtocol: "ramp",
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(ctx)

	var planMade, tests int
	for _, w := range h.week(t, nextMon) {
		switch {
		case w.TestProtocol != "":
			tests++
		case w.Date == testDay:
			t.Errorf("the tick planned %q on the test's day", w.Name)
		default:
			planMade++
		}
	}
	if tests != 1 || planMade < 2 {
		t.Errorf("next week has %d tests and %d planned sessions, want the test plus the rest of the plan", tests, planMade)
	}
	if done, err := h.store.WeekScheduled(ctx, h.goal.ID, nextMon.Format("2006-01-02")); err != nil || !done {
		t.Errorf("week recorded as filled = %v, %v", done, err)
	}

	// A second tick is not a top-up: whatever the rider does to the week now
	// is theirs, exactly as for any other filled week.
	before := len(h.week(t, nextMon))
	h.srv.AutoScheduleTick(ctx)
	if after := len(h.week(t, nextMon)); after != before {
		t.Errorf("a second tick changed the week: %d -> %d workouts", before, after)
	}
}

// The other way round: a week the tick has already filled keeps its sessions
// when a test replaces one of them, and the tick does not put it back.
func TestReplacingAPlannedSessionWithATestSticksAcrossTicks(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	_, nextMon := weekBounds(h.now)

	h.srv.AutoScheduleTick(ctx)
	next := h.week(t, nextMon)
	if len(next) < 3 {
		t.Fatalf("next week has %d sessions, want at least 3", len(next))
	}
	victim := next[1]
	if err := h.store.DeleteWorkout(ctx, victim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "FTP Test (ramp)", GoalID: h.goal.ID, Date: victim.Date,
		Description: "The ramp test.", TestProtocol: "ramp",
	}); err != nil {
		t.Fatal(err)
	}

	h.now = h.now.Add(24 * time.Hour)
	h.srv.AutoScheduleTick(ctx)

	var onDay []workout.Workout
	for _, w := range h.week(t, nextMon) {
		if w.Date == victim.Date {
			onDay = append(onDay, w)
		}
	}
	if len(onDay) != 1 || onDay[0].TestProtocol == "" {
		t.Errorf("the test's day holds %+v, want only the test", onDay)
	}
}
