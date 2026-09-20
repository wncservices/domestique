package scheduler

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The key-session table is written out by name, so it must track the names
// sessionLabel actually produces — or a rename would quietly stop missed
// long rides being noticed.
func TestKeyAndHardNamesMatchWhatTheSchedulerProduces(t *testing.T) {
	for _, sport := range []model.Sport{model.SportCycling, model.SportRunning} {
		for _, tc := range []struct {
			kind      sessionType
			key, hard bool
		}{
			{sessionEasy, false, false},
			{sessionLong, true, false},
			{sessionTempo, true, true},
			{sessionInterval, true, true},
		} {
			name, _ := sessionLabel(tc.kind, sport)
			w := workout.Workout{Name: name}
			if IsKeySession(w) != tc.key || IsHardSession(w) != tc.hard {
				t.Errorf("%s/%q: key=%v hard=%v, want key=%v hard=%v", sport, name, IsKeySession(w), IsHardSession(w), tc.key, tc.hard)
			}
		}
	}
}

func TestOnlyUnadjustedGeneratedWorkoutsMayBeAdapted(t *testing.T) {
	gen := workout.Workout{GoalID: "g", Description: GeneratedDescription}
	if !IsGenerated(gen) {
		t.Error("a scheduler-made workout should be adaptable")
	}
	if IsGenerated(workout.Workout{GoalID: "g", Description: "my own plan"}) {
		t.Error("a rider's own description must never be adapted")
	}
	if IsGenerated(workout.Workout{Description: GeneratedDescription}) {
		t.Error("a workout with no goal is not the scheduler's")
	}
	gen.Description += " " + AdjustedMarker + " moved from 2026-03-17."
	if IsGenerated(gen) {
		t.Error("an already-adjusted workout must not be adjusted a second time")
	}
}

func TestBuiltSessionsCarryTheGeneratedDescription(t *testing.T) {
	req := buildSession(sessionEasy, 1, model.SportCycling, workout.RiderProfile{})
	if req.Description != GeneratedDescription {
		t.Errorf("description = %q — IsGenerated would stop recognising the scheduler's own output", req.Description)
	}
}

func TestEasyVariantIsAFifthShorterAndEasy(t *testing.T) {
	hard := workout.Workout{Sport: model.SportCycling, Name: "VO2max intervals", Steps: []workout.WorkoutStep{
		{Name: "Main", Duration: workout.DurationTime, Seconds: 3600},
	}}
	easy := EasyVariant(hard, workout.RiderProfile{FTPWatts: 250})
	if easy.Name != "Endurance ride" {
		t.Errorf("name = %q, want Endurance ride", easy.Name)
	}
	if got := workout.PlannedSeconds(easy.Steps); got != 2880 {
		t.Errorf("planned = %v s, want 2880 (80%% of an hour)", got)
	}
}

func TestMovedFromReadsTheOriginalDate(t *testing.T) {
	d, ok := MovedFrom(GeneratedDescription + " " + AdjustedMarker + " moved from 2026-03-17, missed.")
	if !ok || d != "2026-03-17" {
		t.Errorf("MovedFrom = %q, %v", d, ok)
	}
	if _, ok := MovedFrom(GeneratedDescription); ok {
		t.Error("an unmoved workout has no original date")
	}
}
