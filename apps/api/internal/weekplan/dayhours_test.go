package weekplan_test

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/weekplan"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func hoursProfile(h map[string]float64) workout.RiderProfile {
	p := workout.RiderProfile{HoursByDay: h}
	p.NormaliseAvailability()
	return p
}

func TestFits(t *testing.T) {
	p := hoursProfile(map[string]float64{"tue": 1})
	if !weekplan.Fits(p, tue, 3600+300) {
		t.Error("an hour plus the slack must fit a 1 h day")
	}
	if weekplan.Fits(p, tue, 3601+300) {
		t.Error("past the slack must not fit")
	}
	if !weekplan.Fits(p, wed, 20*3600) {
		t.Error("a weekday without hours always fits")
	}
}

func TestSwapSkipsAPairThatDoesNotFit(t *testing.T) {
	p := hoursProfile(map[string]float64{"mon": 2, "wed": 1, "fri": 2})
	h := gen("h", mon, workout.ZoneThreshold, 5400)
	in := input(mon, h, easy("e1", wed, 3600))
	in.Fits = func(d string, s float64) bool { return weekplan.Fits(p, d, s) }
	if got, ok := weekplan.PlanSwap(in); ok {
		t.Fatalf("a 90 min session must not go to a 1 h day, got %+v", got)
	}
	in = input(mon, h, easy("e1", wed, 3600), easy("e2", fri, 3600))
	in.Fits = func(d string, s float64) bool { return weekplan.Fits(p, d, s) }
	got, ok := weekplan.PlanSwap(in)
	if !ok || got.EasyID != "e2" || got.HardTo != fri {
		t.Fatalf("got %+v ok=%v, want the 2 h Friday", got, ok)
	}
}
