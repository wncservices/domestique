package lifeevents

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The crew feature's "kept as planned" is the rider's decision too: the return
// ramp must not ease it, just as it leaves its own kind alone.
func TestASessionKeptAroundACrewRideIsNotRamped(t *testing.T) {
	kept := hard("kept", "2026-10-12", 90)
	kept.Description += " " + scheduler.CrewKeptMarker
	kept.Flags = workout.LegacyFlags(kept)
	d := preview([]Event{ill("2026-10-08", "2026-10-11", OptionProper)}, nil, kept, hard("other", "2026-10-13", 90))
	if _, ok := find(d, "ease:kept"); ok {
		t.Fatal("the ramp eased a session the rider kept around a crew ride")
	}
	if _, ok := find(d, "ease:other"); !ok {
		t.Fatal("the control session was not eased")
	}
}

func TestTheSharedMarkersAreTheOnesEachFeatureWrites(t *testing.T) {
	if KeptMarker != scheduler.LifeKeptMarker {
		t.Errorf("lifeevents.KeptMarker %q drifted from scheduler.LifeKeptMarker", KeptMarker)
	}
}
