package api

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/progression"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
)

// TestRoundLevelDeltaNeverReturnsNegativeZero is round 2's minor fix
// (review finding #2): math.Round can hand back IEEE -0 for a small
// negative input that rounds to zero — a value that compares equal to 0 but
// would still print as "-0" in JSON or a log line. roundLevelDelta is what
// both applyProgressionForAnalysis and handleSetSessionFeel now go through
// before storing a level change, and it must always come back as ordinary,
// positive-signed 0 when the rounded result is zero.
func TestRoundLevelDeltaNeverReturnsNegativeZero(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"a small negative input that rounds to zero", -0.02, 0},
		{"an even smaller negative input", -1e-7, 0},
		{"exactly negative zero", math.Copysign(0, -1), 0},
		{"a positive input that rounds to zero", 0.02, 0},
		{"an ordinary negative tenth", -0.1, -0.1},
		{"an ordinary positive tenth", 0.3, 0.3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := roundLevelDelta(c.in)
			if got != c.want {
				t.Errorf("roundLevelDelta(%v) = %v, want %v", c.in, got, c.want)
			}
			// Only a *zero* result's sign bit matters here — an ordinary
			// negative, non-zero delta (like -0.1) is correctly negative,
			// not a -0 bug.
			if got == 0 && math.Signbit(got) {
				t.Errorf("roundLevelDelta(%v) = %v with a negative sign bit, want ordinary +0", c.in, got)
			}
		})
	}
}

// TestRideAnalysisOutcomesConvertToRecognisedProgressionOutcomes guards
// progression.Outcome and rideanalysis.Outcome staying in step by hand: the
// two packages deliberately don't import each other (progression.Outcome's
// own doc comment), so every rideanalysis.Outcome constant that means
// something to progression.Delta must have a matching progression.Outcome
// constant with the identical string value — this package is the one place
// that can see both and check it. OutcomeUnplanned is excluded: "no zone to
// move" is the one outcome progression.Delta's default branch is *supposed*
// to catch, not a case it should recognise.
func TestRideAnalysisOutcomesConvertToRecognisedProgressionOutcomes(t *testing.T) {
	// cur/workoutLevel is chosen per outcome so that Delta's own dedicated
	// branch for it produces a nonzero move — the point being to prove each
	// converted outcome actually reached that branch rather than falling
	// through to the default (unrecognised-outcome) branch, which always
	// returns 0. Struggled is the one deliberate exception: its own branch
	// returns 0 by design (the adapter steps the workout down instead), so
	// it is checked separately, by the delta at cur == workoutLevel matching
	// struggled's documented zero rather than by nonzero-ness.
	cases := []struct {
		outcome           rideanalysis.Outcome
		cur, workoutLevel float64
	}{
		{rideanalysis.OutcomeNailed, 5.0, 5.0},     // diff=0 >= -0.5: nonzero bump
		{rideanalysis.OutcomeCompleted, 5.0, 5.0},  // diff=0 >= 0: flat +0.1
		{rideanalysis.OutcomeIncomplete, 5.0, 4.0}, // diff=-1 <= 0: flat -0.3
	}
	for _, c := range cases {
		t.Run(string(c.outcome), func(t *testing.T) {
			converted := progression.Outcome(c.outcome)
			got := progression.Delta(c.cur, c.workoutLevel, converted, 0)
			if got == 0 {
				t.Errorf("progression.Delta(%v, %v, %q, 0) = 0, want a nonzero move — %q fell into Delta's default (unrecognised) branch", c.cur, c.workoutLevel, converted, converted)
			}
		})
	}

	t.Run(string(rideanalysis.OutcomeStruggled), func(t *testing.T) {
		converted := progression.Outcome(rideanalysis.OutcomeStruggled)
		if converted != progression.OutcomeStruggled {
			t.Fatalf("progression.Outcome(rideanalysis.OutcomeStruggled) = %q, want %q", converted, progression.OutcomeStruggled)
		}
		// Struggled's own branch and the default branch both return 0 for
		// this input, so 0 alone doesn't prove it took the dedicated branch
		// — but a diff that would make every *other* recognised branch
		// nonzero (workoutLevel above cur) still yields 0 here only because
		// struggled is handled explicitly; confirm via the sibling
		// progression package's own struggled test coverage instead.
		if got := progression.Delta(5.0, 6.0, converted, 0); got != 0 {
			t.Errorf("progression.Delta(5.0, 6.0, %q, 0) = %v, want 0 (struggled never moves the level itself)", converted, got)
		}
	})
}
