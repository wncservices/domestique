package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func untouched() workout.Workout {
	return workout.Workout{
		GoalID: "goal", Sport: model.SportCycling, Name: "Long ride",
		Description: scheduler.GeneratedDescription, CreatedAt: "2026-03-01T00:00:00Z", UpdatedAt: "2026-03-01T00:00:00Z",
		Flags: workout.Flags{Origin: workout.OriginPlan},
	}
}

// Each clause of untouchedPlanSession is the only thing standing between the
// season refresh and a rider's session for one shape of row; the table pins
// every one (a mutation that deleted any of them used to leave the suite green).
func TestUntouchedPlanSessionRefusesEachShapeOfTouchedRow(t *testing.T) {
	if !untouchedPlanSession(untouched()) {
		t.Fatal("the baseline must be untouched, or this proves nothing")
	}
	cases := map[string]func(*workout.Workout){
		// Anything the rider (or an edit) appended to the generated text.
		"extra description text": func(w *workout.Workout) { w.Description += " My note." },
		"an FTP test":            func(w *workout.Workout) { w.TestProtocol = "ramp" },
		"edited since creation":  func(w *workout.Workout) { w.UpdatedAt, w.EditedAt = "2026-03-02T00:00:00Z", "2026-03-02T00:00:00Z" },
		"a snapshot of the plan": func(w *workout.Workout) { w.PlannedSnapshot = &workout.PlannedSnapshot{} },
		"swapped":                func(w *workout.Workout) { w.Description += " " + scheduler.SwappedMarker + " easier." },
		"adjusted":               func(w *workout.Workout) { w.Description += " " + scheduler.AdjustedMarker + " eased." },
		"kept by the rider":      func(w *workout.Workout) { w.Description += " " + scheduler.CrewKeptMarker },
		"a crew ride row":        func(w *workout.Workout) { w.CrewRideID = "ride-1" },
	}
	for name, mutate := range cases {
		w := untouched()
		mutate(&w)
		w.Flags = workout.LegacyFlags(w)
		if untouchedPlanSession(w) {
			t.Errorf("%s: offered to the season refresh", name)
		}
	}
}
