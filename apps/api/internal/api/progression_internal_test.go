package api

import (
	"math"
	"testing"
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
