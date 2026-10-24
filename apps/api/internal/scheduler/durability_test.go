package scheduler

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func durabilityWeek() (periodization.Week, workout.RiderProfile) {
	week := periodization.Week{StartDate: "2026-01-05", Phase: periodization.PhaseBuild, TargetHours: 8}
	profile := workout.RiderProfile{
		AvailableDays:   []string{"mon", "wed", "fri", "sun"},
		FTPWatts:        250,
		ExperienceLevel: "intermediate", // endurance starts at 4
	}
	return week, profile
}

// longAndTotal returns the Sunday (long) ride's seconds and the week's total.
func longAndTotal(t *testing.T, levels map[string]float64) (long, total float64) {
	t.Helper()
	week, profile := durabilityWeek()
	out, err := WeekWorkouts(week, profile, levels, "r", "g", model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range out {
		s := totalSeconds(r.Steps)
		total += s
		if i == len(out)-1 {
			long = s
		}
	}
	return long, total
}

func TestLongSlotWeightGrowsWithEnduranceLevel(t *testing.T) {
	base := map[string]float64{"threshold": 3, "vo2max": 3}
	grown := map[string]float64{"threshold": 3, "vo2max": 3, "endurance": 7}
	l0, t0 := longAndTotal(t, base)
	l1, t1 := longAndTotal(t, grown)
	if l1 <= l0 {
		t.Fatalf("long ride %v not longer than %v", l1, l0)
	}
	if math.Abs(t1-t0) > 1 {
		t.Fatalf("week total changed: %v vs %v", t1, t0)
	}
}

func TestLongSlotWeightIsCapped(t *testing.T) {
	_, profile := durabilityWeek()
	got := longWeight(map[string]float64{"endurance": 10}, profile, model.SportCycling)
	if math.Abs(got-2.4) > 1e-9 {
		t.Fatalf("weight = %v, want 2.4", got)
	}
	if got := longWeight(map[string]float64{"endurance": 1}, profile, model.SportCycling); got != 1.8 {
		t.Fatalf("below start weight = %v, want 1.8", got)
	}
}

func TestNoEnduranceLevelIsTodaysWeight(t *testing.T) {
	_, profile := durabilityWeek()
	if got := longWeight(nil, profile, model.SportCycling); got != 1.8 {
		t.Fatalf("weight = %v, want 1.8", got)
	}
	if got := longWeight(map[string]float64{"endurance": 9}, profile, model.SportRunning); got != 1.8 {
		t.Fatalf("running weight = %v, want 1.8", got)
	}
}
