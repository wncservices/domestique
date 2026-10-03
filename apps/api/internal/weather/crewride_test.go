package weather

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A crew ride is the crew's day, not the rider's: weather may mention it but
// never offers to switch it indoors or move it.
func TestACrewRideGetsInfoOnlyNeverASwitchOrAnotherDay(t *testing.T) {
	f := fourDays()
	wet(f, thu, 9, 12)
	o := opts()
	o.SmartTrainer = true
	rideRow := ride("crew", thu, 3*3600)
	rideRow.CrewRideID = "ride-1"
	plain := ride("plain", thu, 3*3600)

	got := map[string]Suggestion{}
	for _, s := range Suggest(f, []workout.Workout{plain, rideRow}, nil, o) {
		got[s.WorkoutID] = s
	}
	if s, ok := got["crew"]; !ok {
		t.Fatal("a wet crew ride gets no mention at all")
	} else if s.CanSwitch || s.AltDate != "" || !s.CrewRide {
		t.Errorf("crew ride suggestion = %+v, want info only: no switch, no other day", s)
	}
	if !got["plain"].CanSwitch {
		t.Error("the control session lost its switch")
	}

	o.SmartTrainer = false
	for _, s := range Suggest(f, []workout.Workout{rideRow}, nil, o) {
		if s.AltDate != "" {
			t.Errorf("a crew ride was offered another day: %+v", s)
		}
	}
}
