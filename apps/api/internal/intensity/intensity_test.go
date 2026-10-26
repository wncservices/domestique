package intensity

import (
	"math"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func power(zones ...int) workout.SessionAnalysis {
	return workout.SessionAnalysis{LoadSource: "fit_power", NormalizedPower: 200, PowerZoneSeconds: zones}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestBucketsFromPowerZoneSeconds(t *testing.T) {
	// 7200 low (1000+6200 split across zones 0-1), 3000 high, 600 anaerobic.
	m, ok := FromAnalyses([]workout.SessionAnalysis{power(1000, 6200, 1000, 2000, 300, 200, 100)})
	if !ok || m.Seconds != 10800 {
		t.Fatalf("ok=%v seconds=%d", ok, m.Seconds)
	}
	if !near(m.Low, 7200.0/10800) || !near(m.High, 3000.0/10800) || !near(m.Anaerobic, 600.0/10800) {
		t.Fatalf("mix = %+v", m)
	}
}

func TestOnlyPowerScoredRidesCount(t *testing.T) {
	good := power(10800, 0, 0, 0, 0, 0, 0)
	hr := power(0, 0, 0, 0, 0, 0, 9999)
	hr.LoadSource = "hr"
	est := power(0, 0, 0, 0, 0, 0, 9999)
	est.LoadSource = "estimate"
	zeroNP := power(0, 0, 0, 0, 0, 0, 9999)
	zeroNP.NormalizedPower = 0
	nilArr := workout.SessionAnalysis{LoadSource: "fit_power", NormalizedPower: 200}
	m, ok := FromAnalyses([]workout.SessionAnalysis{good, hr, est, zeroNP, nilArr})
	if !ok || m.Seconds != 10800 || !near(m.Low, 1) {
		t.Fatalf("mix = %+v ok=%v", m, ok)
	}
}

func TestUnderThreeHoursIsNoMix(t *testing.T) {
	if _, ok := FromAnalyses([]workout.SessionAnalysis{power(10799, 0, 0, 0, 0, 0, 0)}); ok {
		t.Fatal("want no mix under 3 h")
	}
	if _, ok := FromAnalyses(nil); ok {
		t.Fatal("want no mix for nothing")
	}
}

func TestTargetsPerPhase(t *testing.T) {
	cases := []struct {
		p       periodization.Phase
		l, h, a float64
	}{
		{periodization.PhaseBase, .80, .18, .02},
		{periodization.PhaseBuild, .75, .20, .05},
		{periodization.PhasePeak, .75, .15, .10},
	}
	for _, c := range cases {
		l, h, a, ok := Targets(c.p)
		if !ok || !near(l, c.l) || !near(h, c.h) || !near(a, c.a) {
			t.Errorf("%s: %v %v %v %v", c.p, l, h, a, ok)
		}
	}
	for _, p := range []periodization.Phase{periodization.PhaseTaper, "bogus"} {
		if _, _, _, ok := Targets(p); ok {
			t.Errorf("%s should have no targets", p)
		}
	}
}

func TestBuildWithHighOverAndAnaerobicShortPicksVO2First(t *testing.T) {
	mix := Mix{Low: .60, High: .38, Anaerobic: .02, Seconds: 20000}
	got := ChooseZones(periodization.PhaseBuild, mix, 3)
	want := []workout.Zone{workout.ZoneVO2Max, workout.ZoneThreshold, workout.ZoneSweetSpot}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNoRepeatUntilExhausted(t *testing.T) {
	mix := Mix{Low: .60, High: .38, Anaerobic: .02, Seconds: 20000}
	got := ChooseZones(periodization.PhasePeak, mix, 5)
	if len(got) != 5 {
		t.Fatalf("len %d", len(got))
	}
	seen := map[workout.Zone]bool{}
	for _, z := range got[:3] {
		if seen[z] {
			t.Fatalf("repeat before exhaustion: %v", got)
		}
		seen[z] = true
	}
	if got[3] != got[0] || got[4] != got[1] {
		t.Fatalf("should cycle: %v", got)
	}
}

func TestTiedGapsFallBack(t *testing.T) {
	// Gaps -0.5/0.5/0 pp: all within 1 pp of each other.
	mix := Mix{Low: .755, High: .195, Anaerobic: .05, Seconds: 20000}
	if got := ChooseZones(periodization.PhaseBuild, mix, 2); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

func TestTaperHasNoChoice(t *testing.T) {
	if got := ChooseZones(periodization.PhaseTaper, Mix{Low: 1, Seconds: 20000}, 2); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

// zonesFor trims a 3-zone choice to 2 when no third slot can be placed, which
// is only right if the shorter choice is a prefix of the longer.
func TestChooseZonesIsPrefixConsistent(t *testing.T) {
	mix := Mix{Low: .75, High: .05, Anaerobic: .20, Seconds: 40000}
	for _, phase := range []periodization.Phase{periodization.PhaseBase, periodization.PhaseBuild, periodization.PhasePeak} {
		three, two := ChooseZones(phase, mix, 3), ChooseZones(phase, mix, 2)
		if !reflect.DeepEqual(three[:2], two) {
			t.Errorf("%s: %v is not prefixed by %v", phase, three, two)
		}
	}
}

func TestPeakTieGoesToTheLargerGapFirst(t *testing.T) {
	// Peak: high gap 0.15-0.05 and anaerobic gap 0.10-0.0 are equal; allowed
	// order (anaerobic bucket first) breaks the tie, low is over target.
	mix := Mix{Low: .85, High: .15, Anaerobic: 0, Seconds: 20000}
	got := ChooseZones(periodization.PhasePeak, mix, 2)
	want := []workout.Zone{workout.ZoneVO2Max, workout.ZoneAnaerobic}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBaseHasOneBucketAndCyclesIt(t *testing.T) {
	mix := Mix{Low: .60, High: .38, Anaerobic: .02, Seconds: 20000}
	got := ChooseZones(periodization.PhaseBase, mix, 3)
	want := []workout.Zone{workout.ZoneSweetSpot, workout.ZoneTempo, workout.ZoneSweetSpot}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNoSlotsIsNil(t *testing.T) {
	mix := Mix{Low: .5, High: .5, Seconds: 20000}
	for _, k := range []int{0, -1} {
		if got := ChooseZones(periodization.PhaseBuild, mix, k); got != nil {
			t.Errorf("k=%d: want nil, got %v", k, got)
		}
	}
}

func TestAllNegativeGapsStillPickInOrder(t *testing.T) {
	// Every bucket over target is impossible for shares that sum to 1, but a
	// caller-built Mix may not; the largest (least negative) gap still leads.
	mix := Mix{Low: .9, High: .4, Anaerobic: .2, Seconds: 20000}
	got := ChooseZones(periodization.PhaseBuild, mix, 2)
	want := []workout.Zone{workout.ZoneVO2Max, workout.ZoneThreshold}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
