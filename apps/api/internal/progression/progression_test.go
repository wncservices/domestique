package progression

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestInitial(t *testing.T) {
	cases := []struct {
		name       string
		experience string
		sport      model.Sport
		wantZones  []string
		wantValue  float64
	}{
		{"beginner cycling", "beginner", model.SportCycling,
			[]string{"tempo", "sweet_spot", "threshold", "vo2max", "anaerobic"}, 2.0},
		{"intermediate cycling", "intermediate", model.SportCycling,
			[]string{"tempo", "sweet_spot", "threshold", "vo2max", "anaerobic"}, 4.0},
		{"advanced cycling", "advanced", model.SportCycling,
			[]string{"tempo", "sweet_spot", "threshold", "vo2max", "anaerobic"}, 6.0},
		{"unset cycling", "", model.SportCycling,
			[]string{"tempo", "sweet_spot", "threshold", "vo2max", "anaerobic"}, 3.0},
		{"unrecognised experience", "pro", model.SportCycling,
			[]string{"tempo", "sweet_spot", "threshold", "vo2max", "anaerobic"}, 3.0},
		{"case and whitespace insensitive", "  BEGINNER  ", model.SportCycling,
			[]string{"tempo", "sweet_spot", "threshold", "vo2max", "anaerobic"}, 2.0},
		{"beginner running", "beginner", model.SportRunning,
			[]string{"tempo", "threshold", "intervals"}, 2.0},
		{"intermediate running", "intermediate", model.SportRunning,
			[]string{"tempo", "threshold", "intervals"}, 4.0},
		{"advanced running", "advanced", model.SportRunning,
			[]string{"tempo", "threshold", "intervals"}, 6.0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			levels := Initial(c.experience, c.sport)
			if len(levels) != len(c.wantZones) {
				t.Fatalf("got %d levels, want %d: %+v", len(levels), len(c.wantZones), levels)
			}
			gotZones := make(map[string]float64, len(levels))
			for _, l := range levels {
				if l.Sport != string(c.sport) {
					t.Errorf("level %+v: sport = %q, want %q", l, l.Sport, c.sport)
				}
				gotZones[l.Zone] = l.Value
			}
			for _, z := range c.wantZones {
				v, ok := gotZones[z]
				if !ok {
					t.Errorf("missing zone %q in %+v", z, levels)
					continue
				}
				if v != c.wantValue {
					t.Errorf("zone %q value = %v, want %v", z, v, c.wantValue)
				}
			}
		})
	}
}

func TestDelta(t *testing.T) {
	cases := []struct {
		name       string
		cur        float64
		workoutLvl float64
		outcome    Outcome
		feel       int
		want       float64
	}{
		// nailed, diff >= -0.5: new = max(cur, wl) + bump (bump=0.3, feel unrated)
		{"nailed at level, unrated feel", 5.0, 5.0, "nailed", 0, 0.3},
		{"nailed above level within tolerance", 5.0, 5.3, "nailed", 0, 0.6}, // max(5,5.3)+0.3=5.6, delta=0.6
		{"nailed below level within tolerance", 5.0, 4.6, "nailed", 0, 0.3}, // diff=-0.4>=-0.5, max(5,4.6)+0.3=5.3
		{"nailed exactly at -0.5 boundary", 5.0, 4.5, "nailed", 0, 0.3},     // diff=-0.5, still >=-0.5

		// nailed, diff < -0.5: flat +0.1
		{"nailed well below level", 5.0, 4.0, "nailed", 0, 0.1},

		// completed
		{"completed at or above level", 5.0, 5.0, "completed", 0, 0.1},
		{"completed above level", 5.0, 6.0, "completed", 0, 0.1},
		{"completed below level", 5.0, 4.0, "completed", 0, 0},

		// struggled: never moves the level itself
		{"struggled above level", 5.0, 6.0, "struggled", 0, 0},
		{"struggled below level", 5.0, 4.0, "struggled", 0, 0},

		// incomplete
		{"incomplete at or below level", 5.0, 5.0, "incomplete", 0, -0.3},
		{"incomplete below level", 5.0, 4.0, "incomplete", 0, -0.3},
		{"incomplete above level", 5.0, 6.0, "incomplete", 0, 0},

		// unplanned / no zone: no change regardless of feel
		{"unplanned", 5.0, 5.0, "unplanned", 3, 0},
		{"empty outcome", 5.0, 5.0, "", 3, 0},

		// feel adjustments on a nailed bump
		{"nailed feel 1 easy", 5.0, 5.0, "nailed", 1, 0.5},    // bump=0.3+0.2=0.5
		{"nailed feel 2 easy", 5.0, 5.0, "nailed", 2, 0.5},    // bump=0.5
		{"nailed feel 3 neutral", 5.0, 5.0, "nailed", 3, 0.3}, // bump=0.3+0=0.3
		{"nailed feel 4 hard", 5.0, 5.0, "nailed", 4, 0.2},    // bump=0.3-0.1=0.2
		{"nailed feel 5 all-out", 5.0, 5.0, "nailed", 5, 0.1}, // bump=0.3-0.2=0.1

		// bump floor: even a harsh feel never drops the bump below 0.1
		// (0.3 - 0.2 = 0.1 is already the floor; verify it doesn't go lower
		// by combining with a level well above current, which would only
		// increase the bump — floor is exercised by feel 5 case above and
		// re-checked explicitly here with an even lower theoretical bump
		// were it not floored).
		{"nailed feel 5 stays at floor not below", 5.0, 5.0, "nailed", 5, 0.1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Delta(c.cur, c.workoutLvl, c.outcome, c.feel)
			if !almostEqual(got, c.want) {
				t.Errorf("Delta(%v, %v, %q, %d) = %v, want %v", c.cur, c.workoutLvl, c.outcome, c.feel, got, c.want)
			}
		})
	}
}

func TestApply(t *testing.T) {
	cases := []struct {
		name  string
		cur   float64
		delta float64
		want  float64
	}{
		{"ordinary bump", 4.6, 0.7, 5.3},
		{"clamps at floor", 1.2, -1.0, 1.0},
		{"clamps at ceiling", 9.8, 0.5, 10.0},
		{"rounds to one decimal", 5.0, 0.05, 5.1},
		{"rounds down", 5.0, 0.04, 5.0},
		{"never below 1 even from a very negative delta", 2.0, -50, 1.0},
		{"never above 10 even from a very positive delta", 8.0, 50, 10.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Apply(c.cur, c.delta)
			if !almostEqual(got, c.want) {
				t.Errorf("Apply(%v, %v) = %v, want %v", c.cur, c.delta, got, c.want)
			}
		})
	}
}

func TestReason(t *testing.T) {
	got := Reason("Threshold 3×12", "threshold", 5.0, 4.6, 5.3, "nailed")
	want := "Nailed Threshold 3×12 (5.0) — threshold 4.6 → 5.3"
	if got != want {
		t.Errorf("Reason = %q, want %q", got, want)
	}

	for _, outcome := range []Outcome{OutcomeCompleted, OutcomeStruggled, OutcomeIncomplete} {
		t.Run(string(outcome), func(t *testing.T) {
			r := Reason("Sweet spot 3×10", "sweet_spot", 4.0, 4.0, 4.1, outcome)
			wantVerb := outcomeVerbs[outcome]
			if len(r) < len(wantVerb) || r[:len(wantVerb)] != wantVerb {
				t.Errorf("Reason %q does not start with capitalised verb %q", r, wantVerb)
			}
		})
	}
}

func TestRecalibrationDelta(t *testing.T) {
	near := func(got, want float64) bool { return math.Abs(got-want) < 0.0005 }

	t.Run("the worked example, 255 to 268 W", func(t *testing.T) {
		if got := RecalibrationDelta(255, 268); !near(got, -0.717) {
			t.Errorf("RecalibrationDelta(255, 268) = %v, want about -0.717 (the spec rounds to -0.718)", got)
		}
		if got := Apply(5.3, RecalibrationDelta(255, 268)); got != 4.6 {
			t.Errorf("Apply(5.3, delta) = %v, want 4.6", got)
		}
	})

	t.Run("unchanged FTP is no move", func(t *testing.T) {
		if got := RecalibrationDelta(255, 255); got != 0 {
			t.Errorf("RecalibrationDelta(255, 255) = %v, want 0", got)
		}
	})

	t.Run("a +10% rise is under the cap", func(t *testing.T) {
		if got := RecalibrationDelta(255, 280.5); !near(got, -1.375) {
			t.Errorf("RecalibrationDelta(255, 280.5) = %v, want about -1.375", got)
		}
	})

	t.Run("a +20% rise is capped at -2.0 (raw -2.63)", func(t *testing.T) {
		if got := RecalibrationDelta(255, 306); got != -2.0 {
			t.Errorf("RecalibrationDelta(255, 306) = %v, want exactly -2.0", got)
		}
		if got := RecalibrationDelta(255, 318.75); got != -2.0 {
			t.Errorf("RecalibrationDelta(255, 318.75) = %v, want exactly -2.0", got)
		}
	})

	t.Run("a decrease is unconditional and positive; gating is the caller's job", func(t *testing.T) {
		if got := RecalibrationDelta(255, 240); got <= 0 {
			t.Errorf("RecalibrationDelta(255, 240) = %v, want a positive delta (caller must never apply it)", got)
		}
	})

	t.Run("Apply still floors at 1.0 and never goes negative", func(t *testing.T) {
		if got := Apply(1.4, RecalibrationDelta(255, 306)); got != 1.0 {
			t.Errorf("Apply(1.4, -2.0) = %v, want 1.0", got)
		}
		if got := Apply(1.0, RecalibrationDelta(255, 2550)); got != 1.0 {
			t.Errorf("Apply(1.0, huge jump) = %v, want 1.0", got)
		}
	})
}

func TestRecalibrationReason(t *testing.T) {
	got := RecalibrationReason(255, 268, "threshold", 5.3, 4.6)
	want := "FTP 255 → 268 W — threshold 5.3 → 4.6"
	if got != want {
		t.Errorf("RecalibrationReason = %q, want %q", got, want)
	}
}

func TestRecalibrationConstants(t *testing.T) {
	if RecalibrationUpFactor != 1.03 || RecalibrationMaxFactor != 1.25 ||
		RecalibrationLevelsPerDoubling != 10 || RecalibrationMaxDrop != 2.0 {
		t.Error("recalibration constants drifted from the spec")
	}
}
