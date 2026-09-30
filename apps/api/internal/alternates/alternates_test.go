package alternates

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

var profile = workout.RiderProfile{FTPWatts: 200}

// planned is a plan-made structured session on rung level of the sport's zone
// ladder, dated in the future relative to the fixed "today" the tests use.
func planned(t *testing.T, sport model.Sport, zone string, level int) workout.Workout {
	t.Helper()
	l, ok := workoutlib.LadderFor(sport, zone)
	if !ok {
		t.Fatalf("no ladder for %s %s", sport, zone)
	}
	req := workoutlib.Instantiate(l, l.Rungs[level-1], profile)
	return workout.Workout{
		ID: "w1", Rider: "r", Sport: sport, Name: req.Name, GoalID: "g", Date: "2026-10-05",
		Description: scheduler.GeneratedDescription, Steps: req.Steps, Zone: req.Zone, Level: req.Level,
	}
}

// endurance is a plan-made endurance or long ride of totalSeconds.
func endurance(sport model.Sport, totalSeconds float64, long bool) workout.Workout {
	req := scheduler.BuildEnduranceSession(totalSeconds/3600, long, sport, profile)
	return workout.Workout{
		ID: "w1", Rider: "r", Sport: sport, Name: req.Name, GoalID: "g", Date: "2026-10-05",
		Description: scheduler.GeneratedDescription, Steps: req.Steps, Zone: req.Zone,
	}
}

func find(opts []Option, k Kind) (Option, bool) {
	for _, o := range opts {
		if o.Kind == k {
			return o, true
		}
	}
	return Option{}, false
}

func kinds(opts []Option) []Kind {
	var out []Kind
	for _, o := range opts {
		out = append(out, o.Kind)
	}
	return out
}

func TestRungOneHasNoEasier(t *testing.T) {
	w := planned(t, model.SportCycling, "threshold", 1)
	opts := Options(w, 4, false, profile)
	if _, ok := find(opts, Easier); ok {
		t.Fatalf("rung 1 offered an easier option: %v", kinds(opts))
	}
	w = planned(t, model.SportCycling, "threshold", 2)
	easier, ok := find(Options(w, 4, false, profile), Easier)
	if !ok || easier.Level != 1 {
		t.Fatalf("rung 2 easier = %+v, %v; want rung 1", easier, ok)
	}
}

func TestHarderIsCappedAtTheRidersLevelPlusOneNotTheSessions(t *testing.T) {
	// L 4.3 gives a cap of floor(4.3)+1 = 5.
	cases := []struct {
		cur       int
		wantLevel float64 // 0 = not offered
	}{
		{3, 4}, // below the rider: one rung up
		{4, 5}, // at the rider: up to the cap
		{5, 0}, // already at the cap: nothing
		{6, 0}, // past the cap (rider-set): nothing
	}
	for _, c := range cases {
		w := planned(t, model.SportCycling, "threshold", c.cur)
		harder, ok := find(Options(w, 4.3, false, profile), Harder)
		if c.wantLevel == 0 {
			if ok {
				t.Errorf("cur %d: harder offered at %v, want none", c.cur, harder.Level)
			}
			continue
		}
		if !ok || harder.Level != c.wantLevel {
			t.Errorf("cur %d: harder = %v (%v), want %v", c.cur, harder.Level, ok, c.wantLevel)
		}
	}
}

func TestASessionBelowTheRiderCanBeSwappedBackUpToTheCap(t *testing.T) {
	// A rider at 6.2 has a cap of floor(6.2)+1 = 7: from rung 6 harder is
	// rung 7, from rung 7 there is nothing further.
	w := planned(t, model.SportCycling, "threshold", 7)
	if _, ok := find(Options(w, 6.2, false, profile), Harder); ok {
		t.Error("harder offered past the cap of 7")
	}
	w = planned(t, model.SportCycling, "threshold", 6)
	h, ok := find(Options(w, 6.2, false, profile), Harder)
	if !ok || h.Level != 7 {
		t.Errorf("harder from 6 = %v (%v), want 7", h.Level, ok)
	}
}

func TestHarderStopsAtTheTopOfTheLadder(t *testing.T) {
	w := planned(t, model.SportCycling, "threshold", 10)
	if _, ok := find(Options(w, 12, false, profile), Harder); ok {
		t.Error("there is no rung 11")
	}
}

// Cycling threshold totals in minutes by rung: 50 50 56 68 71 76 80 86 98 96.
// Vo2max: 36 40 50 56 60 62 65 74 75 80.

func TestShorterAndLongerThresholdsAreExactly75And125Percent(t *testing.T) {
	// vo2max rung 10 is 80 min; 75 % is exactly 60, which rung 5 is.
	w := planned(t, model.SportCycling, "vo2max", 10)
	shorter, ok := find(Options(w, 10, false, profile), Shorter)
	if !ok || shorter.Level != 5 || shorter.Seconds != 60*60 {
		t.Fatalf("shorter of vo2max 10 = %+v (%v), want rung 5 at exactly 75 %%", shorter, ok)
	}
	// vo2max rung 2 is 40 min; 125 % is exactly 50, which rung 3 is.
	w = planned(t, model.SportCycling, "vo2max", 2)
	longer, ok := find(Options(w, 2, false, profile), Longer)
	if !ok || longer.Level != 3 || longer.Seconds != 50*60 {
		t.Fatalf("longer of vo2max 2 = %+v (%v), want rung 3 at exactly 125 %%", longer, ok)
	}
	// One rung short of the threshold does not count: threshold rung 4 is 68
	// min, 125 % is 85, and rung 8 (86) is the first that reaches it.
	w = planned(t, model.SportCycling, "threshold", 4)
	longer, ok = find(Options(w, 8, false, profile), Longer)
	if !ok || longer.Level != 8 {
		t.Fatalf("longer of threshold 4 = %+v (%v), want rung 8", longer, ok)
	}
}

func TestLongerIsWithinTheHarderCap(t *testing.T) {
	// vo2max rung 2, rider at 1.9: cap 2, so rung 3 (the only 125 % rung
	// nearby) is beyond it and nothing at or below the cap is long enough.
	w := planned(t, model.SportCycling, "vo2max", 2)
	if longer, ok := find(Options(w, 1.9, false, profile), Longer); ok {
		t.Fatalf("longer offered past the cap: %+v", longer)
	}
}

func TestShorterIsTheLongestQualifyingRungAtOrBelowTheCurrentLevel(t *testing.T) {
	// threshold rung 7 is 80 min; 75 % is 60. Rungs 1-3 (50, 50, 56) qualify.
	// The longest is rung 3 (56), not the highest-level or the first.
	w := planned(t, model.SportCycling, "threshold", 7)
	shorter, ok := find(Options(w, 7, false, profile), Shorter)
	if !ok || shorter.Level != 3 {
		t.Fatalf("shorter = %+v (%v), want rung 3", shorter, ok)
	}
	// threshold rung 5 is 71 min; 75 % is 53.25. Rungs 1 and 2 tie at 50; the
	// higher level wins the tie.
	w = planned(t, model.SportCycling, "threshold", 5)
	shorter, ok = find(Options(w, 5, false, profile), Shorter)
	if !ok || shorter.Level != 2 {
		t.Fatalf("shorter tie = %+v (%v), want rung 2", shorter, ok)
	}
}

func TestOptionsWithNoRungBehindThemAreOmitted(t *testing.T) {
	// threshold rung 1 (50): no easier, and nothing shorter or lower.
	w := planned(t, model.SportCycling, "threshold", 1)
	opts := Options(w, 1, false, profile)
	for _, k := range []Kind{Easier, Shorter} {
		if _, ok := find(opts, k); ok {
			t.Errorf("%s offered on rung 1", k)
		}
	}
	// threshold rung 10 (96) for a rider at 10: no harder, no longer.
	w = planned(t, model.SportCycling, "threshold", 10)
	opts = Options(w, 10, false, profile)
	for _, k := range []Kind{Harder, Longer} {
		if _, ok := find(opts, k); ok {
			t.Errorf("%s offered on rung 10", k)
		}
	}
}

func TestRecoveryAndTaperWeeksNeverOfferHarderOrAStructuredLonger(t *testing.T) {
	// A rung with all four available in an ordinary week: vo2max 4 (56 min)
	// at L 8 has easier, harder, shorter (rung 1, 36) and longer (rung 7, 65 is
	// under 70, rung 8 is 74).
	w := planned(t, model.SportCycling, "vo2max", 4)
	ordinary := Options(w, 8, false, profile)
	for _, k := range []Kind{Easier, Harder, Shorter, Longer} {
		if _, ok := find(ordinary, k); !ok {
			t.Fatalf("setup: %s not offered in an ordinary week: %v", k, kinds(ordinary))
		}
	}
	reduced := Options(w, 8, true, profile)
	for _, k := range []Kind{Harder, Longer} {
		if _, ok := find(reduced, k); ok {
			t.Errorf("%s offered in a recovery or taper week", k)
		}
	}
	for _, k := range []Kind{Easier, Shorter} {
		a, _ := find(ordinary, k)
		b, ok := find(reduced, k)
		if !ok || b.Level != a.Level || b.Seconds != a.Seconds {
			t.Errorf("%s changed in a reduced week: %+v vs %+v", k, a, b)
		}
	}
}

func TestEnduranceLongerStillScalesInAReducedWeek(t *testing.T) {
	w := endurance(model.SportCycling, 2*3600, false)
	ordinary := Options(w, 5, false, profile)
	reduced := Options(w, 5, true, profile)
	if len(reduced) != 2 || len(ordinary) != 2 {
		t.Fatalf("options ordinary %v reduced %v, want shorter and longer both times", kinds(ordinary), kinds(reduced))
	}
	if _, ok := find(reduced, Longer); !ok {
		t.Error("endurance longer must still be offered in a recovery or taper week")
	}
}

func TestEnduranceScalesTheMainStepAndOffersNoEasierOrHarder(t *testing.T) {
	w := endurance(model.SportCycling, 2*3600, false) // main step 100 min
	opts := Options(w, 5, false, profile)
	if got := kinds(opts); len(got) != 2 || got[0] != Shorter || got[1] != Longer {
		t.Fatalf("kinds = %v, want [shorter longer]", got)
	}
	shorter, longer := opts[0], opts[1]
	// 20 min fixed + 75 % of 100 = 95 min; + 125 % = 145 min.
	if shorter.Seconds != 95*60 || longer.Seconds != 145*60 {
		t.Errorf("seconds = %v / %v, want 5700 / 8700", shorter.Seconds, longer.Seconds)
	}
	if got := workout.PlannedSeconds(shorter.Steps); got != shorter.Seconds {
		t.Errorf("shorter steps sum to %v, option says %v", got, shorter.Seconds)
	}
	if shorter.Zone != workout.ZoneEndurance || shorter.Level != 0 || shorter.Difficulty != LabelAchievable {
		t.Errorf("endurance option = %+v, want zone endurance, no level, Achievable", shorter)
	}
	if shorter.Name != w.Name {
		t.Errorf("name = %q, want %q kept", shorter.Name, w.Name)
	}
}

func TestEnduranceRoundsToFiveMinutes(t *testing.T) {
	// total 95 min, main 75: 75 % is 56.25 -> total 76.25 -> 75 min.
	w := endurance(model.SportCycling, 95*60, false)
	shorter, ok := find(Options(w, 5, false, profile), Shorter)
	if !ok || shorter.Seconds != 75*60 {
		t.Fatalf("shorter = %+v (%v), want 75 min", shorter, ok)
	}
}

func TestEnduranceFloorAndCeiling(t *testing.T) {
	// A 30-minute ride is already at the floor: nothing shorter.
	w := endurance(model.SportCycling, 30*60, false)
	if _, ok := find(Options(w, 5, false, profile), Shorter); ok {
		t.Error("shorter offered on a ride already at the 30-minute floor")
	}
	// A 40-minute ride goes to 35, not below the floor.
	w = endurance(model.SportCycling, 40*60, false)
	shorter, ok := find(Options(w, 5, false, profile), Shorter)
	if !ok || shorter.Seconds != 35*60 {
		t.Errorf("shorter of 40 min = %+v (%v), want 35 min", shorter, ok)
	}
	// A 5-hour ride is capped at 6 hours, not scaled past it.
	w = endurance(model.SportCycling, 5*3600, true)
	longer, ok := find(Options(w, 5, false, profile), Longer)
	if !ok || longer.Seconds != 6*3600 {
		t.Errorf("longer of 5h = %+v (%v), want 6h", longer, ok)
	}
	if longer.Name != "Long ride" {
		t.Errorf("a long ride lost its name: %q", longer.Name)
	}
	// A 6-hour ride cannot go longer.
	w = endurance(model.SportCycling, 6*3600, true)
	if _, ok := find(Options(w, 5, false, profile), Longer); ok {
		t.Error("longer offered on a ride already at the 6-hour ceiling")
	}
}

func TestRunningEnduranceKeepsItsName(t *testing.T) {
	w := endurance(model.SportRunning, 90*60, false)
	shorter, ok := find(Options(w, 5, false, workout.RiderProfile{}), Shorter)
	if !ok || shorter.Name != w.Name {
		t.Fatalf("shorter = %+v (%v), want name %q", shorter, ok, w.Name)
	}
}

func TestAnIndoorSessionsAlternatesAreComputedFromItsOutdoorForm(t *testing.T) {
	outdoor := endurance(model.SportCycling, 2*3600, false)
	indoorSteps := endurance(model.SportCycling, 90*60, false).Steps // the shortened trainer version
	w := outdoor
	w.Indoor = true
	w.OutdoorSteps = &outdoor.Steps
	w.Steps = indoorSteps
	opts := Options(w, 5, false, profile)
	shorter, _ := find(opts, Shorter)
	if shorter.Seconds != 95*60 {
		t.Errorf("shorter = %v s, want 5700 (from the 2 h outdoor form, not the 90 min indoor one)", shorter.Seconds)
	}
}

func TestDifficultyBoundaries(t *testing.T) {
	cases := []struct {
		rung, level float64
		want        string
	}{
		{5, 7, LabelRecovery},     // d = -2
		{5, 9, LabelRecovery},     // d = -4
		{5, 6.9, LabelAchievable}, // d just above -2
		{5, 5.5, LabelAchievable}, // d = -0.5
		{5, 5.4, LabelProductive}, // d just above -0.5
		{5, 5, LabelProductive},   // d = 0
		{5, 4.6, LabelProductive}, // d = 0.4
		{5, 4.5, LabelStretch},    // d = 0.5
		{5, 4.01, LabelStretch},   // d just under 1
		{5, 4, LabelBreakthrough}, // d = 1
		{5, 2, LabelBreakthrough}, // d = 3
	}
	for _, c := range cases {
		if got := Difficulty(c.rung, c.level); got != c.want {
			t.Errorf("Difficulty(%v, %v) = %q, want %q", c.rung, c.level, got, c.want)
		}
	}
}

func TestOptionsCarryDifficultyAndPlannedTSS(t *testing.T) {
	w := planned(t, model.SportCycling, "threshold", 4)
	harder, ok := find(Options(w, 4, false, profile), Harder)
	if !ok {
		t.Fatal("no harder option")
	}
	if harder.Difficulty != LabelBreakthrough {
		t.Errorf("difficulty = %q, want Breakthrough for one rung above the rider", harder.Difficulty)
	}
	if harder.TSS <= 0 {
		t.Errorf("tss = %v, want a planned TSS with an FTP", harder.TSS)
	}
	noFTP := Options(w, 4, false, workout.RiderProfile{})
	for _, o := range noFTP {
		if o.TSS != 0 {
			t.Errorf("%s tss = %v without an FTP, want 0", o.Kind, o.TSS)
		}
	}
}

func TestNothingIsOfferedForWhatIsNotAPlannedSession(t *testing.T) {
	base := planned(t, model.SportCycling, "threshold", 5)
	cases := map[string]func(*workout.Workout){
		"an FTP test":        func(w *workout.Workout) { w.TestProtocol = "ramp" },
		"a rider-built one":  func(w *workout.Workout) { w.GoalID = ""; w.Description = "my own" },
		"no generated text":  func(w *workout.Workout) { w.Description = "my own" },
		"a zone-less legacy": func(w *workout.Workout) { w.Zone = "" },
		"an unladdered zone": func(w *workout.Workout) { w.Zone = workout.ZoneIntervals }, // no cycling ladder
		"a zero level":       func(w *workout.Workout) { w.Level = 0 },
	}
	for name, mutate := range cases {
		w := base
		mutate(&w)
		if got := Options(w, 5, false, profile); got != nil {
			t.Errorf("%s: got options %v, want nil", name, kinds(got))
		}
	}
}

func TestAvailableNeedsAnUnriddenSessionDatedTodayOrLater(t *testing.T) {
	w := planned(t, model.SportCycling, "threshold", 5)
	w.Date = "2026-10-05"
	if !Available(w, "2026-10-05", false) {
		t.Error("today's undone session should be available")
	}
	if !Available(w, "2026-10-04", false) {
		t.Error("a future session should be available")
	}
	if Available(w, "2026-10-06", false) {
		t.Error("a past session must not be available")
	}
	if Available(w, "2026-10-05", true) {
		t.Error("a ridden session must not be available")
	}
	w.TestProtocol = "ramp"
	if Available(w, "2026-10-04", false) {
		t.Error("a test must not be available")
	}
}

func TestEverySwappedSessionKeepsAlternates(t *testing.T) {
	// A swap appends its marker but keeps the generated prefix, so the rider
	// can swap again.
	w := planned(t, model.SportCycling, "threshold", 5)
	w.Description += " " + scheduler.SwappedMarker + " harder, was Threshold 4."
	if !Plannable(w) {
		t.Error("a swapped session must still offer alternates")
	}
	w.Description += " " + scheduler.AdjustedMarker + " moved from 2026-10-03."
	if !Plannable(w) {
		t.Error("an adjusted session must still offer alternates")
	}
}

// Every ladder for both sports, every rung, a spread of rider levels: the
// invariants of each option hold whatever the rung durations look like.
func TestEveryLadderRungHonoursTheRules(t *testing.T) {
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
		l, ok := workoutlib.LadderFor(k.sport, k.zone)
		if !ok {
			t.Fatalf("no ladder for %v", k)
		}
		for cur := 1; cur <= 10; cur++ {
			for _, riderLevel := range []float64{1, 2.5, 4.3, 6, 9.9, 10} {
				for _, reduced := range []bool{false, true} {
					w := planned(t, k.sport, k.zone, cur)
					opts := Options(w, riderLevel, reduced, profile)
					curTotal := workoutlib.TotalSeconds(l.Rungs[cur-1])
					harderCap := math.Floor(riderLevel) + 1
					for _, o := range opts {
						if len(o.Steps) == 0 || o.Name == "" {
							t.Errorf("%v cur %d %s: empty option %+v", k, cur, o.Kind, o)
						}
						if got := workout.PlannedSeconds(o.Steps); got != o.Seconds {
							t.Errorf("%v cur %d %s: steps %v s, option %v s", k, cur, o.Kind, got, o.Seconds)
						}
						switch o.Kind {
						case Easier:
							if o.Level != float64(cur-1) {
								t.Errorf("%v cur %d: easier level %v", k, cur, o.Level)
							}
						case Harder:
							if reduced {
								t.Errorf("%v cur %d: harder in a reduced week", k, cur)
							}
							if o.Level != float64(cur+1) || o.Level > harderCap {
								t.Errorf("%v cur %d L %v: harder level %v (cap %v)", k, cur, riderLevel, o.Level, harderCap)
							}
						case Shorter:
							if o.Level > float64(cur) || o.Seconds > 0.75*curTotal {
								t.Errorf("%v cur %d: shorter %+v", k, cur, o)
							}
						case Longer:
							if reduced {
								t.Errorf("%v cur %d: structured longer in a reduced week", k, cur)
							}
							if o.Level < float64(cur) || o.Level > harderCap || o.Seconds < 1.25*curTotal {
								t.Errorf("%v cur %d L %v: longer %+v (cap %v)", k, cur, riderLevel, o, harderCap)
							}
						}
					}
				}
			}
		}
	}
}

// Ladder durations are not monotone in level (cycling tempo goes 95 min at
// rung 6 then 90, 80 at rungs 7 and 8), so "shorter" is a search over rungs
// and not "one rung down". This pins what it returns per rung for one such
// ladder so a change to the ladder table is a visible change here.
func TestShorterAndLongerPerRungOnANonMonotoneLadder(t *testing.T) {
	// cycling tempo totals: 50 65 60 80 70 95 90 80 125 110
	shorter := []int{0, 0, 0, 3, 1, 5, 2, 3, 7, 8}  // level of Shorter for cur 1..10, 0 = none
	longer := []int{2, 7, 4, 10, 7, 9, 9, 10, 0, 0} // level of Longer for cur 1..10, 0 = none, rider at 10
	for cur := 1; cur <= 10; cur++ {
		w := planned(t, model.SportCycling, "tempo", cur)
		opts := Options(w, 10, false, profile)
		s, sok := find(opts, Shorter)
		if (shorter[cur-1] == 0) == sok || (sok && int(s.Level) != shorter[cur-1]) {
			t.Errorf("cur %d: shorter = %v (%v), want %d", cur, s.Level, sok, shorter[cur-1])
		}
		l, lok := find(opts, Longer)
		if (longer[cur-1] == 0) == lok || (lok && int(l.Level) != longer[cur-1]) {
			t.Errorf("cur %d: longer = %v (%v), want %d", cur, l.Level, lok, longer[cur-1])
		}
	}
}
