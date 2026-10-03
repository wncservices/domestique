package api

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/crewplan"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func crewRow(date string, seconds float64) workout.Workout {
	return workout.Workout{
		ID: "crew", Sport: model.SportCycling, Name: "Crew ride: Hill Loop", Date: date, CrewRideID: "ride-1",
		Steps: []workout.WorkoutStep{{Name: "Crew ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: seconds, Target: workout.TargetOpen}},
	}
}

// The tomorrow forecast prices today's crew ride by its own estimate, which
// needs no FTP, and not by a flat default for an open step.
func TestTodaysCrewRideIsPricedByItsOwnEstimate(t *testing.T) {
	const day = "2026-10-07"
	want := crewplan.TSSForSeconds(3 * 3600) // 3 h x 0.65^2 x 100

	for _, ftp := range []float64{0, 250} {
		got, ok := todayTrainingLoad([]workout.Workout{crewRow(day, 3*3600)}, nil, ftp, day)
		if !ok {
			t.Fatalf("ftp %v: the load is unknown, want it known: a crew ride needs no FTP", ftp)
		}
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("ftp %v: load = %v, want %v", ftp, got, want)
		}
	}
	if want < 120 || want > 130 {
		t.Fatalf("setup: %v is not about 127 TSS", want)
	}
	if flat := 3 * 50.0; math.Abs(want-flat) < 1 {
		t.Error("the estimate equals the flat 50 TSS/h default; the test cannot tell them apart")
	}
}

func TestACrewRideAlreadyRiddenCountsItsActualLoadOnly(t *testing.T) {
	const day = "2026-10-07"
	sessions := []workout.CompletedSession{{Date: day, TrainingLoad: 90}}
	got, ok := todayTrainingLoad([]workout.Workout{crewRow(day, 3*3600)}, sessions, 250, day)
	if !ok || got != 90 {
		t.Errorf("load = %v, %v, want the actual 90 and not the estimate on top", got, ok)
	}
}

func TestACrewRideOnAnotherDayDoesNotAddToTodaysLoad(t *testing.T) {
	got, ok := todayTrainingLoad([]workout.Workout{crewRow("2026-10-08", 3*3600)}, nil, 250, "2026-10-07")
	if !ok || got != 0 {
		t.Errorf("load = %v, %v, want 0", got, ok)
	}
}
