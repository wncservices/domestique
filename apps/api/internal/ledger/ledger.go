// Package ledger is the pure arithmetic of the load ledger: what a week owed,
// what rolls into the next one, and which planned sessions may be made bigger
// to pay it back. No I/O and no clock; every date is an input.
package ledger

import (
	"time"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const (
	// MinDebt is the TSS at or under which nothing is repaid.
	MinDebt = 5.0
	// UpgradeDebt is the TSS above which an endurance session may become a
	// structured one.
	UpgradeDebt = 20.0
	// RollFraction caps what one week's own debt may carry into the next, as a
	// share of that next week's target.
	RollFraction = 0.10
	// LengthenFraction and LengthenMaxSeconds bound how much one session grows.
	LengthenFraction   = 0.30
	LengthenMaxSeconds = 2700
)

// Debt is the TSS a week still owes: its target plus what was carried in,
// less what was ridden and what is still planned. Never negative.
func Debt(target, carriedIn, completed, plannedRemaining float64) float64 {
	d := target + carriedIn - completed - plannedRemaining
	if d < 0 {
		return 0
	}
	return d
}

var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// OpenDays is how many of the rider's available days in the week beginning
// weekStart (the Monday, YYYY-MM-DD) are not blacked out.
func OpenDays(availableDays []string, weekStart string, blackout map[string]bool) int {
	monday, err := time.Parse("2006-01-02", weekStart)
	if err != nil {
		return 0
	}
	usable := 0
	for _, d := range availableDays {
		for i, name := range weekdays {
			if name == d {
				if !blackout[monday.AddDate(0, 0, i).Format("2006-01-02")] {
					usable++
				}
				break
			}
		}
	}
	return usable
}

// Target is a week's plan TSS scaled by how many of the rider's available days
// are not blacked out. weekStart is the Monday, YYYY-MM-DD.
func Target(planTSS float64, availableDays []string, weekStart string, blackout map[string]bool) float64 {
	if len(availableDays) == 0 {
		return 0
	}
	return planTSS * float64(OpenDays(availableDays, weekStart, blackout)) / float64(len(availableDays))
}

// EffectiveTarget is the target a week owes now: the one fixed when it opened,
// scaled down by the available days lost to life events since. openDays is
// what the week had when it opened (0 for a row that did not record it), and
// the target never grows back past what was fixed.
func EffectiveTarget(target float64, openDays, daysNow int) float64 {
	if openDays <= 0 || daysNow >= openDays {
		return target
	}
	if daysNow < 0 {
		daysNow = 0
	}
	return target * float64(daysNow) / float64(openDays)
}

// Settle closes last week. Only the debt that week incurred itself (not what it
// was carrying in) may roll, up to RollFraction of this week's target, and not
// at all into a reduced week. Everything else is forgiven. A reduced last week
// (recovery or taper) forgives everything: its shortfall was the plan's easing,
// not a debt to carry into a full week.
func Settle(lastDebt, lastCarriedIn, thisTarget float64, thisReduced, lastReduced bool) (rolled, lastForgiven float64) {
	if lastDebt <= 0 {
		return 0, 0
	}
	if lastReduced {
		return 0, lastDebt
	}
	own := lastDebt - lastCarriedIn
	if own < 0 {
		own = 0
	}
	if !thisReduced {
		rolled = own
		if limit := RollFraction * thisTarget; rolled > limit {
			rolled = limit
		}
		if rolled < 0 {
			rolled = 0
		}
	}
	return rolled, lastDebt - rolled
}

// Repayable reports whether a session may be made bigger. Repayment never
// locks a session, so a load_repaid rule alone does not disqualify it, but one
// already carrying it is not lengthened a second time.
func Repayable(w workout.Workout, blackout map[string]bool) bool {
	if !scheduler.IsGenerated(w) || w.RouteSlug != "" || w.TestProtocol != "" || w.CrewRideID != "" {
		return false
	}
	if blackout[w.Date] {
		return false
	}
	for _, r := range w.Flags.AdaptedRules {
		if r == why.LoadRepaid || r == why.GreenPush {
			return false
		}
	}
	return true
}
