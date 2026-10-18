package api

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
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

// A load-capped week is built with WithTargetTSS when it is filled; the zones
// "Train now" reads must come from the same options. (The boost moves rungs,
// not zones today, so this pins the gate rather than a visible difference.)
func TestWeekZonesBuildsALoadCappedWeekThroughTheSeasonOptions(t *testing.T) {
	week := periodization.Week{StartDate: "2026-10-05", Phase: periodization.PhaseBuild, TargetHours: 6, TargetTSS: 100000, LoadCapped: true}
	plan := &periodization.Plan{Weeks: []periodization.Week{week}}
	profile := workout.RiderProfile{AvailableDays: []string{"mon", "wed", "fri", "sun"}, FTPWatts: 250}
	goal := &workout.Goal{ID: "g", Sport: model.SportCycling}
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	got := weekZones(plan, start, profile, nil, "wilant", goal, nil)
	sc := seasonContext{plan: *plan, profile: profile}
	reqs, err := scheduler.WeekWorkouts(week, profile, nil, "wilant", goal.ID, goal.Sport, append(sc.weekOptions(week), fixedOption(nil, week))...)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, r := range reqs {
		if workout.IsStructuredZone(r.Zone) {
			want = append(want, string(r.Zone))
		}
	}
	if len(got) == 0 || len(got) > len(want) {
		t.Errorf("zones = %v, want a subset of %v", got, want)
	}
}
