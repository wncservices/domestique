package periodization

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var loadToday = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) // a Monday

func richProfile() workout.RiderProfile {
	return workout.RiderProfile{HoursPerAvailableDay: 4, AvailableDays: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}}
}

// datedGoal ends on the Sunday of its nth week, so the plan has exactly n weeks.
func datedGoal(weeks int) workout.Goal {
	return workout.Goal{ID: "g", EventDate: loadToday.AddDate(0, 0, 7*weeks-1).Format("2006-01-02")}
}

func baseLoad() *LoadInput {
	return &LoadInput{CTL: 50, ATL: 50, TypicalIF: 0.7, HistoryWeeks: 52, MaxRamp: 8, TaperTSB: 5}
}

func mustBuild(t *testing.T, g workout.Goal, p workout.RiderProfile, l *LoadInput) Plan {
	t.Helper()
	plan, err := BuildWithLoad(g, p, loadToday, l)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestWeeklyTSSMovesCTLByTheRamp(t *testing.T) {
	for _, ctl := range []float64{20, 60, 100} {
		for _, ramp := range []float64{-3, 0, 5} {
			got, _ := NextFitness(ctl, ctl, WeeklyTSS(ctl, ramp))
			if math.Abs(got-ctl-ramp) > 1e-9 {
				t.Errorf("ctl %v ramp %v: gained %v", ctl, ramp, got-ctl)
			}
		}
	}
}

func TestNilLoadLeavesNewFieldsZero(t *testing.T) {
	for _, g := range []workout.Goal{datedGoal(10), {ID: "undated"}} {
		plan := mustBuild(t, g, richProfile(), nil)
		if plan.LoadBasis != "hours" || plan.TargetCTL != 0 || plan.Shortfall != 0 {
			t.Errorf("plan fields = %q %v %v", plan.LoadBasis, plan.TargetCTL, plan.Shortfall)
		}
		for _, w := range plan.Weeks {
			if w.TargetTSS != 0 || w.PlannedCTL != 0 || w.Ramp != 0 || w.LoadCapped {
				t.Errorf("week %d carries load fields: %+v", w.Number, w)
			}
		}
		want, err := Build(g, richProfile(), loadToday)
		if err != nil || !reflect.DeepEqual(want, plan) {
			t.Errorf("Build and BuildWithLoad(nil) differ")
		}
	}
}

func TestRampsPerPhase(t *testing.T) {
	plan := mustBuild(t, datedGoal(18), richProfile(), baseLoad())
	wantRamp := map[Phase][]float64{PhaseBase: {3, 4, 5}, PhaseBuild: {5, 6, 7}}
	pos := map[Phase]int{}
	prevTSS := 0.0
	for _, w := range plan.Weeks {
		switch {
		case w.Recovery:
			if math.Abs(w.TargetTSS-0.65*prevTSS) > 1e-9 {
				t.Errorf("week %d recovery TSS %v, want %v", w.Number, w.TargetTSS, 0.65*prevTSS)
			}
			pos[w.Phase] = 0
		case w.Phase == PhaseBase || w.Phase == PhaseBuild:
			if w.Ramp != wantRamp[w.Phase][pos[w.Phase]] {
				t.Errorf("week %d (%s) ramp %v", w.Number, w.Phase, w.Ramp)
			}
			pos[w.Phase]++
			prevTSS = w.TargetTSS
		case w.Phase == PhasePeak:
			if w.Ramp != 1 {
				t.Errorf("peak week %d ramp %v", w.Number, w.Ramp)
			}
		}
	}
}

func TestRampsAreCappedAtMaxRamp(t *testing.T) {
	l := baseLoad()
	l.MaxRamp = 4
	for _, w := range mustBuild(t, datedGoal(18), richProfile(), l).Weeks {
		if w.Ramp > 4 {
			t.Errorf("week %d ramp %v", w.Number, w.Ramp)
		}
	}
}

func TestReachableTargetNeverRampsBelowDefaults(t *testing.T) {
	l := baseLoad()
	l.TargetCTL = 55
	plan := mustBuild(t, datedGoal(18), richProfile(), l)
	def := mustBuild(t, datedGoal(18), richProfile(), baseLoad())
	for i, w := range plan.Weeks {
		if w.Ramp != def.Weeks[i].Ramp {
			t.Errorf("week %d ramp %v, default %v", w.Number, w.Ramp, def.Weeks[i].Ramp)
		}
	}
	if plan.Shortfall != 0 {
		t.Errorf("shortfall %v", plan.Shortfall)
	}
}

func TestUnreachableTargetSetsShortfall(t *testing.T) {
	l := &LoadInput{CTL: 30, ATL: 30, TypicalIF: 0.7, HistoryWeeks: 52, MaxRamp: 6, TargetCTL: 110, TaperTSB: 5}
	plan := mustBuild(t, datedGoal(8), richProfile(), l)
	if plan.Shortfall <= 0 {
		t.Fatalf("shortfall %v", plan.Shortfall)
	}
	for _, w := range plan.Weeks {
		if !w.Recovery && w.Phase != PhaseTaper && w.Ramp != 6 {
			t.Errorf("week %d (%s) ramp %v, want 6", w.Number, w.Phase, w.Ramp)
		}
	}
}

// endOfPeakCTL rolls the plan's own weeks forward to the end of the last non-taper week.
func endOfPeakCTL(plan Plan, l *LoadInput) float64 {
	ctl, atl := l.CTL, l.ATL
	for _, w := range plan.Weeks {
		if w.Phase == PhaseTaper {
			break
		}
		ctl, atl = NextFitness(ctl, atl, w.TargetTSS)
	}
	return ctl
}

func TestScaledRampsReachTheTarget(t *testing.T) {
	l := &LoadInput{CTL: 40, ATL: 40, TypicalIF: 0.7, HistoryWeeks: 52, MaxRamp: 8, TargetCTL: 100, TaperTSB: 5}
	plan := mustBuild(t, datedGoal(16), richProfile(), l)
	if got := endOfPeakCTL(plan, l); math.Abs(got-100) > 0.5 {
		t.Errorf("CTL at the end of the peak = %v, want 100", got)
	}
	if plan.Shortfall != 0 {
		t.Errorf("shortfall %v", plan.Shortfall)
	}
}

// eventTSB rolls the plan day by day to the event date and returns TSB there.
func eventTSB(plan Plan, l *LoadInput, event string) float64 {
	ctl, atl := l.CTL, l.ATL
	ev, _ := time.Parse("2006-01-02", event)
	for _, w := range plan.Weeks {
		start, _ := time.Parse("2006-01-02", w.StartDate)
		for d := 0; d < 7; d++ {
			if !start.AddDate(0, 0, d).Before(ev) {
				return ctl - atl
			}
			ctl, atl = workout.RollFitness(ctl, atl, w.TargetTSS/7)
		}
	}
	return ctl - atl
}

func TestTaperLandsInTheBand(t *testing.T) {
	l := &LoadInput{CTL: 60, ATL: 70, TypicalIF: 0.7, HistoryWeeks: 52, MaxRamp: 8, TargetCTL: 70, TaperTSB: 8}
	g := datedGoal(18)
	plan := mustBuild(t, g, richProfile(), l)
	if got := eventTSB(plan, l, g.EventDate); math.Abs(got-8) > 1 {
		t.Errorf("event TSB = %v, want 8", got)
	}
}

func TestTaperOnlyPlanIsFinite(t *testing.T) {
	g := workout.Goal{ID: "g", EventDate: loadToday.AddDate(0, 0, 3).Format("2006-01-02")}
	l := baseLoad()
	plan := mustBuild(t, g, richProfile(), l)
	if len(plan.Weeks) != 1 || plan.Weeks[0].Phase != PhaseTaper {
		t.Fatalf("weeks = %+v", plan.Weeks)
	}
	w := plan.Weeks[0]
	for _, v := range []float64{w.TargetTSS, w.TargetHours, w.PlannedCTL, w.Ramp, plan.Shortfall} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("non-finite: %+v", w)
		}
	}
	daily := w.TargetTSS / 7
	if daily < 0.3*l.CTL-1e-9 || daily > l.CTL+1e-9 {
		t.Errorf("daily load %v outside the taper clamps of CTL %v", daily, l.CTL)
	}
}

func TestUndatedHoldsAtTheHoursCeiling(t *testing.T) {
	// 5 h x IF^2 0.49 x 100 / 7 = 35 CTL is all those hours can hold. The
	// hours cap limits the load that could land CTL exactly on it (and the
	// recovery weeks pull it back), so a climb closes in on it from below
	// rather than touching it; a rider within half a point of it holds.
	p := workout.RiderProfile{HoursPerAvailableDay: 1, AvailableDays: []string{"mon", "tue", "wed", "thu", "fri"}}
	ceiling := 5 * 0.49 * 100 / 7

	l := &LoadInput{CTL: 20, ATL: 20, TypicalIF: 0.7, HistoryWeeks: 52, MaxRamp: 8}
	plan := mustBuild(t, workout.Goal{ID: "u"}, p, l)
	if plan.Weeks[0].Ramp <= 0 {
		t.Errorf("first week does not climb: %+v", plan.Weeks[0])
	}
	last := plan.Weeks[len(plan.Weeks)-1]
	if last.PlannedCTL < 27 {
		t.Errorf("CTL only reached %v on the way to %v", last.PlannedCTL, ceiling)
	}
	for _, w := range plan.Weeks {
		if w.PlannedCTL > ceiling+0.5 {
			t.Errorf("week %d planned CTL %v over the ceiling %v", w.Number, w.PlannedCTL, ceiling)
		}
		if gap := ceiling - w.PlannedCTL; !w.Recovery && gap > 0 && math.Abs(w.Ramp-math.Min(math.Min(w.Ramp, 8), gap)) > 1e-9 {
			t.Errorf("week %d ramp %v is more than the %v left to the ceiling", w.Number, w.Ramp, gap)
		}
	}

	l = &LoadInput{CTL: ceiling - 0.3, ATL: ceiling - 0.3, TypicalIF: 0.7, HistoryWeeks: 52, MaxRamp: 8}
	for _, w := range mustBuild(t, workout.Goal{ID: "u"}, p, l).Weeks {
		if !w.Recovery && w.PlannedCTL >= ceiling-0.5 && w.Ramp != 0 {
			t.Errorf("week %d at %v still ramps %v", w.Number, w.PlannedCTL, w.Ramp)
		}
	}
}

func TestSpikeGuard(t *testing.T) {
	l := baseLoad()
	l.HistoryWeeks, l.BestRecentWeekTSS = 3, 200
	for i, w := range mustBuild(t, datedGoal(18), richProfile(), l).Weeks {
		if limit := 200 * math.Pow(1.15, float64(i+1)); w.TargetTSS > limit+1e-9 {
			t.Errorf("week %d TSS %v over %v", i, w.TargetTSS, limit)
		}
	}
}

func TestHoursCapSetsLoadCapped(t *testing.T) {
	p := workout.RiderProfile{HoursPerAvailableDay: 2, AvailableDays: []string{"sat", "sun"}}
	l := &LoadInput{CTL: 80, ATL: 80, TypicalIF: 0.7, HistoryWeeks: 52, MaxRamp: 6}
	prev := -1.0
	for _, w := range mustBuild(t, workout.Goal{ID: "u"}, p, l).Weeks {
		if w.TargetHours > 4 {
			t.Errorf("week %d over the cap: %v", w.Number, w.TargetHours)
		}
		if w.Recovery {
			if prev >= 0 && math.Abs(w.TargetTSS-0.65*prev) > 1e-9 {
				t.Errorf("week %d recovery %v, want 0.65 x %v", w.Number, w.TargetTSS, prev)
			}
			continue
		}
		prev = w.TargetTSS
		if w.TargetHours != 4 || !w.LoadCapped {
			t.Errorf("week %d: hours %v capped %v", w.Number, w.TargetHours, w.LoadCapped)
		}
	}
}

func TestDegenerateInputsStayFinite(t *testing.T) {
	l := &LoadInput{CTL: 50, ATL: 50, TypicalIF: 1e-200, HistoryWeeks: 52, MaxRamp: 8}
	p := workout.RiderProfile{HoursPerAvailableDay: math.Inf(1), AvailableDays: []string{"mon"}}
	plan := mustBuild(t, workout.Goal{ID: "u"}, p, l)
	if plan.LoadBasis != "hours" {
		t.Errorf("basis %q", plan.LoadBasis)
	}
	for _, w := range plan.Weeks {
		for _, v := range []float64{w.TargetTSS, w.PlannedCTL, w.Ramp} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("non-finite week %+v", w)
			}
		}
	}
	// A tiny IF is clamped, so a finite cap stays finite too.
	for _, w := range mustBuild(t, workout.Goal{ID: "u"}, richProfile(), l).Weeks {
		if math.IsNaN(w.TargetHours) || math.IsInf(w.TargetHours, 0) {
			t.Fatalf("non-finite hours %+v", w)
		}
	}
}

func TestTargetHoursFromTSS(t *testing.T) {
	for _, w := range mustBuild(t, datedGoal(12), richProfile(), baseLoad()).Weeks {
		if w.LoadCapped {
			t.Fatalf("week %d capped", w.Number)
		}
		if want := w.TargetTSS / (0.7 * 0.7 * 100); math.Abs(w.TargetHours-want) > 1e-9 {
			t.Errorf("week %d hours %v, want %v", w.Number, w.TargetHours, want)
		}
	}
}

func TestLoadBasis(t *testing.T) {
	if got := mustBuild(t, datedGoal(8), richProfile(), baseLoad()).LoadBasis; got != "ctl" {
		t.Errorf("with input: %q", got)
	}
	if got := mustBuild(t, datedGoal(8), richProfile(), nil).LoadBasis; got != "hours" {
		t.Errorf("without input: %q", got)
	}
	if got := mustBuild(t, datedGoal(8), workout.RiderProfile{}, baseLoad()).LoadBasis; got != "hours" {
		t.Errorf("no hours: %q", got)
	}
}
