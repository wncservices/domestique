package scheduler

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// crewRideRow is the fixed session a rider gets for "I'm going": it carries its
// crew ride's id, one open step of the estimated length and a description that
// is not the generated one.
func crewRideRow(seconds float64) workout.Workout {
	return workout.Workout{
		ID: "w", Rider: "wilant", Sport: model.SportCycling, Name: "Crew ride: Hill Loop",
		GoalID: "goal", Date: "2026-10-10", CrewRideID: "ride-1", Zone: workout.ZoneEndurance,
		Description: "Crew ride with Sunday Club: Hill Loop, 100 km, 1000 m, about 4 h 20, estimated TSS 184.",
		Steps:       []workout.WorkoutStep{{Name: "Crew ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: seconds, Target: workout.TargetOpen}},
	}
}

func TestALongCrewRideIsAKeySessionButNeverHard(t *testing.T) {
	long := crewRideRow(7200)
	if !IsKeySession(long) {
		t.Error("a crew ride of 120 minutes is the week's long ride, a key session")
	}
	if IsHardSession(long) {
		t.Error("a long ride is key but not hard, a crew ride included")
	}
}

func TestAnEnduranceCrewRideIsNotKey(t *testing.T) {
	if IsKeySession(crewRideRow(7199)) {
		t.Error("a crew ride just under 120 minutes is fixed but not key")
	}
	if IsKeySession(crewRideRow(3600)) {
		t.Error("a 60 minute crew ride is not key")
	}
}

func TestACrewRideRowIsNeverGeneratedSoNothingAutomaticMayTouchIt(t *testing.T) {
	if IsGenerated(crewRideRow(7200)) {
		t.Error("a crew ride row must not read as generated: adaptation, easing and replan only touch generated sessions")
	}
	// Even a description that starts with the generated prefix does not make a
	// crew ride row generated: the row is what is fixed, not its text.
	row := crewRideRow(7200)
	row.Description = GeneratedDescription + " " + row.Description
	if IsGenerated(row) {
		t.Error("a crew ride row with the generated prefix still must not be generated")
	}
}

func TestACrewRidePlannedSecondsAreItsStepLength(t *testing.T) {
	if got := workout.PlannedSeconds(crewRideRow(15600).Steps); got != 15600 {
		t.Errorf("planned seconds = %v, want the step's own length", got)
	}
}
