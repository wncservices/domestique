package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Saving a goal fills this week once, unless the week already holds a plan-made
// session. A crew ride the rider joined (linked to the goal so its day reads as
// taken) is not one, or this week would be recorded as filled while holding
// nothing but the ride.
func TestSavingAGoalStillFillsAThisWeekThatOnlyHoldsACrewRide(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	ctx := context.Background()
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", FTPWatts: 250, HoursPerAvailableDay: 2, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	h.mustGo("wilant", s.ride) // Saturday: linked to the goal, week not filled yet
	if row, _ := h.fixedRow("wilant", s.ride); row.GoalID != goal.ID {
		t.Fatalf("setup: the ride is linked to %q, want the goal", row.GoalID)
	}

	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+goal.ID, `{"notes":"saved again"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save goal: status = %d", resp.StatusCode)
	}
	h.srv.WaitForBackground()

	if got := h.generatedIn("wilant", cpMonday, cpSunday); len(got) == 0 {
		t.Error("this week was recorded as filled because it held a crew ride, and nothing was planned")
	}
}
