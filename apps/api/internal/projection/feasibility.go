package projection

import (
	"fmt"
	"math"
)

// FeasibilityKey names the outcome of the "is this goal realistic?" check; the
// UI keys off it, never off the message.
type FeasibilityKey string

const (
	FeasibleComfortable FeasibilityKey = "comfortable"
	FeasibleAchievable  FeasibilityKey = "achievable"
	FeasibleStretch     FeasibilityKey = "stretch"
	FeasibleUnrealistic FeasibilityKey = "unrealistic"
)

// Feasibility is the one-line answer. Tone is a Nuxt UI colour name.
type Feasibility struct {
	Key     FeasibilityKey
	Message string
	Tone    string
}

// FeasibilityInput is what a periodization plan says about reaching the
// fitness an event needs.
type FeasibilityInput struct {
	// TargetCTL is the fitness the event needs.
	TargetCTL float64
	// Shortfall is the gap left even at the maximum ramp, 0 when reached.
	Shortfall float64
	// ReachedCTL is the plan's CTL at the start of the first taper week (the
	// last week when there is no taper).
	ReachedCTL float64
	// HoursCapped is true when the rider's available hours held back any
	// pre-taper week.
	HoursCapped bool
	// Weeks is the length of the plan.
	Weeks int
}

// JudgeFeasibility applies the rules, first match winning. Figures are rounded
// to whole points before comparing so the verdict never contradicts the
// numbers printed beside it.
func JudgeFeasibility(in FeasibilityInput) Feasibility {
	target := math.Round(in.TargetCTL)
	var reached float64
	if in.Shortfall > 0 {
		reached = math.Round(in.TargetCTL - in.Shortfall)
	} else {
		// The plan reaches the target, so never print a figure below it.
		reached = math.Max(math.Round(in.ReachedCTL), target)
	}

	if in.Shortfall <= 0 {
		if reached*100 >= target*105 {
			return Feasibility{FeasibleComfortable,
				fmt.Sprintf("Comfortable: your plan reaches fitness %.0f by race day; this event needs about %.0f.", reached, target), "success"}
		}
		return Feasibility{FeasibleAchievable,
			fmt.Sprintf("Achievable: your plan builds up to the fitness %.0f this event needs.", target), "success"}
	}

	switch {
	case reached*100 >= target*85:
		return Feasibility{FeasibleAchievable,
			fmt.Sprintf("Achievable, just: fitness %.0f of about %.0f by race day.", reached, target), "success"}
	case reached*100 >= target*70 || in.HoursCapped:
		if in.HoursCapped {
			return Feasibility{FeasibleStretch,
				fmt.Sprintf("A stretch: your available hours cap the build at fitness %.0f of about %.0f. More hours a week or a later date would close it.", reached, target), "warning"}
		}
		return Feasibility{FeasibleStretch,
			fmt.Sprintf("A stretch: fitness %.0f of about %.0f by race day. A later date would close it.", reached, target), "warning"}
	}
	return Feasibility{FeasibleUnrealistic,
		fmt.Sprintf("Not realistic in %d weeks: fitness can reach about %.0f, this event needs about %.0f.", in.Weeks, reached, target), "error"}
}
