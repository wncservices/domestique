package alternates

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
)

// The route bias can put a session one rung above what the plan would have
// picked, so above the rider's harder cap (floor(level) + 1 is the most a
// swap may reach). Such a session must still offer its alternates: an easier
// rung, never a harder one past the cap.
func TestABiasedRungAboveTheRidersCapStillHasAlternates(t *testing.T) {
	// Rider at threshold level 4.0: the plan would pick level 4 (target 4.5),
	// a route favouring 12-minute efforts picks level 5 (3 x 12), at the cap.
	// Level 6 is the "one above Pick" a rider at 4.6 could be handed.
	for _, level := range []int{5, 6} {
		w := planned(t, model.SportCycling, "threshold", level)
		opts := Options(w, 4.0, false, profile)
		if _, ok := find(opts, Easier); !ok {
			t.Errorf("level %d: no easier alternate: %v", level, kinds(opts))
		}
		if _, ok := find(opts, Harder); ok {
			t.Errorf("level %d: a harder alternate beyond the rider's cap: %v", level, kinds(opts))
		}
		if len(opts) == 0 {
			t.Errorf("level %d: no alternates at all", level)
		}
	}
}

// The climbing long ride is still the week's long day: rescaling it must go
// on producing a long ride, not an endurance ride.
func TestTheClimbingLongRideStaysALongRideWhenRescaled(t *testing.T) {
	w := endurance(model.SportCycling, 4*3600, true)
	w.Name = scheduler.ClimbingLongRideName
	if !isLong(w, 3*3600, profile) {
		t.Fatal("the climbing long ride was not recognised as long")
	}
	req := EnduranceRide(w, 3*3600, profile)
	if !scheduler.IsLongRideName(req.Name) {
		t.Errorf("rescaled to %q, want a long ride", req.Name)
	}
	if opts := Options(w, 4, false, profile); len(opts) == 0 {
		t.Error("the climbing long ride has no alternates")
	}
}
