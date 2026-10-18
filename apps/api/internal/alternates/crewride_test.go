package alternates

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A crew ride row is the rider's commitment to a ride the crew set; it has no
// alternates, whatever its description says. Plannable used to rely on crew
// ride descriptions never starting with the generated prefix.
func TestACrewRideRowIsNeverPlannable(t *testing.T) {
	w := workout.Workout{
		GoalID: "g", CrewRideID: "ride-1", Description: scheduler.GeneratedDescription,
		Flags: workout.Flags{Origin: workout.OriginPlan}, Zone: workout.ZoneEndurance,
	}
	if Plannable(w) {
		t.Error("a crew ride row is plannable")
	}
	w.CrewRideID = ""
	if !Plannable(w) {
		t.Error("control: the same row without the crew ride link should be plannable")
	}
}
