package scheduler

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The legacy name table is written out by hand, so it must track the names
// enduranceName actually produces for the long day — or a rename there would
// quietly stop a missed long ride/run (made before the zone column existed)
// being noticed. Every current structured session carries a real zone and
// so is classified by that instead (see TestKeyAndHardSessionReadZoneWithNameFallback).
func TestKeyAndHardNamesMatchWhatTheSchedulerProduces(t *testing.T) {
	for _, sport := range []model.Sport{model.SportCycling, model.SportRunning} {
		for _, tc := range []struct {
			long      bool
			key, hard bool
		}{
			{false, false, false},
			{true, true, false},
		} {
			name := enduranceName(tc.long, sport)
			w := workout.Workout{Name: name}
			if IsKeySession(w) != tc.key || IsHardSession(w) != tc.hard {
				t.Errorf("%s/%q: key=%v hard=%v, want key=%v hard=%v", sport, name, IsKeySession(w), IsHardSession(w), tc.key, tc.hard)
			}
		}
	}
}

// A workout's zone, when set, decides key/hard status outright — the name
// tables above are only ever consulted for a row with no zone (made before
// this column existed, or a rider's own hand-built workout that never set
// one).
func TestKeyAndHardSessionReadZoneWithNameFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		w    workout.Workout
		key  bool
		hard bool
	}{
		{"structured zone alone is key and hard", workout.Workout{Zone: workout.ZoneThreshold, Name: "Whatever"}, true, true},
		{"endurance zone falls back to the name table (long)", workout.Workout{Zone: workout.ZoneEndurance, Name: "Long ride"}, true, false},
		{"legacy row with no zone falls back to the name table (tempo)", workout.Workout{Zone: "", Name: "Tempo ride"}, true, true},
		{"a rider's own hand-built workout with no zone is neither", workout.Workout{Zone: "", Name: "Hill repeats"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsKeySession(tc.w); got != tc.key {
				t.Errorf("IsKeySession = %v, want %v", got, tc.key)
			}
			if got := IsHardSession(tc.w); got != tc.hard {
				t.Errorf("IsHardSession = %v, want %v", got, tc.hard)
			}
		})
	}
}

func TestOnlyUnadjustedGeneratedWorkoutsMayBeAdapted(t *testing.T) {
	gen := legacy(workout.Workout{GoalID: "g", Description: GeneratedDescription})
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
	gen = legacy(gen)
	if IsGenerated(gen) {
		t.Error("an already-adjusted workout must not be adjusted a second time")
	}
}

func TestBuiltSessionsCarryTheGeneratedDescription(t *testing.T) {
	req := BuildEnduranceSession(1, false, model.SportCycling, workout.RiderProfile{})
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
	d, ok := MovedFrom(legacy(workout.Workout{Description: GeneratedDescription + " " + AdjustedMarker + " moved from 2026-03-17, missed."}))
	if !ok || d != "2026-03-17" {
		t.Errorf("MovedFrom = %q, %v", d, ok)
	}
	if _, ok := MovedFrom(legacy(workout.Workout{Description: GeneratedDescription})); ok {
		t.Error("an unmoved workout has no original date")
	}
}

func TestASwappedSessionIsRiderTouchedAndNotGenerated(t *testing.T) {
	gen := legacy(workout.Workout{GoalID: "g", Description: GeneratedDescription})
	if !IsGenerated(gen) {
		t.Fatal("setup: a scheduler-made workout should be generated")
	}
	gen.Description += " " + SwappedMarker + " harder, was Threshold 4 (1h10)."
	gen = legacy(gen)
	if IsGenerated(gen) {
		t.Error("a swapped session is the rider's choice; adaptation must not rewrite it")
	}
}

// The predicates read the typed columns. The description is deliberately
// misleading here, so a predicate that still looked at it would answer wrongly.
func TestPredicatesIgnoreDescriptionText(t *testing.T) {
	plan := workout.Flags{Origin: workout.OriginPlan}

	w := workout.Workout{GoalID: "g", Description: "anything", Flags: plan}
	if !IsGenerated(w) {
		t.Error("plan origin with unrelated text must be generated")
	}

	w = workout.Workout{GoalID: "g", Description: GeneratedDescription + " " + AdjustedMarker, Flags: plan}
	if !IsGenerated(w) {
		t.Error("a marker in the text with no adaptation in the flags must still be generated")
	}

	kept := plan
	kept.Kept = workout.KeptCrew
	w = workout.Workout{GoalID: "g", Description: GeneratedDescription, Flags: kept}
	if IsGenerated(w) || !IsPlanSession(w) {
		t.Errorf("kept session: generated=%v planSession=%v, want false/true", IsGenerated(w), IsPlanSession(w))
	}
	if !RiderKept(w) {
		t.Error("RiderKept must read Kept")
	}

	moved := plan
	moved.MovedFrom = "2026-10-01"
	if from, ok := MovedFrom(workout.Workout{Description: "x", Flags: moved}); !ok || from != "2026-10-01" {
		t.Errorf("MovedFrom = %q, %v", from, ok)
	}
}

// legacy gives a fixture the Flags its description text implies, so a test that
// writes markers by hand exercises the columns the predicates now read.
func legacy(w workout.Workout) workout.Workout {
	w.Flags = workout.LegacyFlags(w)
	return w
}

func TestIsPlanMadeAndAutoReplaceableTable(t *testing.T) {
	plan := func(mut func(*workout.Workout)) workout.Workout {
		w := workout.Workout{GoalID: "g", Flags: workout.Flags{Origin: workout.OriginPlan}}
		if mut != nil {
			mut(&w)
		}
		return w
	}
	cases := []struct {
		name           string
		w              workout.Workout
		planMade, auto bool
	}{
		{"plain", plan(nil), true, true},
		{"adapted", plan(func(w *workout.Workout) { w.Flags.AdaptedRules = []why.Rule{why.ReadinessRest} }), true, true},
		{"routed", plan(func(w *workout.Workout) { w.RouteSlug = "loop" }), false, false},
		{"swapped", plan(func(w *workout.Workout) { w.Flags.Swapped = true }), false, false},
		{"kept", plan(func(w *workout.Workout) { w.Flags.Kept = workout.KeptLife }), true, false},
		{"crew ride", plan(func(w *workout.Workout) { w.CrewRideID = "r" }), false, false},
		{"goal gone", plan(func(w *workout.Workout) { w.GoalID = "" }), false, false},
		{"rider origin", plan(func(w *workout.Workout) { w.Flags.Origin = workout.OriginRider }), false, false},
		{"no flags", workout.Workout{GoalID: "g"}, false, false},
	}
	for _, c := range cases {
		if got := IsPlanMade(c.w); got != c.planMade {
			t.Errorf("%s: IsPlanMade = %v, want %v", c.name, got, c.planMade)
		}
		if got := AutoReplaceable(c.w); got != c.auto {
			t.Errorf("%s: AutoReplaceable = %v, want %v", c.name, got, c.auto)
		}
	}
}

func TestLoadRepaidDoesNotLockASession(t *testing.T) {
	w := workout.Workout{GoalID: "g", Flags: workout.Flags{
		Origin: workout.OriginPlan, AdaptedRules: []why.Rule{why.LoadRepaid},
	}}
	if !IsGenerated(w) || !IsPlanSession(w) {
		t.Error("a session that only carries load_repaid should still be generated")
	}
}

func TestGreenPushDoesNotLockASession(t *testing.T) {
	w := workout.Workout{GoalID: "g", Flags: workout.Flags{
		Origin: workout.OriginPlan, AdaptedRules: []why.Rule{why.GreenPush},
	}}
	if !IsGenerated(w) || !IsPlanSession(w) || len(w.Flags.EasedBy()) != 0 {
		t.Error("a session that only carries green_push should still be generated")
	}
}

func TestAnyOtherRuleStillLocks(t *testing.T) {
	w := workout.Workout{GoalID: "g", Flags: workout.Flags{
		Origin: workout.OriginPlan, AdaptedRules: []why.Rule{why.LoadRepaid, why.ReadinessRest},
	}}
	if IsGenerated(w) || IsPlanSession(w) {
		t.Error("load_repaid plus another rule must lock the session")
	}
}

func TestStructuredZonesMatchesTheScheduler(t *testing.T) {
	days := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	for _, sport := range []model.Sport{model.SportCycling, model.SportRunning} {
		for _, phase := range []periodization.Phase{
			periodization.PhaseBase, periodization.PhaseBuild, periodization.PhasePeak, periodization.PhaseTaper,
		} {
			for n := 0; n <= 7; n++ {
				week := periodization.Week{StartDate: "2026-01-05", Phase: phase, TargetHours: 10}
				profile := workout.RiderProfile{AvailableDays: days[:n]}
				out, err := WeekWorkouts(week, profile, nil, "r", "g", sport)
				if err != nil {
					t.Fatal(err)
				}
				got := StructuredZones(phase, sport, n)
				want := structuredZones(out)
				if len(got) != len(want) {
					t.Fatalf("%s %s n=%d: StructuredZones = %v, scheduler gave %v", sport, phase, n, got, want)
				}
				seen := map[workout.Zone]int{}
				for _, z := range got {
					seen[z]++
				}
				for _, z := range want {
					seen[z]--
				}
				for z, c := range seen {
					if c != 0 {
						t.Errorf("%s %s n=%d: zone %s differs: %v vs %v", sport, phase, n, z, got, want)
					}
				}
			}
		}
	}
}
