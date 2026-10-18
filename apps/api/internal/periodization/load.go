package periodization

import (
	"math"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// LoadInput is what sizes a plan in TSS instead of hours: the rider's fitness
// today and how fast they may be asked to move it. Design:
// docs/superpowers/specs/2026-10-18-training-load-model-design.md.
type LoadInput struct {
	CTL, ATL          float64
	TypicalIF         float64
	HistoryWeeks      int
	BestRecentWeekTSS float64
	MaxRamp           float64
	TargetCTL         float64 // 0 for an undated goal
	TaperTSB          float64 // form-band midpoint for a dated goal
}

// weekGain is the share of the gap to the daily load that CTL closes in
// a week of constant load, read off workout.RollFitness so the time constant lives in one place.
var weekGain = func() float64 {
	alpha, _ := workout.RollFitness(0, 0, 1)
	return 1 - math.Pow(1-alpha, 7)
}()

const (
	recoveryTSSFraction = 0.65
	spikeGrowth         = 1.15
	spikeHistoryWeeks   = 6
	taperMinFraction    = 0.3
	taperMaxFraction    = 1.0
	defaultMaxRamp      = 6.0
	defaultTypicalIF    = 0.70
	// minDefaultRamp is the smallest default ramp (peak), which bounds how far
	// the scale factor can usefully go before every ramp sits at MaxRamp.
	minDefaultRamp = 1.0
)

// WeeklyTSS is the TSS of a week at constant daily load that moves a CTL of
// ctl by ramp points over those 7 days. The one copy of this formula.
func WeeklyTSS(ctl, ramp float64) float64 {
	return 7 * (ctl + ramp/weekGain)
}

// NextFitness rolls (ctl, atl) through 7 days at weeklyTSS/7 per day.
func NextFitness(ctl, atl, weeklyTSS float64) (float64, float64) {
	d := weeklyTSS / 7
	for range 7 {
		ctl, atl = workout.RollFitness(ctl, atl, d)
	}
	return ctl, atl
}

// BuildWithLoad is Build with the weeks sized in TSS when load is given and
// the rider has weekly hours to spend. It walks exactly the weeks Build makes
// (phases, recovery, anchoring, numbering are unchanged) and layers the load
// on top; with load == nil or no available hours the result is today's Build
// with LoadBasis "hours".
func BuildWithLoad(goal workout.Goal, profile workout.RiderProfile, today time.Time, load *LoadInput) (Plan, error) {
	var (
		plan Plan
		pos  []int
	)
	if goal.EventDate == "" {
		plan, pos = buildRollingPos(goal, profile, today)
	} else {
		var err error
		plan, pos, err = buildPlanPos(goal, profile, today)
		if err != nil {
			return Plan{}, err
		}
	}
	plan.LoadBasis = "hours"
	capHours := profile.HoursPerAvailableDay * float64(len(profile.AvailableDays))
	if load == nil || !(capHours > 0) || len(plan.Weeks) == 0 {
		return plan, nil
	}
	layerLoad(&plan, pos, goal.EventDate, capHours, sanitizeLoad(*load))
	return plan, nil
}

func finite(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return x
}

func sanitizeLoad(l LoadInput) LoadInput {
	l.CTL = math.Max(0, finite(l.CTL))
	l.ATL = math.Max(0, finite(l.ATL))
	l.BestRecentWeekTSS = math.Max(0, finite(l.BestRecentWeekTSS))
	l.TargetCTL = math.Max(0, finite(l.TargetCTL))
	l.TaperTSB = finite(l.TaperTSB)
	if !(l.TypicalIF > 0) || math.IsInf(l.TypicalIF, 0) {
		l.TypicalIF = defaultTypicalIF
	}
	if !(l.MaxRamp > 0) || math.IsInf(l.MaxRamp, 0) {
		l.MaxRamp = defaultMaxRamp
	}
	return l
}

// defaultRamp is a phase's ramp (CTL points per week) before scaling.
func defaultRamp(phase Phase, pos int) float64 {
	pos = max(0, min(pos, 2))
	switch phase {
	case PhaseBase:
		return float64(3 + pos)
	case PhaseBuild:
		return float64(5 + pos)
	case PhasePeak:
		return 1
	}
	return 0
}

// loadState is the forecast carried from one week to the next.
type loadState struct {
	ctl, atl float64
	// prevDesired is the last non-recovery week's TSS before the guards, -1
	// before there is one; a recovery week is a fraction of it.
	prevDesired float64
	lastTSS     float64
	// held is set once an undated plan has reached its ceiling.
	held bool
}

type loadRun struct {
	load     LoadInput
	capHours float64
	undated  bool
	// eventDate is the dated goal's event, "" when undated.
	eventDate string
	weeks     []Week
	pos       []int
}

func (r loadRun) capTSS() float64 {
	return r.capHours * r.load.TypicalIF * r.load.TypicalIF * 100
}

// step sizes week i from the state, fills it in out and advances the state.
func (r loadRun) step(i int, s float64, st *loadState, out *Week) {
	w := r.weeks[i]
	ifSq100 := r.load.TypicalIF * r.load.TypicalIF * 100
	ceiling := r.capTSS() / 7

	var desired, ramp float64
	if w.Recovery {
		if st.prevDesired < 0 {
			desired = recoveryTSSFraction * WeeklyTSS(st.ctl, 0)
		} else {
			desired = recoveryTSSFraction * st.prevDesired
		}
	} else {
		ramp = math.Min(defaultRamp(w.Phase, r.pos[i])*s, r.load.MaxRamp)
		if r.undated {
			desired = WeeklyTSS(st.ctl, ramp)
			// The hours hold a CTL of `ceiling` and no more, so a ramp that
			// asks for the whole cap or more is where the plan stops climbing.
			// Hold from there at what keeps CTL where it is, which for a rider
			// already above the ceiling is more than the hours allow: the cap
			// trims it below.
			if st.held || desired >= r.capTSS() {
				st.held, ramp = true, 0
				desired = 7 * math.Max(st.ctl, ceiling)
			}
		} else {
			desired = WeeklyTSS(st.ctl, ramp)
		}
		st.prevDesired = desired
	}

	tss := desired
	if r.load.HistoryWeeks < spikeHistoryWeeks && r.load.BestRecentWeekTSS > 0 {
		tss = math.Min(tss, r.load.BestRecentWeekTSS*math.Pow(spikeGrowth, float64(i+1)))
	}
	tss = math.Max(0, finite(tss))
	hours := tss / ifSq100
	capped := false
	if tss > r.capTSS()*(1+1e-9) {
		tss, hours, capped = r.capTSS(), r.capHours, true
	}

	out.TargetTSS, out.TargetHours, out.LoadCapped = tss, hours, capped
	out.PlannedCTL, out.Ramp = st.ctl, ramp
	st.ctl, st.atl = NextFitness(st.ctl, st.atl, tss)
	st.lastTSS = tss
}

// forecast sizes weeks [0, n) with scale factor s.
func (r loadRun) forecast(n int, s float64) (loadState, []Week) {
	st := loadState{ctl: r.load.CTL, atl: r.load.ATL, prevDesired: -1}
	out := make([]Week, n)
	for i := range n {
		r.step(i, s, &st, &out[i])
	}
	return st, out
}

func layerLoad(plan *Plan, pos []int, eventDate string, capHours float64, load LoadInput) {
	undated := eventDate == ""
	r := loadRun{load: load, capHours: capHours, undated: undated, eventDate: eventDate, weeks: plan.Weeks, pos: pos}

	firstTaper := len(plan.Weeks)
	for i, w := range plan.Weeks {
		if w.Phase == PhaseTaper {
			firstTaper = i
			break
		}
	}

	s := 1.0
	if !undated && load.TargetCTL > 0 && firstTaper > 0 {
		reached := func(s float64) float64 { st, _ := r.forecast(firstTaper, s); return st.ctl }
		sMax := math.Max(1, load.MaxRamp/minDefaultRamp)
		if reached(1) < load.TargetCTL {
			if top := reached(sMax); top < load.TargetCTL {
				s = sMax
				plan.Shortfall = load.TargetCTL - top
			} else {
				lo, hi := 1.0, sMax
				for hi-lo > 1e-3 {
					mid := (lo + hi) / 2
					if reached(mid) >= load.TargetCTL {
						hi = mid
					} else {
						lo = mid
					}
				}
				s = hi
			}
		}
	}

	st, weeks := r.forecast(firstTaper, s)
	if firstTaper < len(plan.Weeks) {
		weeks = append(weeks, r.taper(plan.Weeks[firstTaper:], st)...)
	}
	for i := range weeks {
		plan.Weeks[i].TargetTSS = weeks[i].TargetTSS
		plan.Weeks[i].TargetHours = weeks[i].TargetHours
		plan.Weeks[i].PlannedCTL = weeks[i].PlannedCTL
		plan.Weeks[i].Ramp = weeks[i].Ramp
		plan.Weeks[i].LoadCapped = weeks[i].LoadCapped
	}
	plan.LoadBasis = "ctl"
	plan.TargetCTL = load.TargetCTL
}

// taper sizes the taper weeks at one constant daily load, solved so the
// projected TSB on the event date lands on the form-band midpoint, within
// [0.3, 1.0] x the last pre-taper day's load.
func (r loadRun) taper(weeks []Week, st loadState) []Week {
	lastDaily := st.ctl
	if st.lastTSS > 0 {
		lastDaily = st.lastTSS / 7
	}
	lo, hi := taperMinFraction*lastDaily, taperMaxFraction*lastDaily
	hi = math.Min(hi, r.capTSS()/7)
	lo = math.Min(lo, hi)

	days := 0
	start, err1 := time.Parse("2006-01-02", weeks[0].StartDate)
	event, err2 := time.Parse("2006-01-02", r.eventDate)
	if err1 == nil && err2 == nil {
		days = max(0, int(event.Sub(start).Hours()/24))
	}
	tsb := func(l float64) float64 {
		c, a := st.ctl, st.atl
		for range days {
			c, a = workout.RollFitness(c, a, l)
		}
		return c - a
	}
	l := lo
	switch {
	case tsb(lo) <= r.load.TaperTSB:
		l = lo
	case tsb(hi) >= r.load.TaperTSB:
		l = hi
	default:
		for range 60 {
			mid := (lo + hi) / 2
			if tsb(mid) > r.load.TaperTSB {
				lo = mid
			} else {
				hi = mid
			}
		}
		l = (lo + hi) / 2
	}
	l = math.Max(0, finite(l))

	out := make([]Week, len(weeks))
	ctl, atl := st.ctl, st.atl
	ifSq100 := r.load.TypicalIF * r.load.TypicalIF * 100
	for i := range weeks {
		tss := 7 * l
		hours, capped := tss/ifSq100, false
		if tss > r.capTSS()*(1+1e-9) {
			tss, hours, capped = r.capTSS(), r.capHours, true
		}
		out[i] = Week{TargetTSS: tss, TargetHours: hours, LoadCapped: capped, PlannedCTL: ctl}
		ctl, atl = NextFitness(ctl, atl, tss)
	}
	return out
}
