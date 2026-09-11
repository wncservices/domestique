// Package fitworkout turns a structured workout into a Garmin FIT workout
// file: warmup/interval/recovery/cooldown steps with power/heart-rate/pace/
// cadence targets, the FIT Workout profile
// (https://developer.garmin.com/fit/file-types/workout/) rather than the
// Course profile internal/fitcourse writes.
//
// This exists for exactly the reason internal/fitcourse exists for routes:
// research done before writing this package (see docs/training-plan.md's
// "Structured workouts and the providers") found that neither Garmin's nor
// Wahoo's real push mechanism takes a client-supplied FIT workout file the
// way Wahoo's route push takes a FIT course — Garmin's unofficial push
// wants its own JSON schema, and Wahoo's structured-workout push is a
// separate, further-gated feature in a proprietary format. So this package
// is not, today, what either provider's push adapter will send. What it is:
// a portable, provider-agnostic export any head unit can accept from a USB
// copy or its own FIT import, and the FIT-format-specific "genuinely hard"
// piece worth taking github.com/muktihari/fit as a dependency for — a
// binary format with definition messages, scaled fields and a CRC, same as
// internal/fitcourse already argues for courses.
//
// A workout file is a small, fixed shape:
//
//	file_id        type=workout, so the device files it under Workouts
//	workout        the name shown in the device's menu, and how many steps follow
//	workout_step × N   one per step, or per repeat-block marker
//
// The FIT Workout profile has no nested "repeat block" message of its own —
// a set of intervals is represented as its child steps written first,
// followed by a step whose duration_type is repeat_until_steps_cmplt, whose
// duration_value is the message_index of the first child step, and whose
// target_value is the repeat count. Encode does this flattening so nothing
// above this package needs to reproduce it.
package fitworkout

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
)

// manufacturerDevelopment is the id Garmin reserves for non-commercial and
// in-house software — the same choice internal/fitcourse makes, and for the
// same reason: claiming a real manufacturer's id would be a lie the device
// might act on.
const manufacturerDevelopment = typedef.ManufacturerDevelopment

// Duration is how a step's length is measured. Values mirror
// internal/workout.DurationType's own string constants exactly — that
// package's own doc comment is explicit about why: this package maps them
// straight across with no translation for either side to get wrong.
type Duration string

const (
	DurationTime     Duration = "time"
	DurationDistance Duration = "distance"
	DurationOpen     Duration = "open"
)

// Target is what a step asks the rider to hold, if anything. Every target
// here is an absolute value (watts, bpm, m/s, rpm) rather than the FIT
// profile's optional percentage-of-threshold encoding — simpler to reason
// about and to test, and it costs nothing a rider actually building a
// workout by hand needs: they know their own FTP or threshold pace and
// would rather type the watts directly than have this package silently
// assume one.
type Target string

const (
	TargetOpen      Target = "open"
	TargetPower     Target = "power"
	TargetHeartRate Target = "heart_rate"
	// TargetPace is named for what a runner calls it; FIT's own vocabulary
	// calls the underlying target type "speed" — encodeTarget is where
	// that one-word translation happens, the only place it needs to.
	TargetPace    Target = "pace"
	TargetCadence Target = "cadence"
)

// Step is one step of a workout, or a repeat block containing steps of its
// own — see this package's own doc comment for how a repeat block becomes
// FIT's flat step-list-plus-marker shape. Deliberately its own type rather
// than importing internal/workout.WorkoutStep: this package stays a leaf,
// the same reasoning internal/fitcourse gives for taking a plain sport
// string instead of importing internal/model — callers convert their own
// domain type across the small, stable boundary below rather than this
// package reaching upward for it.
type Step struct {
	Name      string
	Intensity string
	Duration  Duration
	// Seconds applies when Duration is DurationTime.
	Seconds float64
	// Meters applies when Duration is DurationDistance.
	Meters float64
	Target Target
	// TargetLow and TargetHigh bound the target range in Target's own units
	// (watts / bpm / m/s / rpm). Equal values mean a single target. Both 0
	// when Target is TargetOpen.
	TargetLow  float64
	TargetHigh float64
	// Repeat, when 2 or more, makes this a repeat block: Steps is what
	// repeats, that many times, and every field above except Name is
	// ignored.
	Repeat int
	Steps  []Step
}

// Options tunes the generated workout.
type Options struct {
	// Name shown in the device's workout list. Devices truncate this; keep
	// it short.
	Name string
	// Sport defaults to cycling.
	Sport typedef.Sport
	// CreatedAt stamps the file. Zero uses the current time.
	CreatedAt time.Time
}

// Encode renders a structured workout as a FIT workout file.
func Encode(steps []Step, opts Options) ([]byte, error) {
	if len(steps) == 0 {
		return nil, errors.New("fitworkout: need at least one step")
	}

	sport := opts.Sport
	if sport == 0 {
		sport = typedef.SportCycling
	}
	created := opts.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	name := opts.Name
	if name == "" {
		name = "Workout"
	}

	wkt := filedef.NewWorkout()
	wkt.FileId = *mesgdef.NewFileId(nil).
		SetType(typedef.FileWorkout).
		SetManufacturer(manufacturerDevelopment).
		SetProduct(0).
		SetTimeCreated(created).
		SetSerialNumber(0)

	fitSteps, err := flattenSteps(steps)
	if err != nil {
		return nil, err
	}

	numSteps, err := toUint16(len(fitSteps), "step count")
	if err != nil {
		return nil, err
	}
	wkt.Workout = mesgdef.NewWorkout(nil).
		SetWktName(name).
		SetSport(sport).
		SetNumValidSteps(numSteps)
	wkt.WorkoutSteps = fitSteps

	fitFile := wkt.ToFIT(nil)

	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fitFile); err != nil {
		return nil, fmt.Errorf("fitworkout: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// toUint16 and toUint32 convert a step count, step index or repeat count —
// all plain Go ints, and all ultimately traceable back to a rider-supplied
// request via internal/workout.FITSteps — into the FIT profile's own
// message_index/num_valid_steps (uint16) and duration_value/target_value
// (uint32) fields. Nothing resembling a real workout gets remotely close to
// either limit, but a request that did would otherwise silently wrap into a
// corrupt file instead of failing loudly, which is what these guard against.
func toUint16(n int, what string) (uint16, error) {
	if n < 0 || n > math.MaxUint16 {
		return 0, fmt.Errorf("fitworkout: %s (%d) is out of the FIT format's uint16 range", what, n)
	}
	return uint16(n), nil //#nosec G115 -- bounds-checked immediately above
}

func toUint32(n int, what string) (uint32, error) {
	if n < 0 || uint64(n) > math.MaxUint32 {
		return 0, fmt.Errorf("fitworkout: %s (%d) is out of the FIT format's uint32 range", what, n)
	}
	return uint32(n), nil //#nosec G115 -- bounds-checked immediately above
}

// flattenSteps turns the nested Step tree into FIT's flat
// step-list-plus-repeat-marker shape — see this package's own doc comment.
func flattenSteps(steps []Step) ([]*mesgdef.WorkoutStep, error) {
	var out []*mesgdef.WorkoutStep
	if err := appendSteps(&out, steps); err != nil {
		return nil, err
	}
	return out, nil
}

func appendSteps(out *[]*mesgdef.WorkoutStep, steps []Step) error {
	for _, s := range steps {
		if s.Repeat > 1 {
			if len(s.Steps) == 0 {
				return fmt.Errorf("fitworkout: repeat block %q has no steps", s.Name)
			}
			firstIndex := len(*out)
			if err := appendSteps(out, s.Steps); err != nil {
				return err
			}
			index, err := toUint16(len(*out), "step index")
			if err != nil {
				return err
			}
			durationValue, err := toUint32(firstIndex, "repeat block start index")
			if err != nil {
				return err
			}
			targetValue, err := toUint32(s.Repeat, "repeat count")
			if err != nil {
				return err
			}
			repeatStep := mesgdef.NewWorkoutStep(nil).
				SetMessageIndex(typedef.MessageIndex(index)).
				SetWktStepName(s.Name).
				SetDurationType(typedef.WktStepDurationRepeatUntilStepsCmplt).
				SetDurationValue(durationValue).
				SetTargetType(typedef.WktStepTargetOpen).
				SetTargetValue(targetValue).
				SetIntensity(typedef.IntensityActive)
			*out = append(*out, repeatStep)
			continue
		}

		step, err := encodeStep(s, len(*out))
		if err != nil {
			return err
		}
		*out = append(*out, step)
	}
	return nil
}

func encodeStep(s Step, index int) (*mesgdef.WorkoutStep, error) {
	idx, err := toUint16(index, "step index")
	if err != nil {
		return nil, err
	}
	step := mesgdef.NewWorkoutStep(nil).
		SetMessageIndex(typedef.MessageIndex(idx)).
		SetWktStepName(s.Name).
		SetIntensity(encodeIntensity(s.Intensity))

	if err := encodeDuration(step, s); err != nil {
		return nil, err
	}
	if err := encodeTarget(step, s); err != nil {
		return nil, err
	}
	return step, nil
}

func encodeIntensity(s string) typedef.Intensity {
	switch s {
	case "warmup":
		return typedef.IntensityWarmup
	case "cooldown":
		return typedef.IntensityCooldown
	case "recovery":
		return typedef.IntensityRecovery
	case "interval":
		return typedef.IntensityInterval
	case "rest":
		return typedef.IntensityRest
	case "other":
		return typedef.IntensityOther
	default:
		return typedef.IntensityActive
	}
}

// durationTimeScale and durationDistanceScale are the FIT profile's own
// scale factors for workout_step's duration_value subfields — confirmed
// against the SDK's generated field table (profile/factory), not the
// dynamic-field convenience comment on WorkoutStep.GetDurationValue, whose
// documented formula does not survive a round trip and looks to be a
// codegen artifact rather than the real encode direction. duration_time is
// scaled by 1000 into milliseconds, duration_distance by 100 into
// centimetres — matching the same "Scale: 1000" doubling this codebase
// already reads correctly via internal/fitcourse's own Scaled setters,
// applied by hand here because WorkoutStep's dynamic fields have no
// generated Scaled variant to do it for us.
const (
	durationTimeScale     = 1000.0
	durationDistanceScale = 100.0
)

func encodeDuration(step *mesgdef.WorkoutStep, s Step) error {
	switch s.Duration {
	case DurationTime:
		if s.Seconds <= 0 {
			return fmt.Errorf("fitworkout: step %q has a time duration but no seconds", s.Name)
		}
		step.SetDurationType(typedef.WktStepDurationTime)
		step.SetDurationValue(uint32(math.Round(s.Seconds * durationTimeScale)))
	case DurationDistance:
		if s.Meters <= 0 {
			return fmt.Errorf("fitworkout: step %q has a distance duration but no meters", s.Name)
		}
		step.SetDurationType(typedef.WktStepDurationDistance)
		step.SetDurationValue(uint32(math.Round(s.Meters * durationDistanceScale)))
	case DurationOpen, "":
		step.SetDurationType(typedef.WktStepDurationOpen)
		// DurationValue stays at its NewWorkoutStep(nil) invalid sentinel —
		// an open duration has no length to encode.
	default:
		return fmt.Errorf("fitworkout: step %q has an unknown duration type %q", s.Name, s.Duration)
	}
	return nil
}

// Absolute-value offsets and scales the FIT profile defines for a custom
// target range — see typedef.WorkoutPower/WorkoutHr's own doc comments for
// the offset convention (a value below the offset is a percentage of
// threshold instead; this package never writes that, only ever an absolute
// value at or above it) and profile/factory's field table for the plain
// scale factors speed and cadence use.
const (
	targetSpeedScale = 1000.0 // m/s -> raw, Scale: 1000
	powerOffset      = float64(typedef.WorkoutPowerWattsOffset)
	hrOffset         = float64(typedef.WorkoutHrBpmOffset)
)

func encodeTarget(step *mesgdef.WorkoutStep, s Step) error {
	switch s.Target {
	case TargetOpen, "":
		step.SetTargetType(typedef.WktStepTargetOpen)
		return nil
	case TargetPower:
		step.SetTargetType(typedef.WktStepTargetPower)
		return setCustomRange(step, s, powerOffset, 1)
	case TargetHeartRate:
		step.SetTargetType(typedef.WktStepTargetHeartRate)
		return setCustomRange(step, s, hrOffset, 1)
	case TargetPace:
		step.SetTargetType(typedef.WktStepTargetSpeed)
		return setCustomRange(step, s, 0, targetSpeedScale)
	case TargetCadence:
		step.SetTargetType(typedef.WktStepTargetCadence)
		return setCustomRange(step, s, 0, 1)
	default:
		return fmt.Errorf("fitworkout: step %q has an unknown target type %q", s.Name, s.Target)
	}
}

// setCustomRange writes an absolute low/high target via
// custom_target_value_low/high rather than a target_value zone number — a
// rider hand-building a workout states real numbers (watts, bpm, m/s, rpm),
// not a device-specific zone index. TargetValue itself is left at its
// invalid sentinel, which is how a device tells "custom range" apart from
// "zone number" for the same target_type.
func setCustomRange(step *mesgdef.WorkoutStep, s Step, offset, scale float64) error {
	if s.TargetLow <= 0 || s.TargetHigh <= 0 {
		return fmt.Errorf("fitworkout: step %q has target %q but no low/high value", s.Name, s.Target)
	}
	low, high := s.TargetLow, s.TargetHigh
	if low > high {
		low, high = high, low
	}
	step.SetCustomTargetValueLow(uint32(math.Round(low*scale + offset)))
	step.SetCustomTargetValueHigh(uint32(math.Round(high*scale + offset)))
	return nil
}

// SportFromString maps a plain sport string ("running" or anything else,
// defaulting to cycling) to the FIT library's own type — the same small
// helper internal/fitcourse.SportFromString provides, duplicated rather
// than imported so this package does not depend on that one just for six
// lines neither package's own domain requires depending on the other for.
func SportFromString(sport string) typedef.Sport {
	if sport == "running" {
		return typedef.SportRunning
	}
	return typedef.SportCycling
}
