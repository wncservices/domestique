package workoutlib

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

func ladder(t *testing.T, zone string) Ladder {
	t.Helper()
	l, ok := LadderFor(model.SportCycling, zone)
	if !ok {
		t.Fatalf("no cycling ladder for %s", zone)
	}
	return l
}

const roomy = 100 * 3600 // a budget no rung exceeds

func TestPickNearWithNoWantedLengthIsPick(t *testing.T) {
	for _, zone := range []string{"sweet_spot", "threshold", "vo2max", "anaerobic"} {
		l := ladder(t, zone)
		for target := 0.5; target <= 10.5; target += 0.5 {
			for _, budget := range []float64{roomy, 2.5 * 3600, 1.5 * 3600} {
				want, wok := Pick(l, target, budget)
				got, gok := PickNear(l, target, budget, 0)
				if got != want || gok != wok {
					t.Errorf("%s target %.1f budget %.0f: PickNear(0) = %+v/%v, Pick = %+v/%v", zone, target, budget, got, gok, want, wok)
				}
			}
		}
	}
}

// Threshold ladder, level -> work: 3 x 8 min is level 3, 4 x 8 level 4,
// 3 x 12 level 5, 2 x 20 level 6. Target 4.5 sits between 4 and 5 and Pick
// takes the lower, so Pick is level 4 and the neighbours are levels 3 and 5.
func TestPickNearChoosesTheWorkLengthClosestToTheWantedOne(t *testing.T) {
	l := ladder(t, "threshold")
	base, _ := Pick(l, 4.5, roomy)
	if base.Level != 4 {
		t.Fatalf("fixture assumption: Pick(4.5) = level %d, want 4", base.Level)
	}
	cases := []struct {
		wantMin   int
		wantLevel int
	}{
		{12, 5}, // 3 x 12 is one rung above Pick and exactly right
		{20, 5}, // 2 x 20 is level 6, two above: out of reach, so the nearest reachable (12 min)
		{8, 4},  // Pick's own is already exact
		{6, 4},  // 3 x 8 and 4 x 8 are equally near; the tie goes to Pick's own
	}
	for _, c := range cases {
		got, ok := PickNear(l, 4.5, roomy, c.wantMin*60)
		if !ok || got.Level != c.wantLevel {
			t.Errorf("want %d min: level %d (ok=%v), want level %d (%+v)", c.wantMin, got.Level, ok, c.wantLevel, got)
		}
	}
}

// One rung below Pick is allowed too: target 5.5 picks level 5 (3 x 12), and
// a rider whose climbs are 8 minutes long is better served by level 4's 4 x 8.
func TestPickNearMayGoOneRungBelowPick(t *testing.T) {
	l := ladder(t, "threshold")
	base, _ := Pick(l, 5.5, roomy)
	if base.Level != 5 {
		t.Fatalf("fixture assumption: Pick(5.5) = level %d, want 5", base.Level)
	}
	got, _ := PickNear(l, 5.5, roomy, 8*60)
	if got.Level != 4 {
		t.Errorf("want 8 min: level %d, want 4 (4 x 8)", got.Level)
	}
}

func TestPickNearNeverGoesMoreThanOneRungAbovePickNorBelowOne(t *testing.T) {
	for _, zone := range []string{"sweet_spot", "threshold", "vo2max", "anaerobic"} {
		l := ladder(t, zone)
		for target := 0.5; target <= 10.5; target += 0.25 {
			base, ok := Pick(l, target, roomy)
			if !ok {
				continue
			}
			for want := 15; want <= 4000; want += 15 {
				got, ok := PickNear(l, target, roomy, want)
				if !ok {
					t.Fatalf("%s: PickNear found nothing where Pick found level %d", zone, base.Level)
				}
				if got.Level > base.Level+1 || got.Level < base.Level-1 {
					t.Fatalf("%s target %.2f want %ds: level %d is more than one from Pick's %d", zone, target, want, got.Level, base.Level)
				}
			}
		}
	}
}

func TestPickNearRespectsTheTimeCap(t *testing.T) {
	l := ladder(t, "threshold")
	// A cap that fits 3 x 8 (level 3: 20 + 3*(8+4) = 56 min) and 4 x 8
	// (level 4: 20 + 4*12 = 68 min) but not 3 x 12 (level 5: 20 + 3*17 = 71 min).
	capSec := float64(70 * 60)
	base, _ := Pick(l, 4.5, capSec)
	got, ok := PickNear(l, 4.5, capSec, 12*60)
	if !ok || TotalSeconds(got) > capSec {
		t.Fatalf("PickNear = %+v (ok=%v) exceeds the %0.f s cap", got, ok, capSec)
	}
	if got.Level == 5 {
		t.Errorf("picked the 3 x 12 rung, which does not fit the cap")
	}
	if base.Level != 4 {
		t.Errorf("fixture: Pick under this cap = level %d, want 4", base.Level)
	}
	// Nothing fits at all.
	if _, ok := PickNear(l, 4.5, 60, 12*60); ok {
		t.Error("a 60 s cap fits nothing; PickNear must say so")
	}
}

// Ties go to Pick's own rung, so a wanted length exactly between two rungs does
// not nudge the session for no reason.
func TestPickNearTiesGoToPick(t *testing.T) {
	l := ladder(t, "threshold")
	base, _ := Pick(l, 4.5, roomy) // level 4: 4 x 8 min = 480 s
	// 600 s is 120 s from 4 x 8 (480) and 120 s from 3 x 12 (720).
	got, _ := PickNear(l, 4.5, roomy, 600)
	if got != base {
		t.Errorf("a tie chose level %d, want Pick's level %d", got.Level, base.Level)
	}
}
