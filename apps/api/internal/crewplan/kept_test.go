package crewplan

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
)

// A session kept through a life-event return is the rider's decision too.
func TestASessionKeptThroughALifeEventReturnIsNotEasedForACrewRide(t *testing.T) {
	kept := hard("fri-kept", fri)
	kept.Description += " " + scheduler.LifeKeptMarker
	if d := Preview(input(longRide, kept)); len(d.Changes) != 0 {
		t.Errorf("changes = %+v, want none for a session the rider chose to keep", d.Changes)
	}
}

func TestTheSharedMarkerIsTheOneCrewplanWrites(t *testing.T) {
	if KeptMarker != scheduler.CrewKeptMarker {
		t.Errorf("crewplan.KeptMarker %q drifted from scheduler.CrewKeptMarker", KeptMarker)
	}
}
