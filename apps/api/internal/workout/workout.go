// Package workout is the manual workout builder: a rider's training goals,
// their own fitness profile, and the structured workouts built for them —
// warmup/interval/recovery/cooldown steps with power/heart-rate/pace/cadence
// targets, the kind of session a head unit can guide a rider through rather
// than a route they follow.
//
// This is Phase A of the plan in docs/training-plan.md: data and a manual
// builder only. Nothing here pushes to a provider, pulls metrics back, or
// generates a plan from a goal — research done before writing this package
// found that neither Garmin's nor Wahoo's real structured-workout push is a
// FIT upload the way course push is (see that doc's "Structured workouts and
// the providers"), so provider push is later, separate work, not assumed
// here. What proves the FIT encoding in this phase is a manual export a
// rider can copy onto any device by hand — see internal/fitworkout and the
// `domestique fit-workout` CLI command, the same role `domestique fit`
// already plays for courses.
//
// One package for three related tables, the same reasoning internal/crew
// gives for crews and crew_members sharing one: goals, rider profiles and
// workouts are tightly coupled read/write together (a workout is built with
// a goal and a profile in view) even though each is its own table.
package workout

import (
	"errors"

	"github.com/wncservices/domestique/apps/api/internal/fitworkout"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// ErrGoalNotFound is returned for a goal id nothing matches.
var ErrGoalNotFound = errors.New("no such goal")

// ErrWorkoutNotFound is returned for a workout id nothing matches.
var ErrWorkoutNotFound = errors.New("no such workout")

// Priority is how much a goal matters relative to a rider's other goals —
// TrainingPeaks' own A/B/C vocabulary (see docs/training-plan.md), kept
// because it is already the shared language a rider coming from that tool
// or Humango would expect: an A race is worth peaking for, a B or C race is
// good practice on the way there, not worth its own taper.
type Priority string

const (
	PriorityA Priority = "A"
	PriorityB Priority = "B"
	PriorityC Priority = "C"
)

// Goal is a race or event a rider is training toward.
type Goal struct {
	ID    string
	Rider string
	Name  string
	Sport model.Sport
	// EventDate is when it happens, "YYYY-MM-DD".
	EventDate string
	Priority  Priority
	// TargetDistanceM and TargetElevationM are 0 when not stated — not every
	// goal has a fixed course (a time trial does; a stage race might not).
	TargetDistanceM  float64
	TargetElevationM float64
	Notes            string
	CreatedAt        string
	UpdatedAt        string
}

// RiderProfile is what the planner (once it exists — see docs/training-plan.md
// Phase C) will need about a rider's own fitness, and what a manual builder
// already wants today to suggest sensible targets. One row per rider: Rider
// is the primary key, not a generated id, the same reasoning
// internal/settings gives for a single deployment-wide row — there is
// exactly one profile per rider, so there is nothing to list or slug.
type RiderProfile struct {
	Rider string
	// FTPWatts is a cyclist's functional threshold power. 0 means unset —
	// never inferred straight from provider data (docs/training-plan.md is
	// explicit that neither provider reliably exposes it). It can, however,
	// be auto-estimated from the rider's own completed sessions (see
	// internal/fitnesstest.EstimateFTP) — FTPEstimated marks exactly that
	// case, so the UI can label it as an estimate and a future sync can
	// keep refining it, right up until the rider explicitly saves the
	// profile themselves, which always clears the flag: a value the rider
	// has looked at and confirmed is never silently touched again.
	FTPWatts     float64
	FTPEstimated bool
	// ThresholdPaceSecPerKM is a runner's threshold pace. 0 means unset.
	// Unlike FTPWatts, this is never auto-estimated today — see
	// internal/fitnesstest's own doc comment for why a training run's pace
	// is a much noisier fitness proxy than a ride's average power.
	ThresholdPaceSecPerKM float64
	// MaxHR and RestingHR are 0 when unset. MaxHR is never auto-estimated,
	// for a sharper reason than pace: this app only ever stores a
	// session's *average* HR, and average is always at or below true max —
	// see internal/fitnesstest's own doc comment. The only path to a
	// number here beyond the rider typing one in is MaxHRTestWorkout.
	MaxHR     int
	RestingHR int
	// AvailableDays is which weekdays the rider can train, lowercase
	// three-letter abbreviations ("mon", "tue", ...). Nil means not stated.
	AvailableDays []string
	// HoursPerAvailableDay is a rough budget, not a hard cap — the manual
	// builder and, later, the planner use it to size sessions.
	HoursPerAvailableDay float64
	ExperienceLevel      string
	UpdatedAt            string
}

// Intensity labels what a step is for — Garmin's own FIT Workout profile
// vocabulary (typedef.Intensity), kept as the domain's own vocabulary too
// since fitworkout.Encode maps it straight across with no translation to
// invent or get wrong.
type Intensity string

const (
	IntensityWarmup   Intensity = "warmup"
	IntensityActive   Intensity = "active"
	IntensityRest     Intensity = "rest"
	IntensityCooldown Intensity = "cooldown"
	IntensityRecovery Intensity = "recovery"
	IntensityInterval Intensity = "interval"
	IntensityOther    Intensity = "other"
)

// DurationType is how a step's length is measured.
type DurationType string

const (
	// DurationTime ends the step after Seconds.
	DurationTime DurationType = "time"
	// DurationDistance ends the step after Meters.
	DurationDistance DurationType = "distance"
	// DurationOpen ends the step on a manual lap press — no fixed length,
	// the ordinary shape for a warmup or cooldown a rider judges by feel.
	DurationOpen DurationType = "open"
)

// TargetType is what a step asks the rider to hold, if anything.
type TargetType string

const (
	// TargetOpen sets no target — the device shows the metric, judges nothing.
	TargetOpen TargetType = "open"
	// TargetPower is watts, absolute (not a percentage of FTP — see
	// WorkoutStep's own field comments for why this package stays with
	// absolute values rather than the FIT profile's optional
	// percentage-of-threshold encoding).
	TargetPower TargetType = "power"
	// TargetHeartRate is beats per minute, absolute.
	TargetHeartRate TargetType = "heart_rate"
	// TargetPace is metres per second, absolute.
	TargetPace TargetType = "pace"
	// TargetCadence is revolutions/steps per minute.
	TargetCadence TargetType = "cadence"
)

// WorkoutStep is one step of a structured workout, or a repeat block
// containing steps of its own.
//
// A repeat block (Repeat > 1) carries its own Name/Intensity as a label for
// the block as a whole, and its Steps as the sequence that repeats — this
// nests the way a rider actually thinks about "6 x 3 minutes at threshold
// with 2 minutes recovery" and the way TrainingPeaks/Zwift builders present
// it, even though the FIT Workout profile itself has no nested block
// concept: internal/fitworkout flattens this into FIT's own flat
// step-list-plus-repeat-marker convention (a repeat_until_steps_cmplt step
// whose duration_value points back at the first child step's message_index)
// at encode time, so nothing above that package needs to know FIT's shape
// at all.
type WorkoutStep struct {
	Name      string
	Intensity Intensity
	Duration  DurationType
	// Seconds applies when Duration is DurationTime.
	Seconds float64
	// Meters applies when Duration is DurationDistance.
	Meters float64
	Target TargetType
	// TargetLow and TargetHigh bound the target range, in Target's own
	// units (watts / bpm / m/s / rpm). Equal values mean a single target
	// rather than a range. Both 0 when Target is TargetOpen.
	TargetLow  float64
	TargetHigh float64
	// Repeat, when 2 or more, makes this a repeat block: Steps is what
	// repeats, that many times, and every other field above is ignored
	// except Name (a label for the block, e.g. "Intervals").
	Repeat int
	Steps  []WorkoutStep
}

// Workout is one structured, riderable session.
type Workout struct {
	ID    string
	Rider string
	Sport model.Sport
	Name  string
	// GoalID links this workout to a goal it was built for. Empty when
	// built standalone — Phase A has no planner generating workouts from a
	// goal automatically, only a rider's own choice to tag one while
	// hand-building it.
	GoalID string
	// Date is when this is meant to be ridden/run, "YYYY-MM-DD". Empty
	// means unscheduled.
	Date        string
	Description string
	Steps       []WorkoutStep
	CreatedAt   string
	UpdatedAt   string
}

// CreateGoalRequest creates a goal. Rider must be set by the caller from the
// authenticated session — see AGENTS.md's "the rider comes from the
// session, never the request body," the same rule every other owned thing
// in this app already follows.
type CreateGoalRequest struct {
	Rider            string
	Name             string
	Sport            model.Sport
	EventDate        string
	Priority         Priority
	TargetDistanceM  float64
	TargetElevationM float64
	Notes            string
}

// UpdateGoalRequest edits a goal. Nil fields are left alone.
type UpdateGoalRequest struct {
	Name             *string
	Sport            *model.Sport
	EventDate        *string
	Priority         *Priority
	TargetDistanceM  *float64
	TargetElevationM *float64
	Notes            *string
}

// CreateWorkoutRequest creates a workout. Rider must be set by the caller
// from the authenticated session, same rule as CreateGoalRequest.
type CreateWorkoutRequest struct {
	Rider       string
	Sport       model.Sport
	Name        string
	GoalID      string
	Date        string
	Description string
	Steps       []WorkoutStep
}

// UpdateWorkoutRequest edits a workout. Nil fields are left alone.
type UpdateWorkoutRequest struct {
	Sport       *model.Sport
	Name        *string
	GoalID      *string
	Date        *string
	Description *string
	Steps       *[]WorkoutStep
}

// FITSteps converts to the leaf-level type fitworkout.Encode takes. The one
// place this conversion is written — both internal/api (a workout's FIT
// download endpoint) and cmd/domestique (the `fit-workout` CLI export, the
// same manual-proof role `domestique fit` plays for a route) call this
// rather than each writing their own copy of the recursion. Living here
// rather than in internal/fitworkout itself is deliberate: fitworkout stays
// a dependency-free leaf, per its own doc comment, so this direction of
// dependency (workout depending on the encoder, not the other way around)
// is the one that keeps that true.
func FITSteps(steps []WorkoutStep) []fitworkout.Step {
	out := make([]fitworkout.Step, 0, len(steps))
	for _, s := range steps {
		out = append(out, fitworkout.Step{
			Name: s.Name, Intensity: string(s.Intensity), Duration: fitworkout.Duration(s.Duration),
			Seconds: s.Seconds, Meters: s.Meters, Target: fitworkout.Target(s.Target),
			TargetLow: s.TargetLow, TargetHigh: s.TargetHigh, Repeat: s.Repeat,
			Steps: FITSteps(s.Steps),
		})
	}
	return out
}
