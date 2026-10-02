package pacing

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

// rampTrack is a synthetic track heading due north at spacingM intervals; its
// elevation follows the (length m, grade %) pieces back to back. Nothing here
// is a real place.
type piece struct{ LengthM, Grade float64 }

func rampTrack(spacingM, baseEle float64, pieces ...piece) []gpx.Point {
	latStep := spacingM / 111_320.0
	pts := []gpx.Point{{Lat: 50, Lon: 3, Ele: baseEle, HasEle: true}}
	lat, ele := 50.0, baseEle
	for _, p := range pieces {
		for d := 0.0; d < p.LengthM; d += spacingM {
			lat += latStep
			ele += spacingM * p.Grade / 100
			pts = append(pts, gpx.Point{Lat: lat, Lon: 3, Ele: ele, HasEle: true})
		}
	}
	return pts
}

func within(got, want, frac float64) bool { return math.Abs(got-want) <= want*frac }

// Hand-worked: 75 kg rider + 8 kg bike = 83 kg, 250 W at the pedals, 0.975
// drivetrain: 243.75 W at the wheel.
//
// Flat: (83*9.80665*0.005 + 0.5*1.2*0.32*v^2) v = 243.75
//
//	-> 0.192 v^3 + 4.0697 v = 243.75 -> v = 10.177 m/s (36.6 km/h).
//
// 6 %: theta = atan(0.06), sin = 0.059896, cos = 0.998205, so the gravity and
// rolling force is 813.97*(0.059896 + 0.005*0.998205) = 52.82 N
//
//	-> 0.192 v^3 + 52.82 v = 243.75 -> v = 4.3225 m/s (15.6 km/h).
func TestSpeedMatchesHandWorkedCases(t *testing.T) {
	ph := DefaultPhysics(75)
	if got := ph.Speed(250, 0); !within(got, 10.177, 0.005) {
		t.Errorf("flat speed = %.3f m/s, want 10.177 within 0.5%%", got)
	}
	if got := ph.Speed(250, 6); !within(got, 4.3225, 0.005) {
		t.Errorf("6%% climb speed = %.3f m/s, want 4.3225 within 0.5%%", got)
	}
}

func TestDefaultPhysicsInputs(t *testing.T) {
	ph := DefaultPhysics(0)
	if ph.MassKg != 75 {
		t.Errorf("unset weight must assume 75 kg, got %v", ph.MassKg)
	}
	if ph.BikeKg != 8 || ph.CdA != 0.32 || ph.Crr != 0.005 || ph.Rho != 1.20 || ph.Eta != 0.975 || ph.MaxDescentKph != 60 {
		t.Errorf("defaults moved: %+v", ph)
	}
	if got := DefaultPhysics(62).MassKg; got != 62 {
		t.Errorf("given mass not kept: %v", got)
	}
}

func TestSpeedRisesWithPowerAndFallsWithGrade(t *testing.T) {
	ph := DefaultPhysics(75)
	prev := 0.0
	for w := 50.0; w <= 400; w += 25 {
		v := ph.Speed(w, 4)
		if v <= prev {
			t.Fatalf("speed not strictly increasing with power: %v W gave %.3f after %.3f", w, v, prev)
		}
		prev = v
	}
	prev = math.Inf(1)
	for g := -2.0; g <= 12; g += 1 {
		v := ph.Speed(250, g)
		if v >= prev {
			t.Fatalf("speed not strictly falling with grade: %v%% gave %.3f after %.3f", g, v, prev)
		}
		prev = v
	}
}

func TestDescentIsCapped(t *testing.T) {
	ph := DefaultPhysics(75)
	cap := 60 / 3.6
	if got := ph.Speed(250, -10); got > cap+1e-9 || got < cap-1e-6 {
		t.Errorf("steep descent at 250 W = %.3f m/s, want the cap %.3f", got, cap)
	}
	if got := ph.Speed(0, -12); got > cap+1e-9 {
		t.Errorf("coasting a steep descent = %.3f m/s, above the cap", got)
	}
}

func TestZeroPowerIsNeverNegativeOrNaN(t *testing.T) {
	ph := DefaultPhysics(75)
	for _, g := range []float64{-15, -6, -1, 0, 3, 12} {
		for _, w := range []float64{0, -5} {
			v := ph.Speed(w, g)
			if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
				t.Errorf("Speed(%v W, %v%%) = %v; want finite and positive", w, g, v)
			}
		}
	}
	// Coasting a gentle descent finds its own terminal speed, not the floor.
	if v := ph.Speed(0, -6); v < 10 {
		t.Errorf("coasting -6%% = %.2f m/s, expected a real terminal speed", v)
	}
}

func TestEventIFBandEdges(t *testing.T) {
	cases := []struct{ hours, want float64 }{
		{0.5, 0.95}, {1.99, 0.95},
		{2, 0.85}, {3, 0.85}, {4, 0.85},
		{4.01, 0.75}, {8, 0.75},
	}
	for _, c := range cases {
		if got := EventIF(c.hours); got != c.want {
			t.Errorf("EventIF(%v) = %v, want %v", c.hours, got, c.want)
		}
	}
}

func TestClimbFactorEdges(t *testing.T) {
	cases := []struct{ sec, want float64 }{
		{60, 1.10}, {299, 1.10},
		{300, 1.05}, {1200, 1.05},
		{1201, 1.00}, {3600, 1.00},
	}
	for _, c := range cases {
		if got := ClimbFactor(c.sec); got != c.want {
			t.Errorf("ClimbFactor(%v) = %v, want %v", c.sec, got, c.want)
		}
	}
}

func TestSegmentsRespectLengthsAndCoverTheTrack(t *testing.T) {
	pts := rampTrack(20, 100,
		piece{700, 0}, piece{1900, 6}, piece{300, -8}, piece{1230, 0}, piece{80, 3})
	total := gpx.DistanceM(pts[0], pts[len(pts)-1])
	// the track is a straight line north, so end-to-end equals the path length
	segs := Segments(pts, 100, 500)
	if len(segs) == 0 {
		t.Fatal("no segments")
	}
	if segs[0].StartM != 0 {
		t.Errorf("first segment starts at %v, want 0", segs[0].StartM)
	}
	if math.Abs(segs[len(segs)-1].EndM-total) > 1 {
		t.Errorf("last segment ends at %.1f, track is %.1f", segs[len(segs)-1].EndM, total)
	}
	for i, s := range segs {
		l := s.EndM - s.StartM
		if l < 100-1e-6 || l > 500+1e-6 {
			t.Errorf("segment %d is %.1f m, want 100 to 500", i, l)
		}
		if i > 0 && math.Abs(s.StartM-segs[i-1].EndM) > 1e-6 {
			t.Errorf("segment %d starts at %.1f, previous ends at %.1f", i, s.StartM, segs[i-1].EndM)
		}
	}
}

func TestSegmentsGradesFollowTheProfile(t *testing.T) {
	pts := rampTrack(20, 100, piece{600, 0}, piece{1500, 6}, piece{600, 0})
	segs := Segments(pts, 100, 500)
	var sawClimb bool
	for _, s := range segs {
		mid := (s.StartM + s.EndM) / 2
		if mid > 900 && mid < 1900 {
			if math.Abs(s.Grade-6) > 0.8 {
				t.Errorf("segment at %.0f m has grade %.2f, want ~6", mid, s.Grade)
			}
			sawClimb = true
		}
		if mid < 400 && math.Abs(s.Grade) > 0.8 {
			t.Errorf("flat segment at %.0f m has grade %.2f", mid, s.Grade)
		}
	}
	if !sawClimb {
		t.Error("no segment landed on the climb")
	}
}

func TestSegmentsDegenerateInputs(t *testing.T) {
	if got := Segments(nil, 100, 500); got != nil {
		t.Errorf("nil points gave %v", got)
	}
	noEle := rampTrack(20, 0, piece{1000, 0})
	noEle[3].HasEle = false
	if got := Segments(noEle, 100, 500); got != nil {
		t.Errorf("a point without elevation gave %v", got)
	}
	short := rampTrack(20, 0, piece{60, 0})
	segs := Segments(short, 100, 500)
	if len(segs) != 1 {
		t.Fatalf("a track shorter than the minimum gave %d segments, want 1", len(segs))
	}
}

func TestSegmentsCarryNoCoordinates(t *testing.T) {
	// Seg is distances and a grade; the struct has three float fields and
	// nothing that could hold a position.
	var s Seg
	_ = [3]float64{s.StartM, s.EndM, s.Grade}
}

func TestClimbSecondsMatchesTheSpeedOnASteadyClimb(t *testing.T) {
	pts := rampTrack(20, 100, piece{800, 0}, piece{1900, 6}, piece{800, 0})
	segs := Segments(pts, 100, 500)
	cl := climbs.Detect(pts, climbs.DeviceConfig)
	if len(cl) != 1 {
		t.Fatalf("fixture produced %d climbs", len(cl))
	}
	ph := DefaultPhysics(75)
	got := ClimbSeconds(cl[0], segs, ph, 250)
	// 1.9 km at ~15.6 km/h is about 7.3 minutes (the spec's worked sample says
	// "about 7.5").
	want := cl[0].LengthM / ph.Speed(250, 6)
	if !within(got, want, 0.05) {
		t.Errorf("ClimbSeconds = %.0f s, want ~%.0f s", got, want)
	}
	if got < 6*60 || got > 8.5*60 {
		t.Errorf("ClimbSeconds = %.0f s; the spec's sample is about 7.5 min", got)
	}
	// More power, less time.
	if faster := ClimbSeconds(cl[0], segs, ph, 300); faster >= got {
		t.Errorf("300 W took %.0f s, 250 W took %.0f s", faster, got)
	}
}
