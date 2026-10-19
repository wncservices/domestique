package weekplan_test

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/weekplan"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// 2026-10-19 is a Monday.
const (
	mon = "2026-10-19"
	tue = "2026-10-20"
	wed = "2026-10-21"
	thu = "2026-10-22"
	fri = "2026-10-23"
	sat = "2026-10-24"
	sun = "2026-10-25"
)

func gen(id, date string, zone workout.Zone, seconds float64) workout.Workout {
	return workout.Workout{
		ID: id, Date: date, Sport: model.SportCycling, Zone: zone,
		GoalID: "g", Description: workout.GeneratedDescription,
		Name:  "s-" + id,
		Steps: []workout.WorkoutStep{{Duration: workout.DurationTime, Seconds: seconds}},
		Flags: workout.Flags{Origin: workout.OriginPlan},
	}
}

func hard(id, date string) workout.Workout { return gen(id, date, workout.ZoneThreshold, 3600) }
func easy(id, date string, secs float64) workout.Workout {
	return gen(id, date, workout.ZoneEndurance, secs)
}

func input(today string, week ...workout.Workout) weekplan.SwapInput {
	return weekplan.SwapInput{Today: today, Sunday: sun, Week: week}
}

func TestHardIncludesTestsAndCrewRides(t *testing.T) {
	test := easy("t", tue, 3600)
	test.TestProtocol = "ramp"
	crew := easy("c", tue, 3600)
	crew.CrewRideID = "r"
	for name, tc := range map[string]struct {
		w    workout.Workout
		want bool
	}{
		"threshold": {hard("a", tue), true},
		"endurance": {easy("b", tue, 3600), false},
		"test":      {test, true},
		"crew":      {crew, true},
	} {
		if got := weekplan.Hard(tc.w); got != tc.want {
			t.Errorf("%s: Hard = %v, want %v", name, got, tc.want)
		}
	}
}

func TestHardNeighbourIgnoresGivenIDs(t *testing.T) {
	week := []workout.Workout{hard("h", tue), easy("e", wed, 3600)}
	if !weekplan.HardNeighbour(wed, week) {
		t.Fatal("hard day before should count")
	}
	if weekplan.HardNeighbour(wed, week, "h") {
		t.Fatal("ignored id must not count")
	}
	if !weekplan.HardNeighbour(mon, week) {
		t.Fatal("hard day after should count")
	}
	if !weekplan.HardNeighbour(thu+"x", week) {
		t.Fatal("an unparseable date fails closed")
	}
}

func TestEveOfFixed(t *testing.T) {
	test := easy("t", thu, 3600)
	test.TestProtocol = "ramp"
	if !weekplan.EveOfFixed(wed, []workout.Workout{test}) {
		t.Fatal("day before a test is an eve")
	}
	if weekplan.EveOfFixed(wed, []workout.Workout{hard("h", thu)}) {
		t.Fatal("a plain hard day is not fixed")
	}
	// Isolated from HardNeighbour: ignoring the test itself leaves no hard
	// neighbour, yet the day before it is still an eve.
	if weekplan.HardNeighbour(wed, []workout.Workout{test}, "t") || !weekplan.EveOfFixed(wed, []workout.Workout{test}) {
		t.Fatal("EveOfFixed must hold on its own")
	}
	if !weekplan.EveOfFixed("not-a-date", []workout.Workout{test}) {
		t.Fatal("an unparseable date fails closed")
	}
}

func TestPlanSwapPrefersTheDayAfterTomorrow(t *testing.T) {
	in := input(mon, hard("h", mon), easy("e1", tue, 3600), easy("e2", wed, 3600), easy("e3", thu, 3600))
	in.TomorrowReady = true
	got, ok := weekplan.PlanSwap(in)
	if !ok || got != (weekplan.Swap{HardID: "h", EasyID: "e2", HardTo: wed, EasyTo: mon}) {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestPlanSwapUsesTomorrowOnlyWhenReady(t *testing.T) {
	in := input(mon, hard("h", mon), easy("e1", tue, 3600))
	if _, ok := weekplan.PlanSwap(in); ok {
		t.Fatal("tomorrow must not be used when not ready")
	}
	in.TomorrowReady = true
	got, ok := weekplan.PlanSwap(in)
	if !ok || got.EasyID != "e1" || got.HardTo != tue {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	// A later allowed day still wins over a ready tomorrow.
	in = input(mon, hard("h", mon), easy("e1", tue, 3600), easy("e2", fri, 3600))
	in.TomorrowReady = true
	got, _ = weekplan.PlanSwap(in)
	if got.EasyID != "e2" {
		t.Fatalf("got %+v", got)
	}
}

func TestPlanSwapSkipsNoTestDays(t *testing.T) {
	in := input(mon, hard("h", mon), easy("e1", wed, 3600), easy("e2", fri, 3600))
	in.NoTestDays = map[string]bool{wed: true}
	got, ok := weekplan.PlanSwap(in)
	if !ok || got.EasyID != "e2" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestPlanSwapNeverCreatesBackToBackHardDays(t *testing.T) {
	// Another hard day on Thursday blocks Wed and Fri; Sat is the first clear day.
	in := input(mon, hard("h", mon), hard("h2", thu),
		easy("e1", wed, 3600), easy("e2", fri, 3600), easy("e3", sat, 3600))
	got, ok := weekplan.PlanSwap(in)
	if !ok || got.EasyID != "e3" || got.HardTo != sat {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	// Count the week at the new dates: no two adjacent hard days.
	hardDays := map[string]bool{}
	for _, w := range in.Week {
		d := w.Date
		switch w.ID {
		case got.HardID:
			d = got.HardTo
		case got.EasyID:
			d = got.EasyTo
		}
		if weekplan.Hard(w) {
			hardDays[d] = true
		}
	}
	if !hardDays[sat] || !hardDays[thu] || hardDays[fri] || hardDays[mon] {
		t.Fatalf("hard days after swap: %v", hardDays)
	}
	// Nothing clear at all: no swap.
	in = input(mon, hard("h", mon), hard("h2", thu), easy("e1", wed, 3600), easy("e2", fri, 3600))
	if _, ok := weekplan.PlanSwap(in); ok {
		t.Fatal("must not swap next to a hard day")
	}
}

func TestPlanSwapTodayCountsWithTheEasySession(t *testing.T) {
	// Hard today is moving away, so a hard tomorrow-neighbour on the target's
	// other side is the only thing that matters; the vacated Monday is easy.
	in := input(mon, hard("h", mon), easy("e1", tue, 3600), easy("e2", wed, 3600))
	in.TomorrowReady = true
	in.NoTestDays = map[string]bool{wed: true}
	got, ok := weekplan.PlanSwap(in)
	if !ok || got.EasyID != "e1" || got.HardTo != tue {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestPlanSwapSkipsTheEveOfATestOrCrewRide(t *testing.T) {
	test := easy("t", thu, 3600)
	test.TestProtocol = "ramp"
	in := input(mon, hard("h", mon), easy("e1", wed, 3600), easy("e2", sat, 3600), test)
	got, ok := weekplan.PlanSwap(in)
	if !ok || got.EasyID != "e2" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	crew := easy("c", sat, 3600)
	crew.CrewRideID = "r"
	in = input(mon, hard("h", mon), easy("e1", fri, 3600), crew)
	if _, ok := weekplan.PlanSwap(in); ok {
		t.Fatal("eve of a crew ride must not take the hard session")
	}
}

func TestPlanSwapTieBreaksOnClosestLengthThenID(t *testing.T) {
	// Hard is 3600 s. 3500 is closer than 2400; 5400 is longer than the hard
	// session and is skipped.
	in := input(mon, hard("h", mon), easy("b", wed, 2400), easy("a", wed, 3500), easy("c", wed, 5400))
	got, _ := weekplan.PlanSwap(in)
	if got.EasyID != "a" {
		t.Fatalf("closest length should win: %+v", got)
	}
	in = input(mon, hard("h", mon), easy("b", wed, 3600), easy("a", wed, 3600))
	got, _ = weekplan.PlanSwap(in)
	if got.EasyID != "a" {
		t.Fatalf("lower id should win: %+v", got)
	}
}

func TestPlanSwapNeverPicksTheLongRideOrALongerSession(t *testing.T) {
	long := gen("long", sat, workout.ZoneEndurance, 3*3600)
	long.Name = "Long ride"
	if !scheduler.IsKeySession(long) {
		t.Skip("fixture is not a key session")
	}
	// Tuesday is Today (Caution). Wed is blocked by Thu's hard session, Sat is
	// the long ride, Sun is the only fit.
	in := input(tue, hard("h", tue), easy("wed", wed, 3600), hard("h2", thu), long, easy("sun", sun, 3600))
	got, ok := weekplan.PlanSwap(in)
	if !ok || got.EasyID != "sun" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	// A candidate longer than the hard session is skipped.
	in = input(mon, hard("h", mon), easy("long-easy", wed, 7200))
	if _, ok := weekplan.PlanSwap(in); ok {
		t.Fatal("a longer session must not take the tired day")
	}
}

func TestPlanSwapNeverLandsOnADayThatAlreadyHoldsAHardSession(t *testing.T) {
	test := hard("t", wed)
	test.TestProtocol = "ramp"
	in := input(mon, hard("h", mon), easy("e", wed, 3600), test)
	if _, ok := weekplan.PlanSwap(in); ok {
		t.Fatal("must not stack two hard sessions on one day")
	}
}

func TestNothingToSwapWhenTodayIsNotHardOrDoneOrNotSwappable(t *testing.T) {
	routed := hard("h", mon)
	routed.RouteSlug = "r"
	manual := hard("h", mon)
	for name, in := range map[string]weekplan.SwapInput{
		"today easy":    input(mon, easy("x", mon, 3600), easy("e", wed, 3600)),
		"no candidate":  input(mon, hard("h", mon)),
		"hard only":     input(mon, hard("h", mon), hard("h2", wed)),
		"routed":        input(mon, routed, easy("e", wed, 3600)),
		"nothing today": input(mon, easy("e", wed, 3600)),
	} {
		if _, ok := weekplan.PlanSwap(in); ok {
			t.Errorf("%s: unexpected swap", name)
		}
	}
	done := input(mon, hard("h", mon), easy("e", wed, 3600))
	done.Done = map[string]bool{"h": true}
	if _, ok := weekplan.PlanSwap(done); ok {
		t.Error("done: unexpected swap")
	}
	pushed := input(mon, manual, easy("e", wed, 3600))
	pushed.ManualPushed = map[string]bool{"h": true}
	if _, ok := weekplan.PlanSwap(pushed); ok {
		t.Error("manual copy on hard: unexpected swap")
	}
	pushed = input(mon, hard("h", mon), easy("e", wed, 3600))
	pushed.ManualPushed = map[string]bool{"e": true}
	if _, ok := weekplan.PlanSwap(pushed); ok {
		t.Error("manual copy on candidate: unexpected swap")
	}
	// A candidate beyond Sunday is not a candidate.
	if _, ok := weekplan.PlanSwap(input(mon, hard("h", mon), easy("e", "2026-10-26", 3600))); ok {
		t.Error("next week's session must not be used")
	}
}

func TestSwappableExcludesRoutedTestCrewKeptRunningManualAndAlreadyReshuffled(t *testing.T) {
	base := easy("a", wed, 3600)
	if !weekplan.Swappable(base, nil) {
		t.Fatal("plain generated cycling session should be swappable")
	}
	mut := func(f func(*workout.Workout)) workout.Workout {
		w := base
		f(&w)
		return w
	}
	for name, w := range map[string]workout.Workout{
		"routed":    mut(func(w *workout.Workout) { w.RouteSlug = "r" }),
		"test":      mut(func(w *workout.Workout) { w.TestProtocol = "ramp" }),
		"crew":      mut(func(w *workout.Workout) { w.CrewRideID = "r" }),
		"kept":      mut(func(w *workout.Workout) { w.Flags.Kept = workout.KeptLife }),
		"running":   mut(func(w *workout.Workout) { w.Sport = model.SportRunning }),
		"rider":     mut(func(w *workout.Workout) { w.Flags.Origin = workout.OriginRider }),
		"reshuffle": mut(func(w *workout.Workout) { w.Flags = w.Flags.WithAdaptation(why.Rule("reshuffle_readiness")) }),
		"eased":     mut(func(w *workout.Workout) { w.Flags = w.Flags.WithAdaptation(why.Rule("readiness_ease")) }),
	} {
		if weekplan.Swappable(w, nil) {
			t.Errorf("%s should not be swappable", name)
		}
	}
	if weekplan.Swappable(base, map[string]bool{"a": true}) {
		t.Error("manual push should not be swappable")
	}
}

func TestPlanSwapWithTwoHardSessionsTodayMovesOnlyTheLongerOne(t *testing.T) {
	long := gen("h2", mon, workout.ZoneThreshold, 5400)
	in := input(mon, hard("h1", mon), long, easy("e", wed, 3600))
	got, ok := weekplan.PlanSwap(in)
	if !ok || got.HardID != "h2" || got.EasyID != "e" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}
