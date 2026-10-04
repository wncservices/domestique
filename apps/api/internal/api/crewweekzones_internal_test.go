package api

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// "Train now" asks which structured zones the plan still holds this week. A
// crew ride squeezes the week's hard session out, so the answer has to know.
func TestWeekZonesKnowsTheWeekIsBuiltAroundACrewRide(t *testing.T) {
	week := periodization.Week{StartDate: "2026-10-05", Phase: periodization.PhaseBase, TargetHours: 4.5}
	plan := &periodization.Plan{Weeks: []periodization.Week{week}}
	profile := workout.RiderProfile{AvailableDays: []string{"wed", "fri", "sat"}}
	goal := &workout.Goal{ID: "g", Sport: model.SportCycling}
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	if zones := weekZones(plan, start, profile, nil, "wilant", goal, nil); len(zones) == 0 {
		t.Fatal("setup: the plain week holds no structured session to squeeze out")
	}
	ride := workout.Workout{CrewRideID: "r", Date: "2026-10-07", Steps: []workout.WorkoutStep{
		{Name: "Crew ride", Duration: workout.DurationTime, Seconds: 1800, Target: workout.TargetOpen},
	}}
	if zones := weekZones(plan, start, profile, nil, "wilant", goal, []workout.Workout{ride}); len(zones) != 0 {
		t.Errorf("zones = %v, want none: the ride sits on the structured day", zones)
	}
}
