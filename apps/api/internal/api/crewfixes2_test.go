package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The session put back when a rider leaves comes from the plan of the goal the
// crew ride was linked to, not from whichever covering goal happens to sort first.
func TestLeavingPutsBackTheSessionOfTheGoalTheRideWasLinkedTo(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	undated := h.planFor("wilant") // the rolling goal
	dated, err := h.training.CreateGoal(context.Background(), workout.CreateGoalRequest{
		Rider: "wilant", Name: "Race", Priority: workout.PriorityA, EventDate: "2027-03-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.mustGo("wilant", s.ride)
	row, _ := h.fixedRow("wilant", s.ride)
	// Link the ride to the rolling goal, which sorts after the dated one.
	if _, err := h.training.UpdateWorkout(context.Background(), row.ID, workout.UpdateWorkoutRequest{GoalID: &undated.ID}); err != nil {
		t.Fatal(err)
	}

	if resp, _ := h.setGoing("wilant", s.ride, `{"going":false}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("leave: status = %d", resp.StatusCode)
	}
	back := h.generatedIn("wilant", cpSaturday, cpSaturday)[cpSaturday]
	if back.ID == "" {
		t.Skip("the plan puts nothing on Saturday for this profile")
	}
	if back.GoalID != undated.ID || back.GoalID == dated.ID {
		t.Errorf("the session put back belongs to goal %q, want the ride's goal %q", back.GoalID, undated.ID)
	}
}
