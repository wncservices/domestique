package scheduler

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Renaming the long ride must not demote it: a long endurance ride is a key
// session by its name (it has no structured zone), so the adapter keeps moving
// a missed one to a free day, and it is still not a hard session.
func TestTheClimbingLongRideIsStillAKeySessionButNotAHardOne(t *testing.T) {
	for _, name := range []string{"Long ride", ClimbingLongRideName} {
		w := workout.Workout{Name: name, Zone: workout.ZoneEndurance}
		if !IsKeySession(w) {
			t.Errorf("%q is not a key session", name)
		}
		if IsHardSession(w) {
			t.Errorf("%q counts as a hard session", name)
		}
	}
	if !IsLongRideName("Long ride") || !IsLongRideName(ClimbingLongRideName) || IsLongRideName("Endurance ride") {
		t.Error("IsLongRideName mis-classifies")
	}
}
