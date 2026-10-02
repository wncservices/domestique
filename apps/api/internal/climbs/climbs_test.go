package climbs

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

type seg struct{ LengthM, GradePercent float64 }

// profile builds a synthetic track heading due north at spacingM intervals,
// its elevation following the segments back to back. Nothing here is a real
// place: the latitude and longitude are arbitrary.
func profile(spacingM, baseEle float64, segs ...seg) []gpx.Point {
	latStep := spacingM / 111_320.0
	pts := []gpx.Point{{Lat: 50.0, Lon: 3.0, Ele: baseEle, HasEle: true}}
	lat, ele := 50.0, baseEle
	for _, s := range segs {
		rise := spacingM * s.GradePercent / 100
		for d := 0.0; d < s.LengthM; d += spacingM {
			lat += latStep
			ele += rise
			pts = append(pts, gpx.Point{Lat: lat, Lon: 3.0, Ele: ele, HasEle: true})
		}
	}
	return pts
}

func TestSteadyRampIsOneClimb(t *testing.T) {
	pts := profile(25, 100, seg{300, 0}, seg{3000, 6}, seg{300, 0})
	got := Detect(pts, TrainingConfig)
	if len(got) != 1 {
		t.Fatalf("got %d climbs, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Index != 0 {
		t.Errorf("index = %d, want 0", c.Index)
	}
	if math.Abs(c.LengthM-3000) > 200 || math.Abs(c.AvgGradient-6) > 0.5 {
		t.Errorf("length %.0f gradient %.1f, want ~3000 m at ~6%%", c.LengthM, c.AvgGradient)
	}
	if math.Abs(c.EndM-c.StartM-c.LengthM) > 1e-6 {
		t.Errorf("EndM-StartM = %.1f, LengthM = %.1f; they must agree", c.EndM-c.StartM, c.LengthM)
	}
	if c.StartIdx >= c.EndIdx {
		t.Errorf("start %d must precede end %d", c.StartIdx, c.EndIdx)
	}
	if c.Score < 16_000 || c.Score >= 32_000 {
		t.Errorf("score = %.0f, want category 3 band", c.Score)
	}
}

func TestFalseFlatInsideOneClimbIsMerged(t *testing.T) {
	// 150 m dip: inside the 200 m merge gap.
	pts := profile(25, 100, seg{300, 0}, seg{1500, 6}, seg{150, -3}, seg{1500, 6}, seg{300, 0})
	got := Detect(pts, TrainingConfig)
	if len(got) != 1 {
		t.Fatalf("got %d climbs, want 1 (merged): %+v", len(got), got)
	}
	if math.Abs(got[0].LengthM-3150) > 250 {
		t.Errorf("length = %.0f, want ~3150", got[0].LengthM)
	}
}

func TestGapOverTheMergeDistanceSplitsIt(t *testing.T) {
	pts := profile(25, 100, seg{300, 0}, seg{1500, 6}, seg{300, -3}, seg{1500, 6}, seg{300, 0})
	got := Detect(pts, TrainingConfig)
	if len(got) != 2 {
		t.Fatalf("got %d climbs, want 2 (split): %+v", len(got), got)
	}
	if got[0].Index != 0 || got[1].Index != 1 {
		t.Errorf("indexes = %d, %d, want 0, 1", got[0].Index, got[1].Index)
	}
	if got[0].EndIdx >= got[1].StartIdx {
		t.Error("climbs overlap")
	}
}

func TestTrailingDescentIsNotCounted(t *testing.T) {
	pts := profile(25, 100, seg{300, 0}, seg{2000, 6}, seg{1000, -6})
	got := Detect(pts, TrainingConfig)
	if len(got) != 1 {
		t.Fatalf("got %d climbs, want 1: %+v", len(got), got)
	}
	// The climb ends at its highest point: ~2000 m from the foot, not 3000.
	if got[0].LengthM > 2200 {
		t.Errorf("length = %.0f; the descent after the summit leaked in", got[0].LengthM)
	}
	if math.Abs(got[0].GainM-120) > 12 {
		t.Errorf("gain = %.0f, want ~120", got[0].GainM)
	}
}

func TestTrainingMinimumLengthIs1km(t *testing.T) {
	short := profile(25, 100, seg{400, 0}, seg{900, 6}, seg{400, 0})
	if got := Detect(short, TrainingConfig); len(got) != 0 {
		t.Errorf("a 900 m climb was kept under TrainingConfig: %+v", got)
	}
	long := profile(25, 100, seg{400, 0}, seg{1100, 6}, seg{400, 0})
	if got := Detect(long, TrainingConfig); len(got) != 1 {
		t.Errorf("a 1100 m climb was dropped under TrainingConfig: %+v", got)
	}
	// The device bar is 500 m: the 900 m ramp is a device climb.
	if got := Detect(short, DeviceConfig); len(got) != 1 {
		t.Errorf("DeviceConfig dropped a 900 m climb: %+v", got)
	}
}

func TestMinimumAverageGradient(t *testing.T) {
	shallow := profile(25, 100, seg{400, 0}, seg{4000, 2.9}, seg{400, 0})
	if got := Detect(shallow, TrainingConfig); len(got) != 0 {
		t.Errorf("a 2.9%% climb was kept: %+v", got)
	}
	enough := profile(25, 100, seg{400, 0}, seg{4000, 3.1}, seg{400, 0})
	if got := Detect(enough, TrainingConfig); len(got) != 1 {
		t.Errorf("a 3.1%% climb was dropped: %+v", got)
	}
}

func TestNoElevationOnAnyPointReturnsNothing(t *testing.T) {
	pts := profile(25, 100, seg{300, 0}, seg{3000, 6}, seg{300, 0})
	pts[len(pts)/2].HasEle = false
	if got := Detect(pts, TrainingConfig); got != nil {
		t.Errorf("got %d climbs from half-real elevation", len(got))
	}
	if got := Detect(nil, TrainingConfig); got != nil {
		t.Errorf("nil points gave %+v", got)
	}
	if got := Detect(pts[:2], TrainingConfig); got != nil {
		t.Errorf("two points gave %+v", got)
	}
}

func TestCategoryThresholds(t *testing.T) {
	cases := []struct {
		score float64
		cat   int
		ok    bool
	}{
		{7_999, 0, false},
		{8_000, 4, true},
		{16_000, 3, true},
		{32_000, 2, true},
		{64_000, 1, true},
		{80_000, 0, true}, // hors categorie
	}
	for _, c := range cases {
		cat, ok := Category(c.score)
		if cat != c.cat || ok != c.ok {
			t.Errorf("Category(%.0f) = %d, %v; want %d, %v", c.score, cat, ok, c.cat, c.ok)
		}
	}
}

func TestClimbCarriesNoCoordinates(t *testing.T) {
	// Structural: Climb is distances and indexes only. A consumer that needs a
	// point looks it up from the index; this is what keeps response bodies
	// free of latitude and longitude.
	pts := profile(25, 100, seg{300, 0}, seg{3000, 6}, seg{300, 0})
	c := Detect(pts, TrainingConfig)[0]
	if c.StartIdx < 0 || c.EndIdx >= len(pts) {
		t.Fatalf("indices out of range: %+v", c)
	}
}
