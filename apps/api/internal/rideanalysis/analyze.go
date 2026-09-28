package rideanalysis

import (
	"math"
	"time"

	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Outcome is how a completed session compared to what was planned for it —
// see the spec's "Outcome" rules under "Scoring against the plan".
type Outcome string

const (
	OutcomeNailed     Outcome = "nailed"
	OutcomeCompleted  Outcome = "completed"
	OutcomeStruggled  Outcome = "struggled"
	OutcomeIncomplete Outcome = "incomplete"
	OutcomeUnplanned  Outcome = "unplanned"
)

// LoadSource records which of the spec's fallback chain produced Load —
// see the spec's "Fallback order for the session's training load".
type LoadSource string

const (
	LoadSourceFITPower    LoadSource = "fit_power"
	LoadSourceProviderTSS LoadSource = "provider_tss"
	LoadSourceFITHR       LoadSource = "fit_hr"
	LoadSourceEstimate    LoadSource = "estimate"
)

// StepResult is one planned step scored against what the ride actually did.
// Target is the step's workout.TargetType as a string ("power" etc.); Low/
// High are that target's planned range. Actual is always in the target's
// own physical unit (W, bpm, m/s) — a lap's average for a lap-scored step,
// or the whole ride's average (over samples that carry the metric) for the
// no-laps fallback, never a percentage. InTargetPct is that fallback's own
// number, the percentage of ride time spent inside [Low, High]; it is 0 for
// a lap-scored step, which is judged by average rather than by time.
type StepResult struct {
	Index       int
	Name        string
	Target      string
	Low         float64
	High        float64
	Actual      float64
	InTargetPct float64
	Result      string // "hit" | "under" | "over"
	// Hard is isHardStep's own verdict on the planned step this result
	// scored — an interval/active step with a real target, as opposed to a
	// recovery, warmup or cooldown step that happens to carry one too (a
	// generated interval session's "Recovery" step has a real power target,
	// not TargetOpen — see scheduler.intervalOffTarget). hitFraction already
	// used isHardStep to decide the outcome; this exposes the same verdict
	// per step so a caller naming which steps were "hit of hard" (adapter's
	// struggle-fatigue reason, say) does not have to re-flatten the plan and
	// re-derive it — and, without this, would silently count every scored
	// step, recovery included, doubling a 4-rep session's denominator to 8.
	Hard bool
}

// Summary is a provider's own ride summary numbers — present even when no
// FIT file was fetched or decoded (see Input.Activity). A 0 value means
// unknown, never a real zero: no provider reports 0 W average power or 0
// TSS for an actual ride.
type Summary struct {
	DurationSeconds float64
	AvgPower        float64
	AvgHR           int
	NormalizedPower float64
	TSS             float64
	IntensityFactor float64
}

// Input is everything Analyze needs to score one ride. Activity is nil when
// only a provider summary is available (no FIT file, or it failed to
// download/decode — see the spec's "Fetching": a Warn, not a failure).
// Planned is nil when nothing was scheduled for that date/sport.
type Input struct {
	Activity *filedef.Activity
	Summary  Summary
	Planned  *workout.Workout
	Profile  workout.RiderProfile
}

// Analysis is the result of scoring one ride: its own metrics (independent
// of whether a plan exists) plus, when Planned was set, the per-step
// comparison and the session's Outcome.
type Analysis struct {
	Outcome    Outcome
	LoadSource LoadSource
	Load       float64

	NormalizedPower float64
	IntensityFactor float64
	TSS             float64
	DurationRatio   float64

	// MaxHR and BestSpeed1200/BestSpeed1800 feed threshold detection
	// (internal/thresholds): the ride's peak heart rate and its best 20-
	// and 30-minute average speeds. All three are 0 when in.Activity is
	// nil — there is no per-second stream to compute them from, the same
	// reasoning that leaves PowerZoneSeconds/HRZoneSeconds/PowerCurve empty
	// in that case.
	MaxHR         int
	BestSpeed1200 float64
	BestSpeed1800 float64

	PowerZoneSeconds [7]int
	HRZoneSeconds    [5]int
	PowerCurve       map[int]float64

	Steps []StepResult
}

// FlattenSteps turns a workout's nested step tree into the same flat
// sequence internal/fitworkout.Encode writes as workout_step FIT messages:
// a repeat block's children first (recursively flattened, so a repeat
// block nested inside another flattens all the way down), then a single
// marker step standing in for the block itself. That marker mirrors what
// the encoder actually writes for a repeat block (see fitworkout.go's
// appendSteps): an open-target step, so it is never itself scored, carrying
// the block's own Name as its label and Intensity forced to "active" (the
// encoder's own hardcoded choice for the marker, independent of the block's
// intensity) rather than DurationTime/Repeat, which have no meaning for a
// marker step.
//
// FIT lap messages carry wkt_step_index pointing into exactly this
// sequence, which is why MatchPlanned's caller must flatten with this
// function (never the raw, nested Steps) before mapping a lap back to a
// planned step — confirmed against the real encoder in
// TestFlattenMatchesFITEncode rather than assumed from the spec's prose.
func FlattenSteps(steps []workout.WorkoutStep) []workout.WorkoutStep {
	var out []workout.WorkoutStep
	appendFlattened(&out, steps)
	return out
}

func appendFlattened(out *[]workout.WorkoutStep, steps []workout.WorkoutStep) {
	for _, s := range steps {
		if s.Repeat > 1 {
			appendFlattened(out, s.Steps)
			*out = append(*out, workout.WorkoutStep{
				Name:      s.Name,
				Intensity: workout.IntensityActive,
				Duration:  workout.DurationOpen,
				Target:    workout.TargetOpen,
			})
			continue
		}
		*out = append(*out, s)
	}
}

// stepToleranceLow and stepToleranceHigh widen a step's planned [low, high]
// target before comparing a lap's actual average against it — a rider or a
// device's own averaging is never exact, and TargetLow==TargetHigh (a single
// target rather than a range) would otherwise make "hit" nearly
// unreachable. Spec: "[low x 0.95, high x 1.05] -> hit, under or over".
const (
	stepToleranceLow  = 0.95
	stepToleranceHigh = 1.05
)

// mainStepHitFraction is the fraction of ride time inside a step's target
// range that counts the no-laps fallback score as "hit" — the spec's own
// number ("no step laps ... ≥ 70% counts as hit").
const mainStepHitFraction = 0.70

// hardStepHitFraction is the fraction of hard steps that must be hit for the
// outcome to avoid "struggled" — spec: "fewer than 75% of hard steps hit".
const hardStepHitFraction = 0.75

// durationRatioIncomplete and durationRatioStruggled are the spec's own
// duration-ratio cutoffs: below the first the ride barely happened at all
// (incomplete beats any per-step scoring); below the second the rider did
// not ride long enough to call the session solid even if every step they
// did ride was on target (struggled).
const (
	durationRatioIncomplete = 0.5
	durationRatioStruggled  = 0.8
	durationRatioNailed     = 0.9
)

// classifyStep compares a measured value against a step's planned range
// with the tolerance band above.
func classifyStep(actual, low, high float64) string {
	switch {
	case actual < low*stepToleranceLow:
		return "under"
	case actual > high*stepToleranceHigh:
		return "over"
	default:
		return "hit"
	}
}

// isHardStep reports whether a flattened step counts toward the "hard
// steps hit" fraction the outcome rules use — spec/resolution: intensity
// interval or active, with a real (non-open) target.
func isHardStep(s workout.WorkoutStep) bool {
	return (s.Intensity == workout.IntensityInterval || s.Intensity == workout.IntensityActive) &&
		s.Target != workout.TargetOpen
}

// mainStep picks the step the no-laps fallback and the no-hard-steps
// "nailed" rule score against: the longest time-duration step with a real
// target — resolution: "the longest time-duration step with a non-open
// target". Ties keep the first one found, which is exactly right for a
// repeat block's identical children.
func mainStep(flattened []workout.WorkoutStep) (int, *workout.WorkoutStep) {
	best := -1
	for i, s := range flattened {
		if s.Duration != workout.DurationTime || s.Target == workout.TargetOpen {
			continue
		}
		if best == -1 || s.Seconds > flattened[best].Seconds {
			best = i
		}
	}
	if best == -1 {
		return -1, nil
	}
	return best, &flattened[best]
}

// metricValue reads the one field of Sample a step's TargetType asks for,
// reporting whether that second actually carries the reading.
func metricValue(s Sample, target workout.TargetType) (float64, bool) {
	switch target {
	case workout.TargetPower:
		return s.Power, s.HasPower
	case workout.TargetHeartRate:
		return s.HeartRate, s.HasHR
	case workout.TargetPace:
		return s.Speed, s.HasSpeed
	default:
		return 0, false
	}
}

// averageOverRange is the lap-scoring rule's "the lap's average of the
// step's target metric" — computed from the resampled 1 Hz samples over
// [startSec, endSec), not from the FIT lap's own avg_power/avg_heart_rate
// fields, since a lap that only carries a wkt_step_index (no averages of
// its own — the ordinary case for a device that did not compute them) would
// otherwise have nothing to score against.
func averageOverRange(samples []Sample, startSec, endSec int, target workout.TargetType) float64 {
	if startSec < 0 {
		startSec = 0
	}
	if endSec > len(samples) {
		endSec = len(samples)
	}
	if startSec >= endSec {
		return 0
	}
	var sum float64
	var n int
	for _, s := range samples[startSec:endSec] {
		if v, ok := metricValue(s, target); ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// timeInRangeFraction is the no-laps fallback's "time in its target range
// across the ride": the fraction of samples that both carry the step's
// metric and fall inside [low, high] (the plain range, not the lap-scoring
// tolerance band — this rule already has its own threshold, 70%, rather
// than widening the range too).
func timeInRangeFraction(samples []Sample, low, high float64, target workout.TargetType) float64 {
	var inRange, total int
	for _, s := range samples {
		v, ok := metricValue(s, target)
		if !ok {
			continue
		}
		total++
		if v >= low && v <= high {
			inRange++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(inRange) / float64(total)
}

// hasHRData reports whether any sample carries a heart-rate reading —
// gating the FIT-HR load fallback on real data rather than a maxHR-only
// profile with an HR-less ride.
func hasHRData(samples []Sample) bool {
	for _, s := range samples {
		if s.HasHR {
			return true
		}
	}
	return false
}

// scoreLaps scores every FIT lap that carries a wkt_step_index against the
// planned step it points to (see FlattenSteps's own doc comment for why the
// index must be resolved against the flattened sequence). Laps pointing at
// an open-target step (including a repeat block's own marker step) are not
// scored — spec: "Steps with an open target are not scored."
func scoreLaps(act *filedef.Activity, samples []Sample, flattened []workout.WorkoutStep) []StepResult {
	if len(act.Laps) == 0 || len(act.Records) == 0 {
		return nil
	}
	rideStart := act.Records[0].Timestamp

	var out []StepResult
	for _, lap := range act.Laps {
		if lap.WktStepIndex == typedef.MessageIndexInvalid {
			continue
		}
		idx := int(lap.WktStepIndex)
		if idx < 0 || idx >= len(flattened) {
			continue
		}
		step := flattened[idx]
		if step.Target == workout.TargetOpen {
			continue
		}
		startSec := int(lap.StartTime.Sub(rideStart).Round(time.Second).Seconds())
		endSec := int(lap.Timestamp.Sub(rideStart).Round(time.Second).Seconds())
		actual := averageOverRange(samples, startSec, endSec, step.Target)
		out = append(out, StepResult{
			Index:  idx,
			Name:   step.Name,
			Target: string(step.Target),
			Low:    step.TargetLow,
			High:   step.TargetHigh,
			Actual: actual,
			Result: classifyStep(actual, step.TargetLow, step.TargetHigh),
			Hard:   isHardStep(step),
		})
	}
	return out
}

// stepScored reports whether some StepResult already scored the flattened
// step at idx — used to decide whether the main-step time-in-range fallback
// still needs to run alongside laps that scored other steps.
func stepScored(steps []StepResult, idx int) bool {
	for _, s := range steps {
		if s.Index == idx {
			return true
		}
	}
	return false
}

// mainStepWindow returns the samples the main-step time-in-range fallback
// should judge: every second of the ride *except* the ones a lap already
// attributed to some other planned step. Without this, a lap'd warmup or
// cooldown at an easy, out-of-range wattage dilutes the main step's own
// average and time-in-range fraction — round 2's own finding: a 300 s
// lapped warmup at 100 W pulled a true 180 W / 80%-in-range main step down
// to 173 W / 73%, and a long enough easy lap can flip a nailed main step to
// "under" purely because of time it never claimed to cover. A lap with no
// wkt_step_index at all (never attributed to any planned step) does not
// exclude its seconds — only a lap that FIT explicitly mapped elsewhere
// does. When mainIdx itself has no lap (the caller only reaches this
// function via stepScored being false for it), there is nothing of its own
// to additionally exclude.
func mainStepWindow(act *filedef.Activity, samples []Sample, mainIdx int, flattened []workout.WorkoutStep) []Sample {
	if act == nil || len(act.Records) == 0 || len(act.Laps) == 0 {
		return samples
	}
	excluded := make([]bool, len(samples))
	rideStart := act.Records[0].Timestamp
	for _, lap := range act.Laps {
		if lap.WktStepIndex == typedef.MessageIndexInvalid {
			continue
		}
		idx := int(lap.WktStepIndex)
		if idx < 0 || idx >= len(flattened) || idx == mainIdx {
			continue
		}
		startSec := int(lap.StartTime.Sub(rideStart).Round(time.Second).Seconds())
		endSec := int(lap.Timestamp.Sub(rideStart).Round(time.Second).Seconds())
		if startSec < 0 {
			startSec = 0
		}
		if endSec > len(samples) {
			endSec = len(samples)
		}
		for sec := startSec; sec < endSec; sec++ {
			excluded[sec] = true
		}
	}

	window := make([]Sample, 0, len(samples))
	for i, s := range samples {
		if !excluded[i] {
			window = append(window, s)
		}
	}
	return window
}

// scoreMainStepByTime is the spec's "no step laps" fallback — also used
// whenever no lap mapped to the main step specifically, even if other laps
// scored other steps, so a planned workout is scored whenever it has
// anything to score at all. samples is expected to already be mainStepWindow's
// output — the ride's seconds minus whatever another lap already claimed —
// not the raw per-second stream, so an easy lapped warmup/cooldown cannot
// dilute the main step's own numbers. The hit rule is time in range
// (InTargetPct >= mainStepHitFraction), but Actual still reports the
// window's average of the target metric in its own physical unit, matching
// every other StepResult — only InTargetPct carries the percentage this
// fallback actually judges by.
func scoreMainStepByTime(samples []Sample, idx int, step workout.WorkoutStep) StepResult {
	frac := timeInRangeFraction(samples, step.TargetLow, step.TargetHigh, step.Target)
	actual := averageOverRange(samples, 0, len(samples), step.Target)
	result := "under"
	if frac >= mainStepHitFraction {
		result = "hit"
	}
	return StepResult{
		Index:       idx,
		Name:        step.Name,
		Target:      string(step.Target),
		Low:         step.TargetLow,
		High:        step.TargetHigh,
		Actual:      actual,
		InTargetPct: frac * 100,
		Result:      result,
		Hard:        isHardStep(step),
	}
}

// scoreMainStepBySummary is the no-Activity fallback: "compare summary NP
// (or avg power) with the main step's target as a single-lap score" — a
// plain classifyStep call, the same rule a real lap gets, since a single
// provider-reported number is exactly a one-lap ride as far as scoring goes.
func scoreMainStepBySummary(summary Summary, idx int, step workout.WorkoutStep) StepResult {
	actual := summary.NormalizedPower
	if actual == 0 {
		actual = summary.AvgPower
	}
	return StepResult{
		Index:  idx,
		Name:   step.Name,
		Target: string(step.Target),
		Low:    step.TargetLow,
		High:   step.TargetHigh,
		Actual: actual,
		Result: classifyStep(actual, step.TargetLow, step.TargetHigh),
		Hard:   isHardStep(step),
	}
}

// hitFraction turns a set of scored steps into the outcome rules' "were the
// hard steps hit" number. When the plan has no hard steps at all, the
// no-hard-steps rule substitutes the main step's own result — resolution:
// "a workout with no hard steps is nailed when ratio >= 0.9 and its main
// step is hit". The bool return is false when there is nothing to score at
// all (no hard steps and no main step with a target), in which case the
// outcome comes from the duration ratio alone.
func hitFraction(steps []StepResult, mainIdx int) (frac float64, scorable bool) {
	var hardTotal, hardHit int
	for _, s := range steps {
		if !s.Hard {
			continue
		}
		hardTotal++
		if s.Result == "hit" {
			hardHit++
		}
	}
	if hardTotal > 0 {
		return float64(hardHit) / float64(hardTotal), true
	}
	if mainIdx < 0 {
		return 0, false
	}
	for _, s := range steps {
		if s.Index == mainIdx {
			if s.Result == "hit" {
				return 1, true
			}
			return 0, true
		}
	}
	// Analyze always appends a main-step score (by lap, or by the
	// whole-ride time-in-range fallback) whenever main exists, so this is
	// unreached in practice — kept as a safe default rather than a panic
	// for a caller that builds Analysis some other way.
	return 0, false
}

// outcomeFrom applies the spec's outcome rules in priority order: unplanned
// (no caller reaches here without Planned set — see Analyze), incomplete,
// struggled, nailed, else completed.
func outcomeFrom(ratio float64, frac float64, scorable bool) Outcome {
	if ratio < durationRatioIncomplete {
		return OutcomeIncomplete
	}
	if !scorable {
		// Resolution: "the workout has nothing to score -> outcome from
		// duration ratio alone ... use completed when ratio >= 0.5, since
		// there is nothing to have nailed."
		return OutcomeCompleted
	}
	if frac < hardStepHitFraction || ratio < durationRatioStruggled {
		return OutcomeStruggled
	}
	if frac >= 1 && ratio >= durationRatioNailed {
		return OutcomeNailed
	}
	return OutcomeCompleted
}

// Analyze scores one completed ride: its own metrics (load, zones, power
// curve — independent of any plan) plus, when in.Planned is set, per-step
// results and an outcome. See package docs/superpowers/specs/2026-09-27-
// training-adaptation-design.md, "Analysis" and "Scoring against the plan".
func Analyze(in Input) Analysis {
	var a Analysis
	var samples []Sample
	rideSeconds := in.Summary.DurationSeconds

	if in.Activity != nil {
		samples = Resample(in.Activity.Records)
		rideSeconds = float64(len(samples))

		a.NormalizedPower = NormalizedPower(samples)
		if in.Profile.FTPWatts > 0 {
			a.IntensityFactor, a.TSS = PowerTSS(len(samples), a.NormalizedPower, in.Profile.FTPWatts)
		}
		a.PowerZoneSeconds = PowerZoneSeconds(samples, in.Profile.FTPWatts)
		a.HRZoneSeconds = HRZoneSeconds(samples, in.Profile.MaxHR)
		a.PowerCurve = PowerCurve(samples)
		a.MaxHR = MaxHR(samples)
		a.BestSpeed1200, a.BestSpeed1800 = BestSpeeds(samples)
	} else {
		// No FIT file decoded (never fetched, or the download/decode
		// failed — spec: a Warn, the sync still succeeds with summary
		// data). Metrics come from whatever the provider's own summary
		// reported; zones and the power curve need a per-second stream
		// this path does not have, so they stay empty.
		a.NormalizedPower = in.Summary.NormalizedPower
		a.IntensityFactor = in.Summary.IntensityFactor
		a.TSS = in.Summary.TSS
	}

	// Fallback order for the session's training load — spec: "FIT power
	// TSS -> provider TSS (summary) -> FIT HR load -> the existing
	// workout.TrainingLoad estimate."
	switch {
	case in.Activity != nil && a.NormalizedPower > 0 && in.Profile.FTPWatts > 0:
		a.LoadSource, a.Load = LoadSourceFITPower, a.TSS
	case in.Summary.TSS > 0:
		a.LoadSource, a.Load = LoadSourceProviderTSS, in.Summary.TSS
	case in.Activity != nil && hasHRData(samples) && in.Profile.MaxHR > 0:
		a.LoadSource = LoadSourceFITHR
		a.Load = HRLoad(samples, in.Profile.MaxHR, in.Profile.RestingHR)
	default:
		a.LoadSource = LoadSourceEstimate
		a.Load = workout.TrainingLoad(rideSeconds, in.Summary.AvgPower, in.Summary.AvgHR, in.Profile)
	}

	if in.Planned == nil {
		a.Outcome = OutcomeUnplanned
		return a
	}

	flattened := FlattenSteps(in.Planned.Steps)
	mainIdx, main := mainStep(flattened)

	plannedSeconds := workout.PlannedSeconds(in.Planned.Steps)
	switch {
	case plannedSeconds > 0:
		a.DurationRatio = rideSeconds / plannedSeconds
	case rideSeconds > 0:
		a.DurationRatio = 1
	default:
		a.DurationRatio = 0
	}

	switch {
	case in.Activity != nil:
		a.Steps = scoreLaps(in.Activity, samples, flattened)
		// Score the main step whenever no lap mapped to it specifically —
		// not only when no lap scored anything at all. A device that ran
		// the workout but only lapped the warmup, say, still leaves the
		// main step itself unscored by any lap, and the ruling is that a
		// planned workout is scored whenever it has anything to score,
		// rather than only in the all-or-nothing "free ride" case.
		if main != nil && !stepScored(a.Steps, mainIdx) {
			// Judge only the seconds no other lap already claimed — see
			// mainStepWindow's own doc comment for why (round 2's finding:
			// an easy lapped warmup/cooldown otherwise dilutes the main
			// step's numbers and can flip its verdict).
			if window := mainStepWindow(in.Activity, samples, mainIdx, flattened); len(window) > 0 {
				a.Steps = append(a.Steps, scoreMainStepByTime(window, mainIdx, *main))
			}
		}
	case main != nil:
		a.Steps = []StepResult{scoreMainStepBySummary(in.Summary, mainIdx, *main)}
	}

	frac, scorable := hitFraction(a.Steps, mainIdx)
	a.Outcome = outcomeFrom(a.DurationRatio, frac, scorable)
	return a
}

// MatchPlanned finds the planned workout for the given date and sport whose
// planned duration is closest to the ride's own — spec: "the planned
// workout for the same date and sport ...; with several, the one whose
// planned duration is closest to the ride's." Returns nil when nothing
// planned matches date and sport.
func MatchPlanned(date, sport string, rideSeconds float64, planned []workout.Workout) *workout.Workout {
	var best *workout.Workout
	bestDiff := math.Inf(1)
	for i := range planned {
		w := &planned[i]
		if w.Date != date || string(w.Sport) != sport {
			continue
		}
		diff := math.Abs(workout.PlannedSeconds(w.Steps) - rideSeconds)
		if diff < bestDiff {
			best, bestDiff = w, diff
		}
	}
	return best
}
