// Package workoutexport writes a workout's steps as the trainer-app files a
// Zwift, TrainerRoad or Golden Cheetah rider can load: .zwo, .mrc and .erg.
//
// Pure: steps in, bytes out, stdlib only. It knows nothing of stores, HTTP or
// the indoor conversion; the caller converts first (indoor.Convert) so that
// distance and open-duration steps are already time-based and heart-rate steps
// already power. What is left that a trainer file cannot hold is refused here,
// never guessed.
package workoutexport

import (
	"errors"
	"fmt"
	"math"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Meta is the descriptive text a file carries.
type Meta struct {
	Name, Description string
	// FileName is the name written into formats that carry one (.mrc, .erg);
	// empty falls back to Name.
	FileName string
}

var (
	// ErrHRTarget is a step paced by heart rate or pace that the indoor
	// conversion could not turn into power (no FTP, or an unknown zone).
	ErrHRTarget = errors.New("workoutexport: a step is paced by heart rate or pace")
	// ErrNoFTP is returned by the formats that store targets relative to FTP
	// when the rider has none.
	ErrNoFTP = errors.New("workoutexport: this format needs an FTP")
	// ErrNotTimed is a step still measured by distance or a lap press.
	ErrNotTimed = errors.New("workoutexport: a step has no fixed duration")
	// ErrEmpty is a workout with nothing to write.
	ErrEmpty = errors.New("workoutexport: the workout has no steps")
)

// OpenStepError is returned by the formats that can only express a power
// line: an open step would be locked to a target by ERG mode, so a test would
// measure the target rather than the rider.
type OpenStepError struct{ Format string }

func (e *OpenStepError) Error() string {
	return "workoutexport: " + e.Format + " cannot hold a step with no power target"
}

// seg is one stretch of the timeline: whole seconds at a power, or a ramp
// from Start to End watts. Open means no target at all (a free ride).
type seg struct {
	Seconds    int
	Open       bool
	Start, End float64
	Name       string
}

func (s seg) steady() bool { return s.Start == s.End }

// item is one top-level element of the .zwo timeline: a segment, or a repeat
// block that fits IntervalsT.
type item struct {
	seg   seg
	block *block
}

// block is a repeat of an on segment then an off segment.
type block struct {
	Repeat  int
	On, Off seg
}

// segOf maps one non-repeat step to a segment. ok is false for a step that
// rounds to nothing (zero seconds), which is dropped.
func segOf(st workout.WorkoutStep) (s seg, ok bool, err error) {
	if st.Target == workout.TargetHeartRate || st.Target == workout.TargetPace {
		return seg{}, false, ErrHRTarget
	}
	if st.Duration != workout.DurationTime {
		return seg{}, false, ErrNotTimed
	}
	secs := int(math.Round(st.Seconds))
	if secs <= 0 {
		return seg{}, false, nil
	}
	s = seg{Seconds: secs, Name: st.Name}
	if st.Target != workout.TargetPower || (st.TargetLow <= 0 && st.TargetHigh <= 0) {
		// Open and cadence-only steps hold no wattage.
		s.Open = true
		return s, true, nil
	}
	lo, hi := st.TargetLow, st.TargetHigh
	if hi < lo {
		lo, hi = hi, lo
	}
	switch {
	case lo == hi:
		s.Start, s.End = lo, lo
	case st.Intensity == workout.IntensityWarmup:
		s.Start, s.End = lo, hi
	case st.Intensity == workout.IntensityCooldown:
		s.Start, s.End = hi, lo
	default:
		// A lock holds one number: a range on a working step is its midpoint.
		mid := (lo + hi) / 2
		s.Start, s.End = mid, mid
	}
	return s, true, nil
}

// isBlock reports whether a step is a repeat block rather than a plain step.
func isBlock(st workout.WorkoutStep) bool { return st.Repeat >= 2 && len(st.Steps) > 0 }

// unroll appends the fully expanded segments of steps, repeats repeated out.
func unroll(steps []workout.WorkoutStep, out *[]seg) error {
	for _, st := range steps {
		if isBlock(st) {
			for i := 0; i < st.Repeat; i++ {
				if err := unroll(st.Steps, out); err != nil {
					return err
				}
			}
			continue
		}
		s, ok, err := segOf(st)
		if err != nil {
			return err
		}
		if ok {
			*out = append(*out, s)
		}
	}
	return nil
}

// Unrolled is the whole workout as one flat run of segments, the shape the
// .mrc and .erg course formats need.
func unrolled(steps []workout.WorkoutStep) ([]seg, error) {
	var out []seg
	if err := unroll(steps, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

// asBlock returns st as an IntervalsT block when it is exactly two
// time-and-power children, on then off, each held at one power and neither a
// repeat. Anything else is unrolled by the caller: correct on every app,
// merely longer.
func asBlock(st workout.WorkoutStep) (block, bool) {
	if !isBlock(st) || len(st.Steps) != 2 {
		return block{}, false
	}
	var kids [2]seg
	for i, k := range st.Steps {
		if isBlock(k) {
			return block{}, false
		}
		s, ok, err := segOf(k)
		if err != nil || !ok || s.Open || !s.steady() {
			return block{}, false
		}
		kids[i] = s
	}
	return block{Repeat: st.Repeat, On: kids[0], Off: kids[1]}, true
}

// timeline is the .zwo view: top-level steps, with eligible repeats kept as
// blocks and every other repeat unrolled in place.
func timeline(steps []workout.WorkoutStep) ([]item, error) {
	var out []item
	for _, st := range steps {
		if isBlock(st) {
			if b, ok := asBlock(st); ok {
				out = append(out, item{block: &b})
				continue
			}
			var flat []seg
			if err := unroll([]workout.WorkoutStep{st}, &flat); err != nil {
				return nil, err
			}
			for _, s := range flat {
				out = append(out, item{seg: s})
			}
			continue
		}
		s, ok, err := segOf(st)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, item{seg: s})
		}
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

// minutes renders cumulative seconds as minutes with two decimals. Computed
// from the running total, never summed from rounded steps, so a long workout
// of short steps does not drift.
func minutes(cumSeconds int) string { return fmt.Sprintf("%.2f", float64(cumSeconds)/60) }
