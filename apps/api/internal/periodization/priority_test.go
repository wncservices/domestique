package periodization

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A 10-week plan from loadToday (a Monday) whose event is a Sunday, or a
// Wednesday, of the last week.
const (
	eventSunday    = "2026-03-15"
	eventWednesday = "2026-03-11"
)

func priorityGoal(event string, p workout.Priority) workout.Goal {
	return workout.Goal{ID: "g", EventDate: event, Priority: p}
}

func priorityLoad() *LoadInput {
	return &LoadInput{CTL: 50, ATL: 55, TypicalIF: 0.7, HistoryWeeks: 52, MaxRamp: 8, TargetCTL: 70, TaperTSB: 5}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

func phases(plan Plan) []Phase {
	out := make([]Phase, len(plan.Weeks))
	for i, w := range plan.Weeks {
		out[i] = w.Phase
	}
	return out
}

// Priority A is today's plan, to the digit: these numbers were read off the
// build before priority shaped anything.
func TestPriorityAIsUnchanged(t *testing.T) {
	for _, p := range []workout.Priority{"", workout.PriorityA} {
		plan := mustBuild(t, priorityGoal(eventSunday, p), richProfile(), priorityLoad())
		if len(plan.Weeks) != 10 {
			t.Fatalf("weeks = %d", len(plan.Weeks))
		}
		w9, w10 := plan.Weeks[8], plan.Weeks[9]
		if w9.Phase != PhasePeak || w10.Phase != PhaseTaper {
			t.Fatalf("phases %q %q", w9.Phase, w10.Phase)
		}
		if !near(w9.TargetTSS, 609.4680) || !near(w9.PlannedCTL, 80.5530) || !near(w9.TargetHours, 12.4381) {
			t.Errorf("week 9 = %+v", w9)
		}
		if !near(w10.TargetTSS, 453.7541) || !near(w10.PlannedCTL, 81.5530) || !near(w10.TargetHours, 9.2603) {
			t.Errorf("week 10 = %+v", w10)
		}
		if w9.MiniTaperDays != 0 || w10.MiniTaperDays != 0 {
			t.Error("A has no mini-taper")
		}
		wed := mustBuild(t, priorityGoal(eventWednesday, p), richProfile(), priorityLoad())
		if w := wed.Weeks[9]; w.Phase != PhaseTaper || !near(w.TargetTSS, 250.1438) || !near(w.TargetHours, 5.1050) {
			t.Errorf("wednesday event week 10 = %+v", w)
		}
		hours, err := BuildPlan(priorityGoal(eventSunday, p), richProfile(), loadToday)
		if err != nil {
			t.Fatal(err)
		}
		if w := hours.Weeks[9]; w.Phase != PhaseTaper || !near(w.TargetHours, 7) {
			t.Errorf("hours model week 10 = %+v", w)
		}
	}
}

// Priority C has no taper at all: the event week is an ordinary week of the
// phase before it, and the event day is the session.
func TestPriorityCHasNoTaper(t *testing.T) {
	plan := mustBuild(t, priorityGoal(eventSunday, workout.PriorityC), richProfile(), priorityLoad())
	for i, w := range plan.Weeks {
		if w.Phase == PhaseTaper || w.MiniTaperDays != 0 {
			t.Errorf("week %d = %+v", i+1, w)
		}
	}
	w9, w10 := plan.Weeks[8], plan.Weeks[9]
	if w9.Phase != PhasePeak || w10.Phase != PhasePeak || w10.Recovery {
		t.Fatalf("last weeks %q %q", w9.Phase, w10.Phase)
	}
	// The ordinary peak week: held volume and the peak ramp of 1 CTL point.
	if !near(w10.Ramp, 1) || w10.TargetHours < w9.TargetHours {
		t.Errorf("week 9 %+v week 10 %+v", w9, w10)
	}
	if w10.TargetTSS < plan.Weeks[8].TargetTSS*0.95 {
		t.Errorf("event week %v is far below the week before %v", w10.TargetTSS, w9.TargetTSS)
	}
	hours, err := BuildPlan(priorityGoal(eventSunday, workout.PriorityC), richProfile(), loadToday)
	if err != nil {
		t.Fatal(err)
	}
	if w := hours.Weeks[9]; w.Phase != PhasePeak || !near(w.TargetHours, 22.4) {
		t.Errorf("hours model week 10 = %+v", w)
	}
}

// Priority B runs the event week as an ordinary week and drops the last days
// before the event to the taper's daily load.
func TestPriorityBHasAMiniTaper(t *testing.T) {
	c := mustBuild(t, priorityGoal(eventSunday, workout.PriorityC), richProfile(), priorityLoad())
	b := mustBuild(t, priorityGoal(eventSunday, workout.PriorityB), richProfile(), priorityLoad())
	for i, w := range b.Weeks {
		if w.Phase == PhaseTaper {
			t.Errorf("week %d is a taper week", i+1)
		}
		if i < 9 && (w.MiniTaperDays != 0 || !near(w.TargetTSS, c.Weeks[i].TargetTSS)) {
			t.Errorf("week %d differs from the no-taper plan: %+v", i+1, w)
		}
	}
	w9, w10 := b.Weeks[8], b.Weeks[9]
	if w9.Phase != PhasePeak || w10.Phase != PhasePeak || w10.MiniTaperDays != 3 {
		t.Fatalf("week 9 %+v week 10 %+v", w9, w10)
	}
	daily := c.Weeks[9].TargetTSS / 7
	if !near(w10.PlannedCTL, c.Weeks[9].PlannedCTL) {
		t.Errorf("event week starts at CTL %v, want %v", w10.PlannedCTL, c.Weeks[9].PlannedCTL)
	}
	l := (w10.TargetTSS - 4*daily) / 3
	if l < 0.3*daily-1e-6 || l > daily+1e-6 || w10.TargetTSS >= c.Weeks[9].TargetTSS {
		t.Errorf("mini-taper daily load %v outside [0.3, 1] x %v", l, daily)
	}
	// Rolled day by day, the form on the event date lands in the band.
	ctl, atl := w10.PlannedCTL, 0.0
	_, atl = rollTo(b, 9, priorityLoad())
	for d := 0; d < 6; d++ {
		load := daily
		if d >= 3 {
			load = l
		}
		ctl, atl = workout.RollFitness(ctl, atl, load)
	}
	if got := ctl - atl; math.Abs(got-5) > 0.5 {
		t.Errorf("event-day form = %v, want 5", got)
	}
	if !near(w10.TargetHours, w10.TargetTSS/(0.7*0.7*100)) {
		t.Errorf("hours %v do not follow TSS %v", w10.TargetHours, w10.TargetTSS)
	}
}

// rollTo is the ATL at the start of week index i, from the weekly TSS before it.
func rollTo(plan Plan, i int, l *LoadInput) (float64, float64) {
	ctl, atl := l.CTL, l.ATL
	for _, w := range plan.Weeks[:i] {
		ctl, atl = NextFitness(ctl, atl, w.TargetTSS)
	}
	return ctl, atl
}

func TestPriorityBMiniTaperIsClampedToTheDaysBeforeTheEvent(t *testing.T) {
	b := mustBuild(t, priorityGoal(eventWednesday, workout.PriorityB), richProfile(), priorityLoad())
	if got := b.Weeks[9].MiniTaperDays; got != 2 {
		t.Errorf("Wednesday event: MiniTaperDays = %d, want 2 (Monday and Tuesday)", got)
	}
	monday := mustBuild(t, priorityGoal("2026-03-09", workout.PriorityB), richProfile(), priorityLoad())
	if w := monday.Weeks[len(monday.Weeks)-1]; w.MiniTaperDays != 0 || w.Phase == PhaseTaper {
		t.Errorf("Monday event has no days before it: %+v", w)
	}
}

func TestPriorityBHoursModelDropsTheLastDays(t *testing.T) {
	b, err := BuildPlan(priorityGoal(eventSunday, workout.PriorityB), richProfile(), loadToday)
	if err != nil {
		t.Fatal(err)
	}
	w := b.Weeks[9]
	// 3 days at the taper's 0.5 of peak hours instead of 0.8: (4 x 0.8 + 3 x 0.5) / 7.
	if w.Phase != PhasePeak || w.MiniTaperDays != 3 || !near(w.TargetHours, 28*(4*0.8+3*0.5)/7) {
		t.Errorf("week 10 = %+v", w)
	}
}

func TestShortPlansWithoutATaperStillHaveAWeek(t *testing.T) {
	for _, p := range []workout.Priority{workout.PriorityB, workout.PriorityC} {
		one := mustBuild(t, workout.Goal{ID: "g", EventDate: loadToday.AddDate(0, 0, 3).Format("2006-01-02"), Priority: p}, richProfile(), baseLoad())
		if len(one.Weeks) != 1 || one.Weeks[0].Phase != PhaseBuild {
			t.Errorf("%s: one-week plan = %+v", p, one.Weeks)
		}
		for _, w := range one.Weeks {
			for _, v := range []float64{w.TargetTSS, w.TargetHours, w.PlannedCTL} {
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
					t.Fatalf("%s: %+v", p, w)
				}
			}
		}
		five := mustBuild(t, workout.Goal{ID: "g", EventDate: loadToday.AddDate(0, 0, 5*7-1).Format("2006-01-02"), Priority: p}, richProfile(), baseLoad())
		for _, ph := range phases(five) {
			if ph == PhaseTaper {
				t.Errorf("%s: five-week plan %v has a taper", p, phases(five))
			}
		}
	}
}

func TestMiniTaperWeekCarriesItsDaysAndIsNotRamped(t *testing.T) {
	// Capped hours: the week before the drop would hit the hours ceiling.
	p := workout.RiderProfile{HoursPerAvailableDay: 1, AvailableDays: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}}
	l := priorityLoad()
	l.CTL, l.ATL, l.TargetCTL = 60, 60, 0
	plan := mustBuild(t, priorityGoal(eventSunday, workout.PriorityB), p, l)
	w := plan.Weeks[9]
	if w.MiniTaperDays != 3 || w.EventDate != eventSunday || w.MiniTaperStart != "2026-03-12" {
		t.Fatalf("week = %+v", w)
	}
	if w.Ramp != 0 {
		t.Errorf("ramp = %v, want 0", w.Ramp)
	}
	if w.LoadCapped || w.TargetTSS > 7*1*0.7*0.7*100 {
		t.Errorf("capped = %v, TSS %v: the reduced week is under the cap", w.LoadCapped, w.TargetTSS)
	}
	if w.MiniTaperDayHours <= 0 || w.MiniTaperDayHours > 1 {
		t.Errorf("mini-taper day hours = %v", w.MiniTaperDayHours)
	}
	hours, err := BuildPlan(priorityGoal(eventSunday, workout.PriorityB), richProfile(), loadToday)
	if err != nil {
		t.Fatal(err)
	}
	if h := hours.Weeks[9]; !near(h.MiniTaperDayHours, 28*0.5/7) {
		t.Errorf("hours model mini-taper day hours = %v, want %v", h.MiniTaperDayHours, 28*0.5/7)
	}
}
