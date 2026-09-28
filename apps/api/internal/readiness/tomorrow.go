package readiness

import (
	"fmt"
	"time"
)

// TomorrowInput is what ForecastTomorrow needs — gathered by the caller
// (internal/api), same division of labour assessReadiness already draws
// for today's Assess: this package stays plain values in, Assessment out.
// See docs/superpowers/specs/2026-09-28-readiness-tomorrow-design.md.
type TomorrowInput struct {
	// TodayVerdict is today's own Assess result.
	TodayVerdict Verdict
	// ProjectedTSB is CTL/ATL rolled forward one day through today's load.
	ProjectedTSB float64
	// HaveProjectedTSB is false with no snapshot or a stale one; the value
	// above is then meaningless and ignored.
	HaveProjectedTSB bool
	// ACWR is the 7-day / 28-day mean load with today's own load counted.
	ACWR float64
	// HaveACWR carries the same 21-day-coverage gate as today's readiness.
	HaveACWR bool
	// ConsecutiveHardDays is hard sessions today and the unbroken run
	// before it.
	ConsecutiveHardDays int
}

// ForecastTomorrow says whether tomorrow's hard session is at risk. Rest
// and caution mean what they mean in Assess — swap for easy, step down one
// rung — but decided a day ahead, and never applied without the rider
// asking. The −30 form and 1.5 load thresholds are deliberately today's own
// numbers rolled one day forward, not a second set to learn.
//
// A rest day today is only ever caution for tomorrow: tonight's sleep and
// HRV can still recover, and the rest day already addresses the cause.
func ForecastTomorrow(in TomorrowInput) Assessment {
	var restReasons, cautionReasons []string

	if in.HaveProjectedTSB && in.ProjectedTSB < -30 {
		restReasons = append(restReasons, fmt.Sprintf("tomorrow's form is projected at %s", formatSigned(in.ProjectedTSB)))
	}

	if in.TodayVerdict == Rest {
		cautionReasons = append(cautionReasons, "you needed to rest today, and tomorrow is a hard session too")
	}
	if in.HaveACWR && in.ACWR >= 1.5 {
		cautionReasons = append(cautionReasons, fmt.Sprintf("with today's session counted, your load this week is %.1f× your usual", in.ACWR))
	}
	if in.ConsecutiveHardDays >= 2 {
		cautionReasons = append(cautionReasons, "tomorrow would be your third hard day in a row")
	}

	switch {
	case len(restReasons) > 0:
		return Assessment{Verdict: Rest, Reasons: append(restReasons, cautionReasons...)}
	case len(cautionReasons) > 0:
		return Assessment{Verdict: Caution, Reasons: cautionReasons}
	default:
		return Assessment{Verdict: Ready}
	}
}

// ACWR is the acute:chronic load ratio Assess uses, for a caller that has
// to build the loads itself — the forecast counts today's own load, which
// Assess's caller may not have logged yet. referenceDate is reduced to its
// calendar date, so a caller's time-of-day or zone never moves the window.
func ACWR(loads []Load, referenceDate time.Time) (float64, bool) {
	return acwr(loads, dateOnly(referenceDate))
}
