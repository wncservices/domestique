package scheduler_test

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Both "kept as planned" markers mean the rider decided; no automatic path may
// change such a session, whichever feature wrote the marker.
func TestARiderKeptSessionIsNeverGeneratedOrReplaceable(t *testing.T) {
	gen := scheduler.GeneratedDescription
	for name, marker := range map[string]string{
		"crew kept": scheduler.CrewKeptMarker,
		"life kept": scheduler.LifeKeptMarker,
	} {
		w := workout.Workout{GoalID: "g", Description: gen + " " + marker}
		if !scheduler.RiderKept(w.Description) {
			t.Errorf("%s: RiderKept = false", name)
		}
		if scheduler.IsGenerated(w) {
			t.Errorf("%s: IsGenerated = true, so adaptation may change it", name)
		}
		if scheduler.AutoReplaceable(w) {
			t.Errorf("%s: AutoReplaceable = true, so replan or an FTP test may delete it", name)
		}
		if !scheduler.IsPlanMade(w) || !scheduler.IsPlanSession(w) {
			t.Errorf("%s: still a plan session structurally", name)
		}
	}
	plain := workout.Workout{GoalID: "g", Description: gen}
	if !scheduler.IsGenerated(plain) || !scheduler.AutoReplaceable(plain) {
		t.Error("an untouched plan session must stay changeable")
	}
}
