// Package adapter closes the adaptive-replanning loop docs/training-plan.md
// calls Phase D: it compares what a rider actually trained, via their own
// completed_sessions, against what internal/periodization asked for, and
// nudges the plan's still-upcoming weeks up or down accordingly — the
// "Humango-shaped feedback loop" that document names as the point of
// periodizing at all, rather than handing out a static plan on day one and
// never looking back.
//
// This does not persist a superseded plan the way docs/training-plan.md's
// original training_plans sketch describes. Nothing in this app persists a
// periodization.Plan today — handleGoalPeriodization computes one fresh on
// every request, from the goal and profile alone, the same "recompute
// rather than cache" choice AGENTS.md's own Architecture section makes for
// the route library ("re-read on every request... caching would mostly buy
// stale answers"). Reconcile follows that precedent: it recomputes the
// adjustment from history on every call rather than writing an audit trail
// of superseded plans.
package adapter

import (
	"time"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// lookbackWeeks bounds how much history feeds the adjustment — recent
// training predicts what is sustainable better than training from months
// ago, and a rider who struggled early in a long plan should not still be
// held back after weeks of since-improved compliance.
const lookbackWeeks = 3

// minAdjustment and maxAdjustment bound how far Reconcile may move a
// week's target from what periodization.BuildPlan proposed — asymmetric on
// purpose: a rider falling behind should be eased off further than one
// overperforming should be pushed, the same caution a coach applies before
// asking for more rather than less.
const (
	minAdjustment = 0.70
	maxAdjustment = 1.15
)

// perWeekMin and perWeekMax clamp any single completed week's own
// compliance ratio before it is averaged in, so one outlier week — a
// single missed ride, or one big century — cannot swing the whole
// adjustment on its own.
const (
	perWeekMin = 0.4
	perWeekMax = 1.3
)

// Reconcile adjusts plan's upcoming Base/Build weeks' TargetHours to
// reflect how much the rider actually trained over the last few *completed*
// calendar weeks, relative to what those weeks asked for at the time.
//
// plan is the forward-looking plan periodization.BuildPlan(goal, profile,
// today) already produced — BuildPlan always anchors its first week to
// today, so plan.Weeks[0] is always "this week" and every later index is a
// week that has not happened yet. There is no past week inside plan to
// compare against: a freshly built plan cannot see its own history. To
// judge compliance, Reconcile instead builds one *separate* plan anchored
// lookbackWeeks Mondays before today — its own first lookbackWeeks weeks
// are the real calendar weeks just elapsed, in true sequential order, so
// recovery-week cadence and the base/build ramp position are computed
// correctly relative to each other (anchoring a fresh BuildPlan call
// independently at *each* past Monday instead, the first approach tried
// here, cannot do this: every fresh call starts its own phase position
// counter at 0, so a week's real position within its 3-week-load/1-week-
// recovery cycle is lost). This is still an approximation, not a stored
// ground truth: because nothing here persists a plan, the phase boundaries
// that reconstruction implies can drift slightly from what today's own
// plan shows for the same calendar week, since the two are anchored
// lookbackWeeks apart and BuildPlan's phase proportions are a function of
// how many weeks remain until the event. That drift is the accepted cost
// of never persisting a plan at all, the same trade this app already made
// for periodization itself.
//
// Two deliberate exclusions:
//
//   - Peak and Taper weeks are never adjusted, in either direction: both
//     are already deliberately reduced- or held-load phases (see
//     periodization's own doc comment), and Peak in particular is a volume
//     proxy for what should really be an intensity change — not a dial
//     this package should nudge based on volume compliance either.
//   - plan.Weeks[0] — the current week, already started and possibly
//     already scheduled into real workout rows by handleGoalSchedule — is
//     always left exactly as BuildPlan computed it. Reconcile only ever
//     changes what has not happened yet.
//
// A rider with no completed_sessions at all yet — a brand new profile, or
// one that has never connected/synced a provider — gets no adjustment
// either: zero recorded sessions means there is nothing to judge compliance
// from, not proof of zero training. Once at least one session exists, an
// elapsed week with genuinely nothing logged against it is treated as real
// (a week the rider actually skipped), which is the signal this feature
// exists to react to.
//
// sessions need not be pre-filtered or sorted — Reconcile only reads Date
// and DurationSeconds, bucketing them into calendar weeks itself, and a
// rider's sessions across every sport all count toward the same hours
// target BuildPlan itself is sport-agnostic about.
func Reconcile(goal workout.Goal, profile workout.RiderProfile, plan periodization.Plan, sessions []workout.CompletedSession, today time.Time) periodization.Plan {
	factor := complianceFactor(goal, profile, sessions, periodization.MondayOf(today))

	out := periodization.Plan{GoalID: plan.GoalID, Weeks: make([]periodization.Week, len(plan.Weeks)), Adjustment: factor}
	for i, wk := range plan.Weeks {
		if i > 0 && factor != 1 && (wk.Phase == periodization.PhaseBase || wk.Phase == periodization.PhaseBuild) {
			wk.TargetHours *= factor
			wk.Adjusted = true
		}
		out.Weeks[i] = wk
	}
	return out
}

// complianceFactor builds the one historical plan Reconcile's own doc
// comment describes, compares its first lookbackWeeks weeks against actual
// training, and averages the clamped ratios into one overall factor
// clamped to [minAdjustment, maxAdjustment]. Returns 1 (no adjustment) when
// there are no completed sessions at all yet, or no qualifying history
// once the plan is reconstructed.
func complianceFactor(goal workout.Goal, profile workout.RiderProfile, sessions []workout.CompletedSession, currentWeekStart time.Time) float64 {
	if len(sessions) == 0 {
		return 1
	}
	actualHoursByWeek := bucketActualHours(sessions)

	pastAnchor := currentWeekStart.AddDate(0, 0, -7*lookbackWeeks)
	histPlan, err := periodization.BuildPlan(goal, profile, pastAnchor)
	if err != nil || len(histPlan.Weeks) < lookbackWeeks {
		return 1
	}

	var ratios []float64
	for _, wk := range histPlan.Weeks[:lookbackWeeks] {
		if wk.Phase != periodization.PhaseBase && wk.Phase != periodization.PhaseBuild {
			continue
		}
		if wk.TargetHours <= 0 {
			continue
		}
		actual := actualHoursByWeek[wk.StartDate]
		ratios = append(ratios, clamp(actual/wk.TargetHours, perWeekMin, perWeekMax))
	}
	if len(ratios) == 0 {
		return 1
	}

	var sum float64
	for _, r := range ratios {
		sum += r
	}
	return clamp(sum/float64(len(ratios)), minAdjustment, maxAdjustment)
}

// bucketActualHours sums each session's duration into the calendar week
// (keyed by that week's Monday, "YYYY-MM-DD") it fell in.
func bucketActualHours(sessions []workout.CompletedSession) map[string]float64 {
	out := make(map[string]float64, len(sessions))
	for _, s := range sessions {
		date, err := time.Parse("2006-01-02", s.Date)
		if err != nil {
			continue
		}
		week := periodization.MondayOf(date).Format("2006-01-02")
		out[week] += s.DurationSeconds / 3600
	}
	return out
}

func clamp(v, low, high float64) float64 {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}
