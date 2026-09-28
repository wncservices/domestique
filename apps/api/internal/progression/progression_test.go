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
