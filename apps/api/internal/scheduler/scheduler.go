// Package scheduler turns one week of a periodization.Plan into concrete,
// dated workout sessions — the piece docs/training-plan.md's own
// architecture diagram calls scheduler.NextWorkouts, and the one
// internal/periodization deliberately stopped short of building (see that
// package's own doc comment: "this package answers 'how many hours,' not
// 'what should Tuesday's session actually contain'").
//
// Deliberately a small, explicit session-type table per sport and per
// phase, not a model — docs/training-plan.md's own recommendation is that
// turning a weekly volume target into "Tuesday: tempo ride" is a solved,
// specifiable rule set (the same vocabulary TrainingPeaks/Humango/
// TrainerRoad converge on), not something worth a network call or
// nondeterminism for. Pure function, same shape and testing precedent as
// periodization.BuildPlan and internal/sync.BuildPlan: fixed inputs in,
// the same output every time.
//
// Only the current week is turned into concrete sessions, not the whole
// plan at once (see NextWorkouts) — the far future stays periodization
// structure only, until it is its own turn. That is what makes Phase D's
// still-to-build adapter.Reconcile straightforward: it never has to undo
// concrete workouts generated under assumptions reality has since
// outgrown, because those workouts do not exist yet.
package scheduler

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// sessionType is one kind of session this package knows how to build —
// shared vocabulary across both sports: docs/training-plan.md names
// exactly these four ("a running goal wants a long run, a tempo run, one
// interval session and easy days; a cycling goal wants an endurance ride,
// a threshold session, and ... a VO2max session").
type sessionType int

const (
	sessionEasy sessionType = iota
	sessionLong
	sessionTempo
	sessionInterval
)

// weekdayOrder is Monday-first, matching periodization.Week.StartDate
// (always a Monday) and how a rider reads a training calendar.
var weekdayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// minIntervalReps floors an interval session at three efforts even when the
// week's target hours would otherwise round it down to one or two — a
// single "interval" is not a session a rider training for VO2max would
// recognise as one.
const minIntervalReps = 3

// intervalOnSeconds and intervalOffSeconds are one interval rep's on/off
// split — a fixed 3-minutes-on, 2-minutes-off shape rather than a per-sport
// or per-phase variable, since this package's own doc comment already
// draws the line at "a small explicit table," not a second dial to tune.
const (
	intervalOnSeconds  = 180.0
	intervalOffSeconds = 120.0
)

// NextWorkouts builds the concrete sessions for the plan week containing
// today. Returns no workouts, not an error, when the rider has no
// available-days template yet (nothing to schedule against) or today falls
// outside every week in the plan (the goal has already passed, or the plan
// was built for a different today).
func NextWorkouts(plan periodization.Plan, profile workout.RiderProfile, rider, goalID string, sport model.Sport, today time.Time) ([]workout.CreateWorkoutRequest, error) {
	week, ok := currentWeek(plan, today)
	if !ok {
		return nil, nil
	}
	return WeekWorkouts(week, profile, rider, goalID, sport)
}

// WeekWorkouts builds one week's sessions directly — split out from
// NextWorkouts so a test (or a future "regenerate week N") can target an
// exact week without depending on time.Now.
func WeekWorkouts(week periodization.Week, profile workout.RiderProfile, rider, goalID string, sport model.Sport) ([]workout.CreateWorkoutRequest, error) {
	days := sortedDays(profile.AvailableDays)
	if len(days) == 0 || week.TargetHours <= 0 {
		return nil, nil
	}

	weekStart, err := time.Parse("2006-01-02", week.StartDate)
	if err != nil {
		return nil, fmt.Errorf("scheduler: parse week start %q: %w", week.StartDate, err)
	}

	types := assignSessionTypes(week.Phase, week.Recovery, len(days))
	shares := sessionShares(types)

	out := make([]workout.CreateWorkoutRequest, 0, len(days))
	for i, day := range days {
		hours := week.TargetHours * shares[i]
		if hours <= 0 {
			continue
		}
		date := weekStart.AddDate(0, 0, weekdayOffset(day)).Format("2006-01-02")
		req := buildSession(types[i], hours, sport, profile)
		req.Rider = rider
		req.GoalID = goalID
		req.Date = date
		out = append(out, req)
	}
	return out, nil
}

// currentWeek finds the plan week whose Monday-to-Sunday span contains
// today.
func currentWeek(plan periodization.Plan, today time.Time) (periodization.Week, bool) {
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	for _, w := range plan.Weeks {
		start, err := time.Parse("2006-01-02", w.StartDate)
		if err != nil {
			continue
		}
		if !day.Before(start) && day.Before(start.AddDate(0, 0, 7)) {
			return w, true
		}
	}
	return periodization.Week{}, false
}

// sortedDays orders a rider's available days Monday-first regardless of
// input order, dropping anything not a recognised three-letter day and any
// duplicate.
func sortedDays(days []string) []string {
	index := make(map[string]int, len(weekdayOrder))
	for i, d := range weekdayOrder {
		index[d] = i
	}
	seen := make(map[string]bool, len(days))
	out := make([]string, 0, len(days))
	for _, d := range days {
		if _, ok := index[d]; ok && !seen[d] {
			out = append(out, d)
			seen[d] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return index[out[i]] < index[out[j]] })
	return out
}

func weekdayOffset(day string) int {
	for i, d := range weekdayOrder {
		if d == day {
			return i
		}
	}
	return 0
}

// assignSessionTypes picks a session type for each of a week's available
// days, in calendar order. The rider's last available day of the week
// always carries the long session — the slot most riders training around
// a job or school actually have the most time for (a weekend, if the
// profile names one) — and Build/Peak each add one harder mid-week session
// on top of it. Base and Taper add none: Base is aerobic volume only, and
// Taper is explicitly about shedding load, not adding a new hard day on
// top of what's already there.
func assignSessionTypes(phase periodization.Phase, recovery bool, n int) []sessionType {
	types := make([]sessionType, n)
	for i := range types {
		types[i] = sessionEasy
	}
	if recovery || n == 0 {
		return types
	}
	types[n-1] = sessionLong

	switch phase {
	case periodization.PhaseBuild:
		if n >= 2 {
			types[midWeekSlot(n)] = sessionTempo
		}
	case periodization.PhasePeak:
		if n >= 2 {
			types[midWeekSlot(n)] = sessionInterval
		}
	}
	return types
}

// midWeekSlot picks a day for the week's one harder session, biased toward
// the middle of the available days so it sits between two easier days
// rather than immediately before or after the long day.
func midWeekSlot(n int) int {
	if n <= 2 {
		return 0
	}
	return (n - 1) / 2
}

// sessionWeight is each session type's share of the week's target hours,
// relative to an easy day's share of 1 — a long day runs longest, tempo
// next, an interval session shortest of the non-easy types (time spent
// hard is not time spent long). Shares are normalised in sessionShares, so
// only the ratios between these matter, not their absolute values.
var sessionWeight = map[sessionType]float64{
	sessionEasy:     1.0,
	sessionLong:     1.8,
	sessionTempo:    1.3,
	sessionInterval: 1.1,
}

// sessionShares normalises sessionWeight across one week's actual session
// types so they sum to exactly 1 — which is what makes the resulting
// workouts' total duration land on the week's own TargetHours rather than
// drift from it.
func sessionShares(types []sessionType) []float64 {
	total := 0.0
	for _, t := range types {
		total += sessionWeight[t]
	}
	shares := make([]float64, len(types))
	if total == 0 {
		return shares
	}
	for i, t := range types {
		shares[i] = sessionWeight[t] / total
	}
	return shares
}

func buildSession(t sessionType, hours float64, sport model.Sport, profile workout.RiderProfile) workout.CreateWorkoutRequest {
	name, intensity := sessionLabel(t, sport)
	// Rounded to a whole second: hours*3600 otherwise carries binary
	// floating-point noise (e.g. 4535.999999999999) into both the API
	// response and, eventually, fitworkout's own duration_time field.
	seconds := math.Round(hours * 3600)

	var main workout.WorkoutStep
	if t == sessionInterval {
		main = intervalBlock(seconds, sport, profile)
	} else {
		target, low, high := zoneTarget(t, sport, profile)
		main = workout.WorkoutStep{
			Name: name, Intensity: intensity, Duration: workout.DurationTime, Seconds: seconds,
			Target: target, TargetLow: low, TargetHigh: high,
		}
	}

	return workout.CreateWorkoutRequest{
		Sport:       sport,
		Name:        name,
		Description: "Generated from the periodization plan.",
		Steps: []workout.WorkoutStep{
			{Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationOpen, Target: workout.TargetOpen},
			main,
			{Name: "Cooldown", Intensity: workout.IntensityCooldown, Duration: workout.DurationOpen, Target: workout.TargetOpen},
		},
	}
}

func sessionLabel(t sessionType, sport model.Sport) (string, workout.Intensity) {
	running := sport == model.SportRunning
	switch t {
	case sessionLong:
		if running {
			return "Long run", workout.IntensityActive
		}
		return "Long ride", workout.IntensityActive
	case sessionTempo:
		if running {
			return "Tempo run", workout.IntensityActive
		}
		return "Tempo ride", workout.IntensityActive
	case sessionInterval:
		if running {
			return "Interval session", workout.IntensityInterval
		}
		return "VO2max intervals", workout.IntensityInterval
	default:
		if running {
			return "Easy run", workout.IntensityActive
		}
		return "Endurance ride", workout.IntensityActive
	}
}

// zoneTarget picks the most precise target this rider's profile supports,
// in the same priority order the rest of this app already reads a profile
// in: a sport-specific threshold (FTP for cycling, threshold pace for
// running) first, heart rate as the fallback that works for either sport,
// open when neither is set — never inferred, never guessed, matching
// RiderProfile's own field comments on why these are rider-entered only.
func zoneTarget(t sessionType, sport model.Sport, profile workout.RiderProfile) (workout.TargetType, float64, float64) {
	switch {
	case sport == model.SportCycling && profile.FTPWatts > 0:
		low, high := powerZone(t)
		return workout.TargetPower, profile.FTPWatts * low, profile.FTPWatts * high
	case sport == model.SportRunning && profile.ThresholdPaceSecPerKM > 0:
		low, high := paceZone(t)
		threshold := 1000 / profile.ThresholdPaceSecPerKM // m/s
		return workout.TargetPace, threshold * low, threshold * high
	case profile.MaxHR > 0:
		low, high := hrZone(t)
		return workout.TargetHeartRate, float64(profile.MaxHR) * low, float64(profile.MaxHR) * high
	default:
		return workout.TargetOpen, 0, 0
	}
}

// powerZone is a fraction of FTP. Coggan's own published training zones,
// simplified to the two this package needs beyond intervalOnTarget/
// intervalOffTarget below.
func powerZone(t sessionType) (low, high float64) {
	if t == sessionTempo {
		return 0.76, 0.90
	}
	return 0.55, 0.75 // Easy, Long
}

// paceZone is a fraction of threshold *speed* (metres/second) — a slower
// zone is a smaller fraction of threshold speed, the opposite direction
// from powerZone's fractions of FTP, because pace and power point opposite
// ways relative to effort.
func paceZone(t sessionType) (low, high float64) {
	if t == sessionTempo {
		return 0.95, 1.00
	}
	return 0.80, 0.90 // Easy, Long
}

// hrZone is a fraction of max heart rate.
func hrZone(t sessionType) (low, high float64) {
	if t == sessionTempo {
		return 0.80, 0.88
	}
	return 0.60, 0.75 // Easy, Long
}

// intervalBlock builds a repeat block sized to fill approximately
// totalSeconds of work, at fixed 3-minutes-on/2-minutes-off reps — see this
// package's own doc comment on why that split is fixed rather than tuned
// per sport or phase.
func intervalBlock(totalSeconds float64, sport model.Sport, profile workout.RiderProfile) workout.WorkoutStep {
	// Rounded rather than floored: a fixed rep length can never land exactly
	// on totalSeconds, so round to the nearest rep instead of always losing
	// up to one whole rep's worth of the budgeted time.
	reps := int(math.Round(totalSeconds / (intervalOnSeconds + intervalOffSeconds)))
	if reps < minIntervalReps {
		reps = minIntervalReps
	}

	onTarget, onLow, onHigh := intervalOnTarget(sport, profile)
	offTarget, offLow, offHigh := intervalOffTarget(sport, profile)

	return workout.WorkoutStep{
		Name:   "Intervals",
		Repeat: reps,
		Steps: []workout.WorkoutStep{
			{Name: "On", Intensity: workout.IntensityInterval, Duration: workout.DurationTime, Seconds: intervalOnSeconds,
				Target: onTarget, TargetLow: onLow, TargetHigh: onHigh},
			{Name: "Recovery", Intensity: workout.IntensityRecovery, Duration: workout.DurationTime, Seconds: intervalOffSeconds,
				Target: offTarget, TargetLow: offLow, TargetHigh: offHigh},
		},
	}
}

func intervalOnTarget(sport model.Sport, profile workout.RiderProfile) (workout.TargetType, float64, float64) {
	switch {
	case sport == model.SportCycling && profile.FTPWatts > 0:
		return workout.TargetPower, profile.FTPWatts * 1.06, profile.FTPWatts * 1.20
	case sport == model.SportRunning && profile.ThresholdPaceSecPerKM > 0:
		threshold := 1000 / profile.ThresholdPaceSecPerKM
		return workout.TargetPace, threshold * 1.08, threshold * 1.15
	case profile.MaxHR > 0:
		return workout.TargetHeartRate, float64(profile.MaxHR) * 0.90, float64(profile.MaxHR) * 0.97
	default:
		return workout.TargetOpen, 0, 0
	}
}

func intervalOffTarget(sport model.Sport, profile workout.RiderProfile) (workout.TargetType, float64, float64) {
	switch {
	case sport == model.SportCycling && profile.FTPWatts > 0:
		return workout.TargetPower, profile.FTPWatts * 0.40, profile.FTPWatts * 0.50
	case sport == model.SportRunning && profile.ThresholdPaceSecPerKM > 0:
		threshold := 1000 / profile.ThresholdPaceSecPerKM
		return workout.TargetPace, threshold * 0.60, threshold * 0.70
	case profile.MaxHR > 0:
		return workout.TargetHeartRate, float64(profile.MaxHR) * 0.50, float64(profile.MaxHR) * 0.60
	default:
		return workout.TargetOpen, 0, 0
	}
}
