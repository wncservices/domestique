package projection

import "time"

// SustainableRamp is the most CTL a week should add: Friel and Coggan/Allen
// put a workable ramp at 5 to 8 a week, 3 to 5 for the long run.
const SustainableRamp = 8.0

// rampExemptCTL: from a standing start a few TSS a day is mathematically a big
// weekly ramp, so a week that begins under this fitness is never flagged.
const rampExemptCTL = 20.0

// tssPerCTLPoint is the weekly load one CTL point per week takes at steady state.
const tssPerCTLPoint = 42.0

// RampWarning is one week whose build outran what is sustainable. WeekStart is
// the Monday the week began.
type RampWarning struct {
	WeekStart string
	PerWeek   float64
	ExcessTSS float64
}

// Ramp is the biggest weekly CTL gain measured and every week over the limit.
type Ramp struct {
	MaxPerWeek float64
	Warnings   []RampWarning
}

// Ramps reads each Monday-to-Monday week in the series (so it needs the days
// from RollWithHistory to reach the current week): the CTL gain across the
// week, warned when it is strictly above SustainableRamp.
func Ramps(points []Point) Ramp {
	ctlOn := make(map[string]float64, len(points))
	for _, p := range points {
		ctlOn[p.Date] = p.CTL
	}
	var r Ramp
	for _, p := range points {
		d, err := time.Parse(dateLayout, p.Date)
		if err != nil || d.Weekday() != time.Monday {
			continue
		}
		prev := d.AddDate(0, 0, -7).Format(dateLayout)
		before, ok := ctlOn[prev]
		if !ok || before < rampExemptCTL {
			continue
		}
		perWeek := p.CTL - before
		if perWeek > r.MaxPerWeek {
			r.MaxPerWeek = perWeek
		}
		if perWeek > SustainableRamp {
			r.Warnings = append(r.Warnings, RampWarning{WeekStart: prev, PerWeek: perWeek, ExcessTSS: (perWeek - SustainableRamp) * tssPerCTLPoint})
		}
	}
	return r
}
