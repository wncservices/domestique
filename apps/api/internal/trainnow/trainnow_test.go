package trainnow

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

var profile = workout.RiderProfile{FTPWatts: 250}

var picker = []int{30, 45, 60, 75, 90, 120, 150, 180}

func level(l float64) func(string) float64 { return func(string) float64 { return l } }

// planSession is a plan-made structured session on rung lvl of the ladder.
func planSession(t *testing.T, sport model.Sport, zone string, lvl int) *workout.Workout {
	t.Helper()
	l, ok := workoutlib.LadderFor(sport, zone)
	if !ok {
		t.Fatalf("no ladder for %s %s", sport, zone)
	}
	req := workoutlib.Instantiate(l, l.Rungs[lvl-1], profile)
	return &workout.Workout{
		ID: "today", Rider: "r", Sport: sport, Name: req.Name, GoalID: "g", Date: "2026-03-25",
		Description: scheduler.GeneratedDescription, Steps: req.Steps, Zone: req.Zone, Level: req.Level,
	}
}

// enduranceSession is a plan-made endurance ride of the given length.
func enduranceSession(minutes float64, long bool) *workout.Workout {
	req := scheduler.BuildEnduranceSession(minutes/60, long, model.SportCycling, profile)
	return &workout.Workout{
		ID: "today", Rider: "r", Sport: model.SportCycling, Name: req.Name, GoalID: "g", Date: "2026-03-25",
		Description: scheduler.GeneratedDescription, Steps: req.Steps, Zone: req.Zone,
	}
}

func base(minutes int) Input {
	return Input{
		Minutes: minutes, Level: level(5), Profile: profile, Sport: model.SportCycling,
		HasPlan: true, Verdict: readiness.Ready,
	}
}

func byKind(s []Suggestion, k Kind) (Suggestion, bool) {
	for _, x := range s {
		if x.Kind == k {
			return x, true
		}
	}
	return Suggestion{}, false
}

func kinds(s []Suggestion) string {
	var out []string
	for _, x := range s {
		out = append(out, string(x.Kind))
	}
	return strings.Join(out, ",")
}

func TestEverySuggestionFitsEveryPickerValueOnEveryLadder(t *testing.T) {
	type key struct {
		sport model.Sport
		zone  string
	}
	keys := []key{
		{model.SportCycling, "tempo"}, {model.SportCycling, "sweet_spot"}, {model.SportCycling, "threshold"},
		{model.SportCycling, "vo2max"}, {model.SportCycling, "anaerobic"},
		{model.SportRunning, "tempo"}, {model.SportRunning, "threshold"}, {model.SportRunning, "intervals"},
	}
	for _, k := range keys {
		for _, n := range picker {
			for _, lv := range []float64{1, 4.3, 7, 10} {
				for _, verdict := range []readiness.Verdict{readiness.Ready, readiness.Caution, readiness.Rest} {
					in := base(n)
					in.Sport, in.Level, in.Verdict = k.sport, level(lv), verdict
					in.Today = planSession(t, k.sport, k.zone, 6)
					in.PlannedIsToday = true
					in.WeekZones = []string{k.zone, "sweet_spot", "threshold", "tempo", "intervals"}
					out := Suggest(in)
					if len(out) == 0 {
						t.Errorf("%v N=%d L=%v %s: no suggestions, easy is always available", k, n, lv, verdict)
					}
					for _, s := range out {
						if s.Seconds > float64(n)*60 {
							t.Errorf("%v N=%d L=%v %s: %s is %v s, over the time", k, n, lv, verdict, s.Kind, s.Seconds)
						}
						if got := workout.PlannedSeconds(s.Steps); got != s.Seconds {
							t.Errorf("%v N=%d %s: steps %v s, suggestion says %v s", k, n, s.Kind, got, s.Seconds)
						}
						if len(s.Steps) == 0 || s.Name == "" {
							t.Errorf("%v N=%d %s: empty suggestion %+v", k, n, s.Kind, s)
						}
					}
				}
			}
		}
	}
}

func TestWhenEvenRungOneDoesNotFitItBecomesAnEnduranceRide(t *testing.T) {
	// cycling anaerobic rung 1 is 41 minutes: it cannot fit 30.
	in := base(30)
	in.Today = planSession(t, model.SportCycling, "anaerobic", 3)
	in.PlannedIsToday = true
	out := Suggest(in)
	p, ok := byKind(out, Planned)
	if !ok {
		t.Fatalf("kinds = %s, want a planned suggestion", kinds(out))
	}
	if p.Zone != workout.ZoneEndurance || p.Level != 0 || p.Seconds != 30*60 {
		t.Errorf("planned = %+v, want a 30-minute endurance ride", p)
	}
	// Never trimmed below the fixed warmup and cooldown.
	if p.Seconds < workoutlib.WarmupCooldownSeconds {
		t.Errorf("ride is %v s, shorter than the warmup and cooldown", p.Seconds)
	}
}

func TestAnEnduranceStandInIsCappedAtOneAndAQuarterOfTheSessionButEasyTakesAllOfN(t *testing.T) {
	in := base(180)
	in.Today = enduranceSession(60, false)
	in.PlannedIsToday = true
	out := Suggest(in)
	p, _ := byKind(out, Planned)
	e, ok := byKind(out, Easy)
	if p.Seconds != 75*60 {
		t.Errorf("planned = %v s, want 75 min (1.25 x 60): 180 minutes must not turn an easy hour into three", p.Seconds)
	}
	if !ok || e.Seconds != 180*60 {
		t.Errorf("easy = %+v, want all 180 minutes", e)
	}

	// Under the cap it is N exactly.
	in = base(45)
	in.Today = enduranceSession(60, false)
	in.PlannedIsToday = true
	if p, _ := byKind(Suggest(in), Planned); p.Seconds != 45*60 {
		t.Errorf("planned at N=45 = %v s, want 45 min", p.Seconds)
	}
}

func TestPlannedIsTodaysSessionAtTheRidersLevelFittedToTheTime(t *testing.T) {
	in := base(180)
	in.Level = level(6)
	in.Today = planSession(t, model.SportCycling, "threshold", 3)
	in.PlannedIsToday = true
	p, ok := byKind(Suggest(in), Planned)
	if !ok {
		t.Fatal("no planned suggestion")
	}
	// Its zone at level L, not the session's own rung.
	if p.Zone != workout.ZoneThreshold || p.Level != 6 || p.Difficulty != "Productive" {
		t.Errorf("planned = %+v, want threshold rung 6, Productive", p)
	}
	if p.TSS <= 0 || !strings.Contains(p.Why, "Today") {
		t.Errorf("planned = %+v, want a TSS and a why about today's plan", p)
	}

	in.PlannedIsToday = false
	if p, _ := byKind(Suggest(in), Planned); !strings.Contains(strings.ToLower(p.Why), "next") {
		t.Errorf("why = %q, want it to say this is the next session on the plan", p.Why)
	}
}

func TestATestOrNoSessionIsNeverAPlannedBase(t *testing.T) {
	in := base(60)
	if _, ok := byKind(Suggest(in), Planned); ok {
		t.Error("planned suggested with no session")
	}
	test := planSession(t, model.SportCycling, "threshold", 4)
	test.TestProtocol = "ramp"
	in.Today = test
	in.PlannedIsToday = true
	if _, ok := byKind(Suggest(in), Planned); ok {
		t.Error("an FTP test was used as the base of a planned suggestion")
	}
	riderBuilt := planSession(t, model.SportCycling, "threshold", 4)
	riderBuilt.GoalID, riderBuilt.Description = "", "mine"
	in.Today = riderBuilt
	if _, ok := byKind(Suggest(in), Planned); ok {
		t.Error("a rider-built session was used as the base of a planned suggestion")
	}
}

func TestWantedIsTheFirstWeekZoneNotDoneAndNotPlanned(t *testing.T) {
	in := base(90)
	in.Today = planSession(t, model.SportCycling, "vo2max", 4)
	in.PlannedIsToday = true
	in.WeekZones = []string{"threshold", "vo2max", "sweet_spot"}
	in.DoneZones = map[string]bool{"threshold": true}
	w, ok := byKind(Suggest(in), Wanted)
	if !ok || w.Zone != "sweet_spot" || w.Level != 5 {
		t.Fatalf("wanted = %+v (%v), want sweet_spot at level 5: threshold is done and vo2max is the planned one", w, ok)
	}

	in.DoneZones = map[string]bool{"threshold": true, "sweet_spot": true}
	if _, ok := byKind(Suggest(in), Wanted); ok {
		t.Error("wanted suggested with every other zone done")
	}

	// Plan order, not alphabetical.
	in.DoneZones = nil
	in.Today = nil
	in.WeekZones = []string{"vo2max", "anaerobic", "tempo"}
	if w, _ := byKind(Suggest(in), Wanted); w.Zone != "vo2max" {
		t.Errorf("wanted = %q, want the first in plan order (vo2max)", w.Zone)
	}
}

func TestDuplicatesOnZoneLevelAndMinutesAreDropped(t *testing.T) {
	// A 60-minute endurance day at N=60: planned and easy are the same ride.
	in := base(60)
	in.Today = enduranceSession(60, false)
	in.PlannedIsToday = true
	out := Suggest(in)
	if len(out) != 1 || out[0].Kind != Planned {
		t.Errorf("suggestions = %s, want the one ride, kept as planned", kinds(out))
	}
}

func TestOrderIsPlannedWantedEasy(t *testing.T) {
	in := base(90)
	in.Today = planSession(t, model.SportCycling, "threshold", 5)
	in.PlannedIsToday = true
	in.WeekZones = []string{"threshold", "sweet_spot"}
	if got := kinds(Suggest(in)); got != "planned,wanted,easy" {
		t.Errorf("kinds = %s, want planned,wanted,easy", got)
	}
}

func TestEasyIsAnEnduranceRideOfNMinutesLabelledRecovery(t *testing.T) {
	e, ok := byKind(Suggest(base(75)), Easy)
	if !ok {
		t.Fatal("no easy suggestion")
	}
	if e.Zone != workout.ZoneEndurance || e.Level != 0 || e.Seconds != 75*60 || e.Difficulty != "Recovery" {
		t.Errorf("easy = %+v, want 75 min endurance, Recovery", e)
	}
}

func TestRestGivesOnlyAnEasyRideCappedAtAnHourWithTheReasons(t *testing.T) {
	in := base(120)
	in.Verdict = readiness.Rest
	in.ReadinessReasons = []string{"HRV is low", "slept badly"}
	in.Today = planSession(t, model.SportCycling, "threshold", 5)
	in.PlannedIsToday = true
	in.WeekZones = []string{"threshold", "sweet_spot"}
	out := Suggest(in)
	if len(out) != 1 || out[0].Kind != Easy || out[0].Seconds != 60*60 {
		t.Fatalf("suggestions = %+v, want one easy ride of 60 min", out)
	}
	if !strings.Contains(out[0].Why, "HRV is low") || !strings.Contains(out[0].Why, "slept badly") {
		t.Errorf("why = %q, want the readiness reasons", out[0].Why)
	}
	in.Minutes = 45
	if out := Suggest(in); out[0].Seconds != 45*60 {
		t.Errorf("under the cap N is kept: %v s", out[0].Seconds)
	}
}

func TestCautionUsesOneRungDownAndDropsWanted(t *testing.T) {
	in := base(180)
	in.Verdict = readiness.Caution
	in.Level = level(5)
	in.Today = planSession(t, model.SportCycling, "threshold", 5)
	in.PlannedIsToday = true
	in.WeekZones = []string{"threshold", "sweet_spot"}
	out := Suggest(in)
	if got := kinds(out); got != "planned,easy" {
		t.Fatalf("kinds = %s, want planned,easy (wanted dropped)", got)
	}
	p, _ := byKind(out, Planned)
	if p.Level != 4 {
		t.Errorf("planned level = %v, want L-1 = 4", p.Level)
	}
	if p.Difficulty != "Achievable" {
		t.Errorf("difficulty = %q, want it read against the real level 5 (Achievable)", p.Difficulty)
	}

	// The floor is rung 1.
	in.Level = level(1)
	in.Today = planSession(t, model.SportCycling, "threshold", 5)
	if p, _ := byKind(Suggest(in), Planned); p.Level != 1 {
		t.Errorf("planned level at L=1 under caution = %v, want the floor of 1", p.Level)
	}
}

func TestTwoHardDaysInSevenDropWantedAndWarnOnPlanned(t *testing.T) {
	in := base(90)
	in.Today = planSession(t, model.SportCycling, "threshold", 5)
	in.PlannedIsToday = true
	in.WeekZones = []string{"threshold", "sweet_spot"}

	in.HardDaysLast7 = 1
	out := Suggest(in)
	if p, _ := byKind(out, Planned); p.Warning != "" {
		t.Errorf("one hard day warned: %q", p.Warning)
	}
	if _, ok := byKind(out, Wanted); !ok {
		t.Error("wanted dropped after a single hard day")
	}

	in.HardDaysLast7 = 2
	out = Suggest(in)
	if _, ok := byKind(out, Wanted); ok {
		t.Error("wanted offered as a third hard day in 7")
	}
	p, ok := byKind(out, Planned)
	if !ok || p.Warning != "A third hard day in 7" {
		t.Errorf("planned = %+v (%v), want the plan's own day kept with the warning", p, ok)
	}

	// An endurance day is not a hard day, so there is nothing to warn about.
	in.Today = enduranceSession(90, false)
	if p, _ := byKind(Suggest(in), Planned); p.Warning != "" {
		t.Errorf("an endurance planned ride was warned: %q", p.Warning)
	}

	// Under caution the load rule is moot.
	in.Today = planSession(t, model.SportCycling, "threshold", 5)
	in.Verdict = readiness.Caution
	if p, _ := byKind(Suggest(in), Planned); p.Warning != "" {
		t.Errorf("caution also carried the load warning: %q", p.Warning)
	}
}

func TestNoPlanGivesOnlyEasy(t *testing.T) {
	in := base(90)
	in.HasPlan = false
	in.Today = planSession(t, model.SportCycling, "threshold", 5)
	in.WeekZones = []string{"threshold"}
	out := Suggest(in)
	if len(out) != 1 || out[0].Kind != Easy {
		t.Errorf("suggestions = %s, want easy only", kinds(out))
	}
}

func TestNoWellnessDataIsTheSameAsReady(t *testing.T) {
	a := base(90)
	a.Today = planSession(t, model.SportCycling, "threshold", 5)
	a.WeekZones = []string{"threshold", "sweet_spot"}
	b := a
	b.Verdict = ""
	if got, want := kinds(Suggest(b)), kinds(Suggest(a)); got != want {
		t.Errorf("empty verdict gave %s, ready gave %s", got, want)
	}
}

func TestRunningUsesRunningLadders(t *testing.T) {
	in := base(90)
	in.Sport = model.SportRunning
	in.Today = planSession(t, model.SportRunning, "threshold", 4)
	in.PlannedIsToday = true
	in.WeekZones = []string{"threshold", "intervals"}
	out := Suggest(in)
	w, ok := byKind(out, Wanted)
	if !ok || w.Zone != "intervals" || !strings.Contains(w.Name, "Intervals") {
		t.Errorf("wanted = %+v (%v), want the running intervals ladder", w, ok)
	}
}
