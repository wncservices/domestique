package pacing

import (
	"math"
	"strings"
	"testing"
	"unicode"

	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// hilly is a synthetic route: flat, a 2.3 km climb at 6 %, a descent, a flat
// bit, a 1 km climb at 8 %, a second descent, flat. No real place.
func hillyInput(ftp, ifv float64, prof workout.RiderProfile) Input {
	pts := rampTrack(25, 100,
		piece{1500, 0}, piece{2300, 6}, piece{1000, -5}, piece{500, 0}, piece{1000, 8}, piece{1000, -4}, piece{1500, 0})
	return Input{
		Segs:    Segments(pts, 100, 500),
		Climbs:  climbs.Detect(pts, climbs.DeviceConfig),
		FTP:     ftp,
		IF:      ifv,
		Phys:    DefaultPhysics(75),
		Profile: prof,
	}
}

// independentNP is normalised power read straight off a plan's segments: the
// 30 s rolling mean of one-second power, fourth-power mean, fourth root. Written
// out here so the plan is not checked against its own helper.
func independentNP(p Plan) float64 {
	var power []float64
	for _, s := range p.Segments {
		for i := 0; i < int(math.Round(s.Seconds)); i++ {
			power = append(power, s.Watts)
		}
	}
	if len(power) < 30 {
		return 0
	}
	var sum4 float64
	var n int
	for i := 29; i < len(power); i++ {
		var m float64
		for j := i - 29; j <= i; j++ {
			m += power[j]
		}
		m /= 30
		sum4 += m * m * m * m
		n++
	}
	return math.Pow(sum4/float64(n), 0.25)
}

func TestPlanNormalisedPowerEqualsFTPTimesIF(t *testing.T) {
	for _, ifv := range []float64{0.75, 0.85, 0.95} {
		in := hillyInput(250, ifv, workout.RiderProfile{})
		plan := Build(in)
		if plan.Reason != "" || len(plan.Segments) == 0 {
			t.Fatalf("IF %.2f: unavailable %q", ifv, plan.Reason)
		}
		want := 250 * ifv
		if got := independentNP(plan); math.Abs(got-want) > 0.005*want {
			t.Errorf("IF %.2f: NP of the timeline = %.1f, want %.1f within 0.5%%", ifv, got, want)
		}
		if math.Abs(plan.NormalizedW-want) > 0.005*want || math.Abs(plan.IF-ifv) > 0.005 {
			t.Errorf("IF %.2f: plan reports NP %.1f / IF %.3f", ifv, plan.NormalizedW, plan.IF)
		}
	}
}

func TestClimbWattsFollowTheFactorsAndAreCapped(t *testing.T) {
	plan := Build(hillyInput(250, 0.85, workout.RiderProfile{}))
	var flatW float64
	for _, s := range plan.Segments {
		if s.Kind == "flat" {
			flatW = s.Watts
		}
	}
	if flatW == 0 {
		t.Fatal("no flat segment")
	}
	sawClimb := false
	for _, s := range plan.Segments {
		if s.Kind != "climb" {
			continue
		}
		sawClimb = true
		var sec float64
		for _, c := range plan.Climbs {
			if c.Index == s.ClimbIndex {
				sec = c.Seconds
			}
		}
		f := ClimbFactor(sec)
		want := math.Min(flatW*f, 250*f)
		if math.Abs(s.Watts-want) > 0.5 {
			t.Errorf("climb %d (%.0f s): %.1f W, want %.1f (flat %.1f x %.2f)", s.ClimbIndex, sec, s.Watts, want, flatW, f)
		}
	}
	if !sawClimb {
		t.Fatal("no climb segment")
	}

	// An override IF above 1: the flat-level power exceeds FTP, and the cap is
	// what stops a long climb being pushed over threshold.
	hot := Build(hillyInput(250, 1.05, workout.RiderProfile{}))
	capped := false
	for _, s := range hot.Segments {
		if s.Kind != "climb" {
			continue
		}
		var sec float64
		for _, c := range hot.Climbs {
			if c.Index == s.ClimbIndex {
				sec = c.Seconds
			}
		}
		limit := 250 * ClimbFactor(sec)
		if s.Watts > limit+0.5 {
			t.Errorf("IF 1.05: climb %d at %.1f W is over the cap %.1f", s.ClimbIndex, s.Watts, limit)
		}
		if math.Abs(s.Watts-limit) < 0.5 {
			capped = true
		}
	}
	if !capped {
		t.Error("expected the cap to bind on at least one climb at IF 1.05")
	}
}

func TestDescentsSteeperThanMinusThreePercentGetAnEasySpin(t *testing.T) {
	plan := Build(hillyInput(250, 0.85, workout.RiderProfile{}))
	var sawDescent bool
	for _, s := range plan.Segments {
		switch s.Kind {
		case "descent":
			sawDescent = true
			if math.Abs(s.Watts-0.55*250) > 0.5 {
				t.Errorf("descent at %.1f W, want %.1f (0.55 x FTP)", s.Watts, 0.55*250)
			}
			if s.Gradient >= -3 {
				t.Errorf("a %.1f%% stretch is not a descent", s.Gradient)
			}
		default:
			if s.Gradient < -3.0 && s.Kind == "flat" {
				t.Errorf("a %.1f%% stretch was treated as flat", s.Gradient)
			}
		}
	}
	if !sawDescent {
		t.Error("no descent segment in a route with two")
	}
}

func TestAdjacentSegmentsOfOneKindAreMerged(t *testing.T) {
	plan := Build(hillyInput(250, 0.85, workout.RiderProfile{}))
	if n := len(plan.Segments); n < 5 || n > 12 {
		t.Errorf("%d segments, want a readable handful (the raw profile has ~40)", n)
	}
	for i := 1; i < len(plan.Segments); i++ {
		a, b := plan.Segments[i-1], plan.Segments[i]
		if a.Kind == b.Kind && a.ClimbIndex == b.ClimbIndex {
			t.Errorf("segments %d and %d are both %s (climb %d) and were not merged", i-1, i, a.Kind, a.ClimbIndex)
		}
		if math.Abs(a.EndM-b.StartM) > 1e-6 {
			t.Errorf("a gap or overlap between segments %d and %d", i-1, i)
		}
	}
	var total float64
	for _, s := range plan.Segments {
		total += s.Seconds
	}
	if math.Abs(total-plan.Seconds) > 1 {
		t.Errorf("segment seconds sum to %.0f, plan says %.0f", total, plan.Seconds)
	}
}

// The few metres where a summit rolls over are part of the descent, not a
// ten-second "flat" line of their own.
func TestASummitRolloverIsNotAFlatLineOfItsOwn(t *testing.T) {
	plan := Build(hillyInput(250, 0.85, workout.RiderProfile{}))
	for i, s := range plan.Segments {
		if s.Kind == KindFlat && s.EndM-s.StartM < 200 && i > 0 && i < len(plan.Segments)-1 {
			t.Errorf("segment %d is a %.0f m flat stub between %s and %s", i, s.EndM-s.StartM, plan.Segments[i-1].Kind, plan.Segments[i+1].Kind)
		}
	}
}

func TestTargetsAreThreePercentEitherSideRoundedToFiveWatts(t *testing.T) {
	plan := Build(hillyInput(250, 0.85, workout.RiderProfile{}))
	for _, s := range plan.Segments {
		if s.WattsLow%5 != 0 || s.WattsHigh%5 != 0 {
			t.Errorf("targets %d-%d are not multiples of 5", s.WattsLow, s.WattsHigh)
		}
		if math.Abs(float64(s.WattsLow)-0.97*s.Watts) > 2.6 || math.Abs(float64(s.WattsHigh)-1.03*s.Watts) > 2.6 {
			t.Errorf("targets %d-%d are not 97-103%% of %.1f", s.WattsLow, s.WattsHigh, s.Watts)
		}
		if s.WattsLow > s.WattsHigh {
			t.Errorf("low %d above high %d", s.WattsLow, s.WattsHigh)
		}
	}
}

func TestAFlatCourseGivesConstantPower(t *testing.T) {
	pts := rampTrack(25, 50, piece{12_000, 0})
	plan := Build(Input{Segs: Segments(pts, 100, 500), FTP: 250, IF: 0.85, Phys: DefaultPhysics(75)})
	if len(plan.Segments) != 1 || plan.Segments[0].Kind != "flat" {
		t.Fatalf("a flat course should be one flat segment, got %+v", plan.Segments)
	}
	if math.Abs(plan.Segments[0].Watts-250*0.85) > 0.5 {
		t.Errorf("flat power = %.1f, want %.1f", plan.Segments[0].Watts, 250*0.85)
	}
	if math.Abs(plan.VI-1) > 0.001 {
		t.Errorf("VI = %.4f on a flat course, want 1", plan.VI)
	}
	// 12 km at ~212 W is about 36 km/h.
	if plan.AvgKph < 30 || plan.AvgKph > 40 {
		t.Errorf("average speed %.1f km/h", plan.AvgKph)
	}
}

func TestHeartRateRangesComeFromTheLibraryAndAreAbsentWithoutHRData(t *testing.T) {
	prof := workout.RiderProfile{ThresholdHR: 170, MaxHR: 190}
	plan := Build(hillyInput(250, 0.85, prof))
	for _, s := range plan.Segments {
		zone := zoneForPct(s.Watts / 250)
		low, high, ok := workoutlib.HRRange(model.SportCycling, prof, zone)
		if !ok || s.HRLow != int(math.Round(low)) || s.HRHigh != int(math.Round(high)) {
			t.Errorf("%s at %.0f W: HR %d-%d, want %.0f-%.0f (%s)", s.Kind, s.Watts, s.HRLow, s.HRHigh, low, high, zone)
		}
	}
	bare := Build(hillyInput(250, 0.85, workout.RiderProfile{}))
	for _, s := range bare.Segments {
		if s.HRLow != 0 || s.HRHigh != 0 {
			t.Errorf("a rider with no HR data got HR %d-%d", s.HRLow, s.HRHigh)
		}
	}
	// Max HR alone is enough.
	maxOnly := Build(hillyInput(250, 0.85, workout.RiderProfile{MaxHR: 190}))
	if maxOnly.Segments[0].HRHigh == 0 {
		t.Error("max HR alone should give HR ranges")
	}
}

func TestNoFTPIsUnavailable(t *testing.T) {
	in := hillyInput(0, 0.85, workout.RiderProfile{MaxHR: 190, ThresholdHR: 170})
	plan := Build(in)
	if plan.Reason != ReasonNoFTP || len(plan.Segments) != 0 {
		t.Errorf("plan = %+v, want unavailable for no FTP (HR cannot drive the model)", plan)
	}
	if got := Build(Input{FTP: 250, IF: 0.85}); got.Reason == "" {
		t.Error("no profile should be unavailable too")
	}
}

func TestCueNamesAreShortASCII(t *testing.T) {
	cases := []struct {
		n, low, high int
		hr           bool
		want         string
	}{
		{3, 250, 265, false, "C3 250-265W"},
		{3, 148, 156, true, "C3 148-156bpm"},
		{1, 95, 105, false, "C1 95-105W"},
		{12, 250, 265, false, "C12 250-265W"},
		{12, 148, 156, true, "C12 148-156bpm"},
	}
	for _, c := range cases {
		got := CueName(c.n, c.low, c.high, c.hr)
		if got != c.want {
			t.Errorf("CueName(%d, %d, %d, %v) = %q, want %q", c.n, c.low, c.high, c.hr, got, c.want)
		}
	}
	// Every combination stays within 15 characters and ASCII, with no en dash.
	for n := 1; n <= 120; n += 7 {
		for _, v := range [][2]int{{5, 10}, {95, 105}, {250, 265}, {999, 1050}} {
			for _, hr := range []bool{false, true} {
				got := CueName(n, v[0], v[1], hr)
				if len(got) > 15 {
					t.Errorf("%q is %d characters", got, len(got))
				}
				for _, r := range got {
					if r > unicode.MaxASCII {
						t.Errorf("%q has non-ASCII %q", got, r)
					}
				}
				if strings.ContainsRune(got, '–') {
					t.Errorf("%q has an en dash", got)
				}
			}
		}
	}
}
