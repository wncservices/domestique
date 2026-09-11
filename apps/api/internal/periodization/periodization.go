// Package periodization turns a goal and a rider's own profile into a
// periodized training structure: phase boundaries and a weekly volume
// target for every week between now and the goal.
//
// This is Phase C of docs/training-plan.md, and it follows that document's
// own recommendation: periodization is a solved, specified algorithm —
// TrainingPeaks' own Annual Training Plan shape (base, build, peak, taper)
// and the classic 3:1 build:recover microcycle inside base/build — so this
// is a pure, deterministic function rather than a model call. Give it a
// goal, a profile and today's date; get back the same plan every time,
// exactly the property this codebase's own testing culture rewards (see
// internal/sync.BuildPlan for the precedent this follows).
//
// Deliberately scoped short of producing actual workouts. This package
// answers "how many hours should week 6 ask for, and what phase is it in,"
// not "what should Tuesday's session actually contain" — turning a week's
// target into concrete sessions on concrete days needs a per-sport,
// per-phase workout-template library, real design work of its own kept out
// of this pass (see docs/training-plan.md's phase table: this is
// periodization.BuildPlan, not yet scheduler.NextWorkouts) so the part with an
// actual specified algorithm behind it can be reviewed and tested on its
// own, without that separate design question riding along with it.
//
// This models weekly *volume* only (hours), not intensity distribution —
// a real limitation worth stating plainly rather than implying more than
// this does: two riders with the same weekly hours target can be training
// very differently depending on how that time splits across zones. Peak
// phase in particular is, in real periodization, about race-pace
// intensity more than volume; this package can only turn a dial it has —
// hours — so Peak's own dial setting is a volume proxy for what a real
// plan would mostly express through intensity instead.
package periodization

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Phase is one block of a periodized plan.
type Phase string

const (
	// PhaseBase builds aerobic volume — the largest share of a plan with
	// enough weeks to have one at all.
	PhaseBase Phase = "base"
	// PhaseBuild raises intensity on top of Base's volume: this is where
	// the 3:1 microcycle matters most, since build load without a
	// recovery week is how overreaching happens.
	PhaseBuild Phase = "build"
	// PhasePeak sharpens toward race readiness — short, and (see this
	// package's own doc comment) mostly a volume proxy for what should
	// really be an intensity change.
	PhasePeak Phase = "peak"
	// PhaseTaper reduces load progressively into the goal event.
	PhaseTaper Phase = "taper"
)

// Week is one week of a periodized plan.
type Week struct {
	// Number is 1-based, counting from the plan's first week.
	Number int
	// StartDate is that week's Monday, "YYYY-MM-DD".
	StartDate string
	Phase     Phase
	// Recovery marks a deliberately reduced-load week — the "1" in a
	// classic 3:1 build:recover microcycle, within Base or Build only;
	// Peak and Taper are already reduced-load phases and carry no
	// recovery weeks of their own.
	Recovery bool
	// TargetHours is this week's training volume target, derived from the
	// rider's own profile (RiderProfile.HoursPerAvailableDay ×
	// len(AvailableDays)) — 0 when the profile states neither, in which
	// case every week's TargetHours is 0 and a caller should treat this
	// plan as phase/timing information only, prompting the rider to fill
	// in their profile before it means anything as a volume target.
	TargetHours float64
}

// Plan is a full periodized structure for one goal.
type Plan struct {
	GoalID string
	Weeks  []Week
}

// ErrNoEventDate is returned when the goal has no event date to plan
// toward — there is nothing to periodize against.
var ErrNoEventDate = errors.New("periodization: goal has no event date")

// ErrEventInThePast is returned when the goal's event date is not after
// today.
var ErrEventInThePast = errors.New("periodization: goal's event date is not in the future")

// Phase-length proportions of the plan's total weeks, applied once the plan
// is long enough for all four phases to exist (see allocateWeeks for the
// short-plan fallbacks below that). These are TrainingPeaks' own published
// Annual Training Plan proportions, not a universal law — a coach may
// reasonably choose different splits for a given rider; this is a
// reasonable default, not the only correct one.
const (
	baseFraction  = 0.40
	buildFraction = 0.35
	peakFraction  = 0.15
	taperFraction = 0.10
)

// Every 4th week within Base or Build is a recovery week — the classic 3:1
// microcycle: three weeks of rising load, one week deliberately reduced.
const recoveryEveryNWeeks = 4

// recoveryLoadFraction is how far a recovery week drops below the load the
// non-recovery week immediately before it would otherwise have reached.
const recoveryLoadFraction = 0.6

// Each phase's own ceiling, as a fraction of the rider's peak weekly hours
// (profile.HoursPerAvailableDay × len(AvailableDays)). Base builds toward
// this gradually rather than starting at it — see weekFraction — so a
// plan's very first week is not the same volume as its last Base week.
const (
	baseCeiling  = 0.70
	buildCeiling = 1.00
	peakCeiling  = 0.80
	// taperStartFraction and taperEndFraction bound a linear ramp down
	// across however many weeks Taper actually has — see taperFraction.
	taperStartFraction = 0.50
	taperEndFraction   = 0.25
)

// BuildPlan builds a periodized structure from today until goal's event
// date, sized to the rider's own weekly availability.
func BuildPlan(goal workout.Goal, profile workout.RiderProfile, today time.Time) (Plan, error) {
	if goal.EventDate == "" {
		return Plan{}, ErrNoEventDate
	}
	event, err := time.Parse("2006-01-02", goal.EventDate)
	if err != nil {
		return Plan{}, fmt.Errorf("periodization: parse event date %q: %w", goal.EventDate, err)
	}

	start := mondayOf(today)
	if !event.After(start) {
		return Plan{}, ErrEventInThePast
	}

	// The week containing the event itself must be included even when the
	// gap is an exact multiple of 7 days — event.Sub(start) is then exactly
	// N weeks, and a plain ceil of that would stop one week short, landing
	// the event on the Monday immediately after the last planned week
	// rather than inside it. Integer division (floor, since both operands
	// are non-negative here) plus one covers every case uniformly.
	daysUntilEvent := int(event.Sub(start).Hours() / 24)
	totalWeeks := daysUntilEvent/7 + 1

	peakHours := profile.HoursPerAvailableDay * float64(len(profile.AvailableDays))

	lengths := allocateWeeks(totalWeeks)
	order := []Phase{PhaseBase, PhaseBuild, PhasePeak, PhaseTaper}

	plan := Plan{GoalID: goal.ID, Weeks: make([]Week, 0, totalWeeks)}
	weekNumber := 0
	for _, phase := range order {
		n := lengths[phase]
		for i := 0; i < n; i++ {
			weekNumber++
			recovery := (phase == PhaseBase || phase == PhaseBuild) &&
				(i+1)%recoveryEveryNWeeks == 0
			plan.Weeks = append(plan.Weeks, Week{
				Number:      weekNumber,
				StartDate:   start.AddDate(0, 0, (weekNumber-1)*7).Format("2006-01-02"),
				Phase:       phase,
				Recovery:    recovery,
				TargetHours: peakHours * weekFraction(phase, i, n, recovery),
			})
		}
	}
	return plan, nil
}

// allocateWeeks splits totalWeeks across the four phases. Short plans
// cannot fit all four meaningfully — there is no point calling a single
// week "Base" — so this collapses toward the phases nearest the event
// first, the same priority a rider running out of time would choose
// themselves: protect the taper, then peak, before base gets to exist at
// all.
func allocateWeeks(total int) map[Phase]int {
	switch {
	case total <= 1:
		return map[Phase]int{PhaseTaper: total}
	case total <= 3:
		taper := 1
		return map[Phase]int{PhaseBuild: total - taper, PhaseTaper: taper}
	case total <= 6:
		taper, peak := 1, 1
		return map[Phase]int{PhaseBuild: total - taper - peak, PhasePeak: peak, PhaseTaper: taper}
	default:
		taper := roundAtLeastOne(total, taperFraction)
		peak := roundAtLeastOne(total, peakFraction)
		build := roundAtLeastOne(total, buildFraction)
		base := total - taper - peak - build
		if base < 0 {
			// However the rounding above landed, taper and peak are the
			// phases closest to the event and matter most to keep intact;
			// build absorbs the shortfall since it is the next most
			// elastic, and base — the most elastic of all — is what
			// actually shrinks to 0 here if there still isn't room.
			build += base // base is negative, so this reduces build
			base = 0
			if build < 1 {
				build = 1
			}
		}
		return map[Phase]int{PhaseBase: base, PhaseBuild: build, PhasePeak: peak, PhaseTaper: taper}
	}
}

func roundAtLeastOne(total int, fraction float64) int {
	n := int(math.Round(float64(total) * fraction))
	if n < 1 {
		n = 1
	}
	return n
}

// weekFraction is a phase-local week's target as a fraction of the rider's
// peak weekly hours.
func weekFraction(phase Phase, indexInPhase, phaseLength int, recovery bool) float64 {
	switch phase {
	case PhasePeak:
		// Held level — see this package's own doc comment on why Peak is
		// only a volume proxy: a real plan would vary intensity here, not
		// volume, and this model has no intensity dial to turn.
		return peakCeiling

	case PhaseTaper:
		if phaseLength <= 1 {
			return taperEndFraction
		}
		// Linear ramp down across however many taper weeks there are, so
		// the final week before the event is always the lightest.
		t := float64(indexInPhase) / float64(phaseLength-1)
		return taperStartFraction - t*(taperStartFraction-taperEndFraction)

	default: // PhaseBase, PhaseBuild
		ceiling := buildCeiling
		if phase == PhaseBase {
			ceiling = baseCeiling
		}
		if recovery {
			return ceiling * recoveryLoadFraction
		}
		// Within each block of up to recoveryEveryNWeeks-1 rising weeks
		// between recoveries, ramp from 80% up to 100% of this phase's own
		// ceiling — progressive overload, not a flat plateau. The block's
		// last non-recovery week (posInBlock == blockLen-1) must land
		// exactly at the ceiling, so the ramp divides by blockLen-1 steps,
		// not blockLen — dividing by the block's own length would top out
		// at (blockLen-1)/blockLen and never actually reach the ceiling
		// before recovery cuts in.
		posInBlock := indexInPhase % recoveryEveryNWeeks
		blockLen := recoveryEveryNWeeks - 1
		steps := blockLen - 1
		if steps < 1 {
			steps = 1
		}
		t := float64(posInBlock) / float64(steps)
		if t > 1 {
			t = 1
		}
		return ceiling * (0.8 + 0.2*t)
	}
}

// mondayOf returns the Monday of the week containing t, at midnight —
// periodization plans align to calendar weeks, the way a rider actually
// reads a training calendar, rather than starting mid-week from whatever
// day today happens to be.
func mondayOf(t time.Time) time.Time {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	daysSinceMonday := (int(t.Weekday()) + 6) % 7
	return t.AddDate(0, 0, -daysSinceMonday)
}
