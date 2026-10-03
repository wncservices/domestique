package scheduler

import (
	"reflect"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func cl(grade float64) climbs.Climb { return climbs.Climb{AvgGradient: grade} }

func TestDemandFromClimbsPerTheSpecTable(t *testing.T) {
	min := func(m float64) float64 { return m * 60 }

	t.Run("sustained is the longest 4 to 30 minute climb, capped at 20", func(t *testing.T) {
		d := DemandFromClimbs([]climbs.Climb{cl(5), cl(5), cl(5), cl(5)},
			[]float64{min(3), min(9), min(25), min(40)}, 100, 20_000)
		if d == nil || d.Sustained != 20*60 {
			t.Errorf("Sustained = %+v, want 1200 (25 min capped; 40 min is out of range)", d)
		}
		d = DemandFromClimbs([]climbs.Climb{cl(5), cl(5)}, []float64{min(12), min(7)}, 100, 20_000)
		if d.Sustained != 12*60 {
			t.Errorf("Sustained = %d, want 720", d.Sustained)
		}
		d = DemandFromClimbs([]climbs.Climb{cl(5)}, []float64{min(4)}, 100, 20_000)
		if d.Sustained != 4*60 {
			t.Errorf("a 4 minute climb is in range: Sustained = %d", d.Sustained)
		}
		d = DemandFromClimbs([]climbs.Climb{cl(5)}, []float64{min(30)}, 100, 20_000)
		if d.Sustained != 20*60 {
			t.Errorf("a 30 minute climb is in range and capped: Sustained = %d", d.Sustained)
		}
		if d = DemandFromClimbs([]climbs.Climb{cl(5)}, []float64{min(31)}, 100, 20_000); d != nil && d.Sustained != 0 {
			t.Errorf("a 31 minute climb is out of range: Sustained = %d", d.Sustained)
		}
	})

	t.Run("short is the median of climbs under 6 minutes", func(t *testing.T) {
		d := DemandFromClimbs([]climbs.Climb{cl(6), cl(6), cl(6), cl(6)},
			[]float64{min(2), min(4), min(5), min(9)}, 100, 20_000)
		if d.Short != 4*60 {
			t.Errorf("Short = %d, want the median of 2, 4, 5 min = 240", d.Short)
		}
		d = DemandFromClimbs([]climbs.Climb{cl(6), cl(6)}, []float64{min(3), min(5)}, 100, 20_000)
		if d.Short != 4*60 {
			t.Errorf("Short = %d, want the mean of the middle two = 240", d.Short)
		}
	})

	t.Run("a very short climb asks nothing of anaerobic slots", func(t *testing.T) {
		d := DemandFromClimbs([]climbs.Climb{cl(11)}, []float64{90}, 100, 20_000)
		if d == nil || d.Sustained != 0 || d.wantFor("anaerobic") != 0 {
			t.Errorf("a 90 s climb gave %+v; it may bias vo2max (Short) but never threshold or anaerobic", d)
		}
		if got := (&RouteDemand{Sustained: 600, Short: 300}).wantFor("anaerobic"); got != 0 {
			t.Errorf("wantFor(anaerobic) = %d, want 0", got)
		}
	})

	t.Run("climbing at 10 m/km or 1500 m", func(t *testing.T) {
		if d := DemandFromClimbs(nil, nil, 300, 20_000); d == nil || !d.Climbing {
			t.Errorf("15 m/km should be climbing: %+v", d)
		}
		if d := DemandFromClimbs(nil, nil, 200, 20_000); d == nil || !d.Climbing {
			t.Errorf("exactly 10 m/km should be climbing: %+v", d)
		}
		if d := DemandFromClimbs(nil, nil, 1500, 300_000); d == nil || !d.Climbing {
			t.Errorf("1500 m in total should be climbing: %+v", d)
		}
		if d := DemandFromClimbs(nil, nil, 100, 20_000); d != nil {
			t.Errorf("5 m/km and 100 m is not a demand: %+v", d)
		}
	})

	t.Run("nothing qualifying is nil", func(t *testing.T) {
		if d := DemandFromClimbs(nil, nil, 0, 0); d != nil {
			t.Errorf("got %+v", d)
		}
		// mismatched lengths are a caller bug, answered safely
		if d := DemandFromClimbs([]climbs.Climb{cl(5)}, nil, 0, 0); d != nil {
			t.Errorf("got %+v", d)
		}
	})
}

// A week for a cycling rider with four days, levels where Pick and the demand
// part ways: threshold 4 (target 4.5: Pick is level 4, 4 x 8) and vo2max 4.
func demandWeek(phase periodization.Phase, recovery bool) (periodization.Week, workout.RiderProfile, map[string]float64) {
	week := periodization.Week{StartDate: "2026-10-05", Phase: phase, Recovery: recovery, TargetHours: 10}
	profile := workout.RiderProfile{FTPWatts: 250, AvailableDays: []string{"tue", "thu", "sat", "sun"}}
	levels := map[string]float64{"sweet_spot": 4, "threshold": 4, "vo2max": 4, "anaerobic": 4}
	return week, profile, levels
}

var testDemand = &RouteDemand{Sustained: 12 * 60, Short: 5 * 60, Climbing: true}

func weekWith(t *testing.T, phase periodization.Phase, recovery bool, opts ...Option) []workout.CreateWorkoutRequest {
	t.Helper()
	week, profile, levels := demandWeek(phase, recovery)
	out, err := WeekWorkouts(week, profile, levels, "wilant", "g1", model.SportCycling, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func byZone(out []workout.CreateWorkoutRequest, zone workout.Zone) *workout.CreateWorkoutRequest {
	for i := range out {
		if out[i].Zone == zone {
			return &out[i]
		}
	}
	return nil
}

func TestBuildThresholdSlotMovesTowardTheClimbs(t *testing.T) {
	plain := byZone(weekWith(t, periodization.PhaseBuild, false), workout.ZoneThreshold)
	biased := byZone(weekWith(t, periodization.PhaseBuild, false, WithRouteDemand(testDemand)), workout.ZoneThreshold)
	if plain == nil || biased == nil {
		t.Fatal("no threshold session in a Build week")
	}
	if plain.Level != 4 || biased.Level != 5 {
		t.Errorf("levels = %v (plain), %v (biased); want 4 and 5", plain.Level, biased.Level)
	}
	if plain.Name != "Threshold 4×8" || biased.Name != "Threshold 3×12" {
		t.Errorf("names = %q / %q, want Threshold 4×8 / Threshold 3×12", plain.Name, biased.Name)
	}
	// Zone and description unchanged: only the shape of the work moved.
	if biased.Zone != plain.Zone || biased.Description != plain.Description || biased.Date != plain.Date {
		t.Errorf("zone, description or date moved: %+v vs %+v", biased, plain)
	}
}

func TestPeakVO2maxSlotMovesTowardShortClimbs(t *testing.T) {
	plain := byZone(weekWith(t, periodization.PhasePeak, false), workout.ZoneVO2Max)
	biased := byZone(weekWith(t, periodization.PhasePeak, false, WithRouteDemand(testDemand)), workout.ZoneVO2Max)
	if plain == nil || biased == nil {
		t.Fatal("no vo2max session in a Peak week")
	}
	if plain.Level != 4 || biased.Level != 5 || biased.Name != "VO2max 5×4" {
		t.Errorf("plain level %v, biased level %v name %q; want 4, 5 and VO2max 5×4", plain.Level, biased.Level, biased.Name)
	}
}

func TestNoBiasOutsideBuildAndPeakOrInARecoveryWeek(t *testing.T) {
	for _, tc := range []struct {
		name     string
		phase    periodization.Phase
		recovery bool
	}{
		{"base", periodization.PhaseBase, false},
		{"taper", periodization.PhaseTaper, false},
		{"recovery week in build", periodization.PhaseBuild, true},
		{"recovery week in base", periodization.PhaseBase, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain := weekWith(t, tc.phase, tc.recovery)
			biased := weekWith(t, tc.phase, tc.recovery, WithRouteDemand(testDemand))
			if !reflect.DeepEqual(plain, biased) {
				t.Errorf("a route demand changed a %s week:\n%+v\n%+v", tc.name, plain, biased)
			}
		})
	}
}

func TestWithoutADemandTheOutputIsExactlyToday(t *testing.T) {
	for _, phase := range []periodization.Phase{periodization.PhaseBase, periodization.PhaseBuild, periodization.PhasePeak, periodization.PhaseTaper} {
		plain := weekWith(t, phase, false)
		if got := weekWith(t, phase, false, WithRouteDemand(nil)); !reflect.DeepEqual(plain, got) {
			t.Errorf("%s: WithRouteDemand(nil) changed the week", phase)
		}
		if got := weekWith(t, phase, false, WithRouteDemand(&RouteDemand{})); !reflect.DeepEqual(plain, got) {
			t.Errorf("%s: an empty demand changed the week", phase)
		}
	}
}

func TestZonesAndDatesNeverChangeUnderABias(t *testing.T) {
	for _, phase := range []periodization.Phase{periodization.PhaseBuild, periodization.PhasePeak} {
		plain := weekWith(t, phase, false)
		biased := weekWith(t, phase, false, WithRouteDemand(testDemand))
		if len(plain) != len(biased) {
			t.Fatalf("%s: %d sessions became %d", phase, len(plain), len(biased))
		}
		for i := range plain {
			if plain[i].Zone != biased[i].Zone || plain[i].Date != biased[i].Date || plain[i].Sport != biased[i].Sport {
				t.Errorf("%s session %d changed zone/date/sport: %+v vs %+v", phase, i, plain[i], biased[i])
			}
		}
	}
}

func TestLongRideIsRenamedOnlyInBuildAndPeakAndKeepsItsSteps(t *testing.T) {
	for _, phase := range []periodization.Phase{periodization.PhaseBuild, periodization.PhasePeak} {
		plain := byZone(weekWith(t, phase, false), workout.ZoneEndurance)
		var long, plainLong *workout.CreateWorkoutRequest
		// Climbing only: a wanted effort length would move the structured
		// rungs, which changes how many hours are left for the long ride. The
		// rename itself must change nothing but the name.
		biased := weekWith(t, phase, false, WithRouteDemand(&RouteDemand{Climbing: true}))
		all := weekWith(t, phase, false)
		for i := range all {
			if all[i].Name == "Long ride" {
				plainLong = &all[i]
			}
		}
		for i := range biased {
			if biased[i].Name == ClimbingLongRideName {
				long = &biased[i]
			}
		}
		if plain == nil || plainLong == nil || long == nil {
			t.Fatalf("%s: long ride not found (plain %v, biased %v)", phase, plainLong, long)
		}
		if !reflect.DeepEqual(plainLong.Steps, long.Steps) || plainLong.Date != long.Date {
			t.Errorf("%s: renaming the long ride changed its steps or date", phase)
		}
	}
	if ClimbingLongRideName != "Long ride, with climbing" {
		t.Errorf("the name is binding: %q", ClimbingLongRideName)
	}

	// Not renamed when the route is not a climbing one, even with a demand.
	flatish := &RouteDemand{Sustained: 12 * 60}
	for _, w := range weekWith(t, periodization.PhaseBuild, false, WithRouteDemand(flatish)) {
		if w.Name == ClimbingLongRideName {
			t.Error("a route that is not climbing renamed the long ride")
		}
	}
	// Base, taper, recovery: never.
	for _, w := range weekWith(t, periodization.PhaseBase, false, WithRouteDemand(testDemand)) {
		if w.Name == ClimbingLongRideName {
			t.Error("the base long ride was renamed")
		}
	}
}

func TestBiasIsOneLevelAbovePickAtMost(t *testing.T) {
	for _, phase := range []periodization.Phase{periodization.PhaseBuild, periodization.PhasePeak} {
		for lvl := 1.0; lvl <= 9; lvl += 0.5 {
			week, profile, _ := demandWeek(phase, false)
			levels := map[string]float64{"threshold": lvl, "vo2max": lvl, "anaerobic": lvl}
			plain, _ := WeekWorkouts(week, profile, levels, "wilant", "g1", model.SportCycling)
			for _, want := range []int{60, 240, 720, 1200} {
				biased, _ := WeekWorkouts(week, profile, levels, "wilant", "g1", model.SportCycling,
					WithRouteDemand(&RouteDemand{Sustained: want, Short: want}))
				for i := range plain {
					if biased[i].Level > plain[i].Level+1 {
						t.Fatalf("%s level %.1f want %d: %s is level %v, more than one above the plain %v", phase, lvl, want, biased[i].Zone, biased[i].Level, plain[i].Level)
					}
				}
			}
		}
	}
}

func TestNextWorkoutsAcceptsTheOption(t *testing.T) {
	week, profile, levels := demandWeek(periodization.PhaseBuild, false)
	plan := periodization.Plan{Weeks: []periodization.Week{week}}
	today := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	out, err := NextWorkouts(plan, profile, levels, "wilant", "g1", model.SportCycling, today, WithRouteDemand(testDemand))
	if err != nil {
		t.Fatal(err)
	}
	if byZone(out, workout.ZoneThreshold) == nil || byZone(out, workout.ZoneThreshold).Name != "Threshold 3×12" {
		t.Errorf("NextWorkouts ignored the demand: %+v", out)
	}
}
