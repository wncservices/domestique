package workout

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/wncservices/domestique/apps/api/internal/why"
)

// The markers below are the text conventions adaptation state used to live in.
// They are defined here, not in scheduler, so the parser that reads them back
// (LegacyFlags) sits next to the typed Flags that replace them; scheduler keeps
// aliases so no caller had to change.

// GeneratedDescription is what every workout the scheduler builds carries as
// its description, which is also how the rest of the app tells it from a
// rider's hand-built one.
const GeneratedDescription = "Generated from the periodization plan."

// AdjustedMarker prefixes the note appended when a generated workout has been
// changed automatically.
const AdjustedMarker = "Adjusted automatically:"

// SwappedMarker prefixes the note appended when the rider swapped a plan-made
// session for an alternate.
const SwappedMarker = "Swapped by you:"

// CrewKeptMarker is written into a session whose ease the rider unticked in a
// crew-ride preview.
const CrewKeptMarker = "Kept as planned by you around your crew ride."

// LifeKeptMarker is written into a session whose ease or shortening the rider
// unticked in a life-event preview.
const LifeKeptMarker = "Kept as planned by you through the return to training."

// LifeNotePrefix starts the note a life event appends after AdjustedMarker.
const LifeNotePrefix = "Life event:"

// Origin says who made a session.
type Origin string

const (
	OriginPlan  Origin = "plan"
	OriginRider Origin = "rider"
	OriginCrew  Origin = "crew"
	OriginTest  Origin = "test"
)

// Kept records which feature's ease the rider declined.
type Kept string

const (
	KeptNone Kept = ""
	KeptCrew Kept = "crew"
	KeptLife Kept = "life"
)

// MovedBy says who moved a session off its original day.
type MovedBy string

const (
	MovedNone    MovedBy = ""
	MovedByLife  MovedBy = "life"
	MovedByRider MovedBy = "rider"
	MovedByAuto  MovedBy = "auto"
)

// Flags is a session's adaptation state as typed values.
type Flags struct {
	Origin       Origin
	AdaptedRules []why.Rule // in order; nil = never adapted
	Swapped      bool
	Kept         Kept
	MovedFrom    string // first original date, "YYYY-MM-DD"; "" = never moved
	MovedBy      MovedBy
}

// IsLifeRule reports whether r is one of the rules a life event writes. The
// description text cannot tell them apart, so parity compares them as a class.
func IsLifeRule(r why.Rule) bool { return r == why.LifeEvent || r == why.IllnessRamp }

// Adapted reports whether any automatic adjustment has been made.
func (f Flags) Adapted() bool { return len(f.AdaptedRules) > 0 }

// WithAdaptation returns a copy with r appended. It always copies the slice so
// two derived values can never write over each other's backing array.
func (f Flags) WithAdaptation(r why.Rule) Flags {
	rules := make([]why.Rule, len(f.AdaptedRules), len(f.AdaptedRules)+1)
	copy(rules, f.AdaptedRules)
	f.AdaptedRules = append(rules, r)
	return f
}

// Equivalent is the parity relation between stored flags and what the legacy
// text says: everything must match except that adaptation rules are compared
// only by count and by being a life rule or not, which is the most the text
// can tell.
func (f Flags) Equivalent(g Flags) bool {
	if f.Origin != g.Origin || f.Swapped != g.Swapped || f.Kept != g.Kept ||
		f.MovedFrom != g.MovedFrom || f.MovedBy != g.MovedBy ||
		len(f.AdaptedRules) != len(g.AdaptedRules) {
		return false
	}
	for i := range f.AdaptedRules {
		if IsLifeRule(f.AdaptedRules[i]) != IsLifeRule(g.AdaptedRules[i]) {
			return false
		}
	}
	return true
}

// CurrentFlags is the flags a change builds on: the stored ones when the row has
// them (Origin is always set once the columns are filled), else what the text
// says. The stored columns carry exact rule ids the text cannot, so a later
// adaptation must extend them rather than the lossy text reading.
func CurrentFlags(w Workout) Flags {
	if w.Flags.Origin != "" {
		return w.Flags
	}
	return LegacyFlags(w)
}

var movedFromRE = regexp.MustCompile(`moved from (\d{4}-\d{2}-\d{2})`)

// MovedFromDate returns the first original date named in a description, if a
// move note is present.
func MovedFromDate(description string) (string, bool) {
	m := movedFromRE.FindStringSubmatch(description)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// LegacyFlags derives Flags from the text conventions, for rows written before
// the typed columns existed.
func LegacyFlags(w Workout) Flags {
	d := w.Description
	var f Flags
	switch {
	case w.CrewRideID != "":
		f.Origin = OriginCrew
	case w.GoalID != "" && strings.HasPrefix(d, GeneratedDescription):
		f.Origin = OriginPlan
	case w.TestProtocol != "":
		f.Origin = OriginTest
	default:
		f.Origin = OriginRider
	}

	rest := d
	for {
		i := strings.Index(rest, AdjustedMarker)
		if i < 0 {
			break
		}
		rest = rest[i+len(AdjustedMarker):]
		rule := why.Legacy
		if strings.HasPrefix(strings.TrimPrefix(rest, " "), LifeNotePrefix) {
			rule = why.LifeEvent
		}
		f.AdaptedRules = append(f.AdaptedRules, rule)
	}

	f.Swapped = strings.Contains(d, SwappedMarker)
	switch {
	case strings.Contains(d, CrewKeptMarker):
		f.Kept = KeptCrew
	case strings.Contains(d, LifeKeptMarker):
		f.Kept = KeptLife
	}

	if from, ok := MovedFromDate(d); ok {
		f.MovedFrom = from
		switch {
		case strings.Contains(d, "by a life event"):
			f.MovedBy = MovedByLife
		case strings.Contains(d, "Rescheduled by you"):
			f.MovedBy = MovedByRider
		default:
			f.MovedBy = MovedByAuto
		}
	}
	return f
}

// ErrFlagsDisagree is returned, while the parity check is on, by a write whose
// typed flags and description markers say different things.
var ErrFlagsDisagree = errors.New("workout: flags disagree with description markers")

var parityCheck atomic.Bool

// EnableLegacyParityCheck makes CreateWorkout and UpdateWorkout refuse a row
// whose flags are not Equivalent to what its description markers say. It is a
// process-wide switch for tests (and internal/testdb): it proves a migrated
// writer sets both in step, and costs nothing while off.
func EnableLegacyParityCheck() { parityCheck.Store(true) }

// checkParity reports ErrFlagsDisagree for a row as it will be stored.
func checkParity(w Workout) error {
	if !parityCheck.Load() {
		return nil
	}
	if legacy := LegacyFlags(w); !w.Flags.Equivalent(legacy) {
		return fmt.Errorf("%w: workout %q has %+v, description says %+v", ErrFlagsDisagree, w.ID, w.Flags, legacy)
	}
	return nil
}
