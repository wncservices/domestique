package ledger

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/indoor"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var testProfile = workout.RiderProfile{
	FTPWatts:      200,
	AvailableDays: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"},
}

func enduranceWorkout(id, date string, seconds float64) workout.Workout {
	req := scheduler.BuildEnduranceSession(seconds/3600, false, model.SportCycling, testProfile)
	return workout.Workout{
		ID: id, Sport: model.SportCycling, Name: req.Name, GoalID: "g", Date: date,
		Description: req.Description, Steps: req.Steps, Zone: workout.ZoneEndurance,
		Flags: workout.Flags{Origin: workout.OriginPlan},
	}
}

func input(week ...workout.Workout) RepayInput {
	return RepayInput{
		Debt: 100, Week: week, From: "2026-10-15", To: "2026-10-18",
		Profile: testProfile, Phase: periodization.PhaseBuild,
	}
}

func byID(as []Action) map[string]Action {
	m := map[string]Action{}
	for _, a := range as {
		m[a.WorkoutID] = a
	}
	return m
}

func TestRepayLengthensLongestFirstWithinCaps(t *testing.T) {
	long := enduranceWorkout("long", "2026-10-17", 3*3600)
	mid := enduranceWorkout("mid", "2026-10-15", 3600)
	in := input(long, mid)
	in.Debt = 1000
	got := byID(PlanRepayment(in))
	// 3h: +30% is 3240s more, the 2700s cap binds; 1h: +30% = 1080s binds.
	if s := workout.PlannedSeconds(got["long"].Steps); s != 3*3600+2700 {
		t.Fatalf("long = %v s", s)
	}
	if s := workout.PlannedSeconds(got["mid"].Steps); s != 3600*1.3 {
		t.Fatalf("mid = %v s", s)
	}
	if got["long"].Kind != "lengthen" || got["long"].TSSAdded <= 0.5 {
		t.Fatalf("bad action %+v", got["long"])
	}

	// Debt runs out after the first (longest) session.
	in.Debt = got["long"].TSSAdded - 0.1 + MinDebt
	only := PlanRepayment(in)
	if len(only) != 1 || only[0].WorkoutID != "long" {
		t.Fatalf("expected only the longest session, got %+v", only)
	}
}

func TestRepayNeverExceedsDayHours(t *testing.T) {
	in := input(enduranceWorkout("a", "2026-10-15", 4800), enduranceWorkout("b", "2026-10-16", 3600))
	in.Debt = 1000
	in.Profile.HoursPerAvailableDay = 1.5
	for _, a := range PlanRepayment(in) {
		if s := workout.PlannedSeconds(a.Steps); s > 5400 {
			t.Fatalf("%s is %v s, over 5400", a.WorkoutID, s)
		}
	}
}

func TestRepayAfterAvailabilityShrinks(t *testing.T) {
	in := input(enduranceWorkout("a", "2026-10-15", 7200))
	in.Debt = 1000
	in.Profile.HoursPerAvailableDay = 1
	if got := PlanRepayment(in); len(got) != 0 {
		t.Fatalf("expected no actions, got %+v", got)
	}
}

func TestRepayUpgradeOncePerWeekWithSpacing(t *testing.T) {
	// Lengthening is capped away by day hours so the debt stays high.
	mk := func(week ...workout.Workout) RepayInput {
		in := input(week...)
		in.Debt = 100
		in.Profile.HoursPerAvailableDay = 0.5
		return in
	}
	long := enduranceWorkout("long", "2026-10-18", 3*3600)
	a := enduranceWorkout("a", "2026-10-15", 3600)
	b := enduranceWorkout("b", "2026-10-16", 3600)

	got := PlanRepayment(mk(long, a, b))
	if len(got) != 1 || got[0].Kind != "upgrade" || got[0].WorkoutID != "a" {
		t.Fatalf("want one upgrade of a, got %+v", got)
	}
	if got[0].Zone == workout.ZoneEndurance || got[0].TSSAdded <= 0 {
		t.Fatalf("upgrade must be structured and add load: %+v", got[0])
	}

	// A hard neighbour of a (day before) rules a out; b's neighbour is a, fine.
	hard := enduranceWorkout("h", "2026-10-14", 3600)
	hard.Zone, hard.Name = workout.ZoneThreshold, "Threshold"
	got = PlanRepayment(mk(long, a, b, hard))
	if len(got) != 1 || got[0].WorkoutID != "b" {
		t.Fatalf("want upgrade moved to b, got %+v", got)
	}

	// Too little debt for an upgrade.
	low := mk(long, a, b)
	low.Debt = UpgradeDebt
	if got := PlanRepayment(low); len(got) != 0 {
		t.Fatalf("debt at the upgrade bar must not upgrade: %+v", got)
	}

	// A full structured count blocks it.
	full := mk(long, a, b)
	n := len(scheduler.StructuredZones(full.Phase, model.SportCycling, len(testProfile.AvailableDays)))
	for i := 0; i < n; i++ {
		s := enduranceWorkout("s", "2026-10-13", 3600)
		s.ID = "s" + string(rune('0'+i))
		s.Zone = workout.ZoneThreshold
		full.Week = append(full.Week, s)
	}
	if got := PlanRepayment(full); len(got) != 0 {
		t.Fatalf("full structured count must block the upgrade: %+v", got)
	}
}

func TestRepayLeavesEasedRoutedKeptTestCrewAndBlackoutAlone(t *testing.T) {
	eased := enduranceWorkout("eased", "2026-10-15", 3600)
	eased.Flags = eased.Flags.WithAdaptation(why.ReadinessRest)
	routed := enduranceWorkout("routed", "2026-10-15", 3600)
	routed.RouteSlug = "r"
	test := enduranceWorkout("test", "2026-10-15", 3600)
	test.TestProtocol = "ramp"
	crew := enduranceWorkout("crew", "2026-10-15", 3600)
	crew.CrewRideID = "c"
	blacked := enduranceWorkout("black", "2026-10-16", 3600)
	repaid := enduranceWorkout("repaid", "2026-10-17", 3600)
	repaid.Flags = repaid.Flags.WithAdaptation(why.LoadRepaid)
	ok := enduranceWorkout("ok", "2026-10-18", 3600)

	in := input(eased, routed, test, crew, blacked, repaid, ok)
	in.Debt = 1000
	in.Blackout = map[string]bool{"2026-10-16": true}
	got := PlanRepayment(in)
	if len(got) != 1 || got[0].WorkoutID != "ok" {
		t.Fatalf("only ok may change, got %+v", got)
	}
}

func TestRepayNothingInRecoveryOrTaperOrUnderMinDebt(t *testing.T) {
	w := enduranceWorkout("a", "2026-10-15", 3600)
	r := input(w)
	r.Recovery = true
	if got := PlanRepayment(r); got != nil {
		t.Fatalf("recovery: %+v", got)
	}
	tp := input(w)
	tp.Phase = periodization.PhaseTaper
	if got := PlanRepayment(tp); got != nil {
		t.Fatalf("taper: %+v", got)
	}
	d := input(w)
	d.Debt = MinDebt
	if got := PlanRepayment(d); got != nil {
		t.Fatalf("min debt: %+v", got)
	}
}

func TestRepayOutsideWindowIsLeftAlone(t *testing.T) {
	today := enduranceWorkout("today", "2026-10-14", 3600)
	in := input(today)
	in.Debt = 1000
	if got := PlanRepayment(in); len(got) != 0 {
		t.Fatalf("a session before From must not change: %+v", got)
	}
}

func TestRepayIndoorRideCappedByTrainerDoesNotLoop(t *testing.T) {
	// An indoor ride is already trimmed to the trainer's two-hour main-work cap,
	// so lengthening the outdoor ride adds nothing once converted.
	out := enduranceWorkout("in", "2026-10-15", 4*3600)
	outdoor := out.Steps
	res, err := indoor.Convert(out, testProfile)
	if err != nil {
		t.Fatal(err)
	}
	out.Steps, out.Indoor, out.OutdoorSteps = res.Steps, true, &outdoor
	if workout.PlannedSeconds(out.Steps) >= workout.PlannedSeconds(outdoor) {
		t.Fatal("fixture must be trimmed by the trainer conversion")
	}
	in := input(out)
	in.Debt = 1000
	if got := PlanRepayment(in); len(got) != 0 {
		t.Fatalf("a trainer-capped ride gains nothing and must be skipped: %+v", got)
	}
}

func TestRepayUpgradeIsSkippedWhenTheWeekAlreadyHadOne(t *testing.T) {
	long := enduranceWorkout("long", "2026-10-18", 3*3600)
	a := enduranceWorkout("a", "2026-10-15", 3600)
	in := input(long, a)
	in.Debt = 100
	in.Profile.HoursPerAvailableDay = 0.5
	if got := PlanRepayment(in); len(got) != 1 || got[0].Kind != "upgrade" {
		t.Fatalf("setup: want an upgrade, got %+v", got)
	}
	in.UpgradedAlready = true
	if got := PlanRepayment(in); len(got) != 0 {
		t.Fatalf("the week's upgrade was already made: %+v", got)
	}
}

// An upgrade rewrites the description, which would erase a rider's move note.
func TestRepayNeverUpgradesARiderMovedSession(t *testing.T) {
	long := enduranceWorkout("long", "2026-10-18", 3*3600)
	a := enduranceWorkout("a", "2026-10-15", 3600)
	a.Flags.MovedFrom, a.Flags.MovedBy = "2026-10-13", workout.MovedByRider
	in := input(long, a)
	in.Debt = 100
	in.Profile.HoursPerAvailableDay = 0.5
	if got := PlanRepayment(in); len(got) != 0 {
		t.Fatalf("a moved session was upgraded: %+v", got)
	}
	// Lengthening keeps the description, so it stays allowed.
	in.Profile.HoursPerAvailableDay = 0
	in.Debt = 10
	if got := PlanRepayment(in); len(got) == 0 || got[0].Kind != "lengthen" {
		t.Fatalf("a moved session may still be lengthened: %+v", got)
	}
}

func TestRepayUpgradeKeepsAwayFromTestAndCrewDays(t *testing.T) {
	long := enduranceWorkout("long", "2026-10-18", 3*3600)
	a := enduranceWorkout("a", "2026-10-15", 3600)
	for name, mark := range map[string]func(*workout.Workout){
		"test": func(w *workout.Workout) { w.TestProtocol = "ramp" },
		"crew": func(w *workout.Workout) { w.CrewRideID = "c" },
	} {
		n := enduranceWorkout("n", "2026-10-14", 3600)
		mark(&n)
		in := input(long, a, n)
		in.Debt = 100
		in.Profile.HoursPerAvailableDay = 0.5
		if got := PlanRepayment(in); len(got) != 0 {
			t.Errorf("%s the day before: upgraded anyway %+v", name, got)
		}
	}
}
