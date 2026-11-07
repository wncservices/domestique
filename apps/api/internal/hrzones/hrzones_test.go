package hrzones

import (
	"math"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func boundsEqual(got [4]float64, want [4]float64) bool {
	for i := range got {
		if !near(got[i], want[i]) {
			return false
		}
	}
	return true
}

func TestBoundsPerMethod(t *testing.T) {
	cases := []struct {
		name  string
		p     workout.RiderProfile
		sport string
		want  [4]float64
		ok    bool
	}{
		{
			name:  "threshold cycling",
			p:     workout.RiderProfile{ThresholdHR: 160, HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesThreshold}},
			sport: "cycling",
			want:  [4]float64{129.6, 144, 150.4, 160},
			ok:    true,
		},
		{
			name:  "threshold running uses the running fractions",
			p:     workout.RiderProfile{ThresholdHR: 160, HRZonesRunning: workout.HRZoneSetting{Method: workout.HRZonesThreshold}},
			sport: "running",
			want:  [4]float64{136, 144, 152, 160},
			ok:    true,
		},
		{
			name:  "max",
			p:     workout.RiderProfile{MaxHR: 190, HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesMax}},
			sport: "cycling",
			want:  [4]float64{114, 133, 152, 171},
			ok:    true,
		},
		{
			name:  "reserve",
			p:     workout.RiderProfile{MaxHR: 190, RestingHR: 50, HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesReserve}},
			sport: "cycling",
			want:  [4]float64{134, 148, 162, 176},
			ok:    true,
		},
		{
			name: "custom passes its bounds through",
			p: workout.RiderProfile{HRZonesCycling: workout.HRZoneSetting{
				Method: workout.HRZonesCustom, Bounds: []int{120, 140, 155, 170},
			}},
			sport: "cycling",
			want:  [4]float64{120, 140, 155, 170},
			ok:    true,
		},
		{
			name:  "auto with threshold uses the threshold table",
			p:     workout.RiderProfile{ThresholdHR: 160, MaxHR: 190},
			sport: "cycling",
			want:  [4]float64{129.6, 144, 150.4, 160},
			ok:    true,
		},
		{
			name:  "auto without threshold falls back to max",
			p:     workout.RiderProfile{MaxHR: 190},
			sport: "cycling",
			want:  [4]float64{114, 133, 152, 171},
			ok:    true,
		},
		{
			name:  "auto with nothing is no zones",
			p:     workout.RiderProfile{},
			sport: "cycling",
			ok:    false,
		},
		{
			name:  "threshold without threshold HR is no zones",
			p:     workout.RiderProfile{MaxHR: 190, HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesThreshold}},
			sport: "cycling",
			ok:    false,
		},
		{
			name:  "max without max HR is no zones",
			p:     workout.RiderProfile{ThresholdHR: 160, HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesMax}},
			sport: "cycling",
			ok:    false,
		},
		{
			name:  "reserve without resting HR is no zones",
			p:     workout.RiderProfile{MaxHR: 190, HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesReserve}},
			sport: "cycling",
			ok:    false,
		},
		{
			name:  "reserve with resting at or above max is no zones",
			p:     workout.RiderProfile{MaxHR: 190, RestingHR: 190, HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesReserve}},
			sport: "cycling",
			ok:    false,
		},
		{
			name: "custom with a bad length is no zones",
			p: workout.RiderProfile{HRZonesCycling: workout.HRZoneSetting{
				Method: workout.HRZonesCustom, Bounds: []int{120, 140, 155},
			}},
			sport: "cycling",
			ok:    false,
		},
		{
			name:  "the running setting is not read for cycling",
			p:     workout.RiderProfile{HRZonesRunning: workout.HRZoneSetting{Method: workout.HRZonesCustom, Bounds: []int{120, 140, 155, 170}}},
			sport: "cycling",
			ok:    false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Bounds(c.p, c.sport)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && !boundsEqual(got, c.want) {
				t.Errorf("bounds = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSettingPicksTheSport(t *testing.T) {
	p := workout.RiderProfile{
		HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesMax},
		HRZonesRunning: workout.HRZoneSetting{Method: workout.HRZonesReserve},
	}
	if got := Setting(p, "running"); got.Method != workout.HRZonesReserve {
		t.Errorf("running = %q, want reserve", got.Method)
	}
	if got := Setting(p, "cycling"); got.Method != workout.HRZonesMax {
		t.Errorf("cycling = %q, want max", got.Method)
	}
	if got := Setting(p, "other"); got.Method != workout.HRZonesMax {
		t.Errorf("other sport = %q, want the cycling setting", got.Method)
	}
}

func TestValid(t *testing.T) {
	cases := []struct {
		name    string
		s       workout.HRZoneSetting
		wantErr bool
	}{
		{"auto", workout.HRZoneSetting{}, false},
		{"threshold", workout.HRZoneSetting{Method: workout.HRZonesThreshold}, false},
		{"max", workout.HRZoneSetting{Method: workout.HRZonesMax}, false},
		{"reserve", workout.HRZoneSetting{Method: workout.HRZonesReserve}, false},
		{"unknown method", workout.HRZoneSetting{Method: "karvonen"}, true},
		{"max with stray bounds", workout.HRZoneSetting{Method: workout.HRZonesMax, Bounds: []int{1, 2, 3, 4}}, true},
		{"auto with stray bounds", workout.HRZoneSetting{Bounds: []int{120, 140, 155, 170}}, true},
		{"custom", workout.HRZoneSetting{Method: workout.HRZonesCustom, Bounds: []int{120, 140, 155, 170}}, false},
		{"custom with no bounds", workout.HRZoneSetting{Method: workout.HRZonesCustom}, true},
		{"custom with three bounds", workout.HRZoneSetting{Method: workout.HRZonesCustom, Bounds: []int{120, 140, 155}}, true},
		{"custom with a repeat", workout.HRZoneSetting{Method: workout.HRZonesCustom, Bounds: []int{120, 140, 140, 170}}, true},
		{"custom descending", workout.HRZoneSetting{Method: workout.HRZonesCustom, Bounds: []int{140, 120, 155, 170}}, true},
		{"custom below 40", workout.HRZoneSetting{Method: workout.HRZonesCustom, Bounds: []int{30, 140, 155, 170}}, true},
		{"custom above 230", workout.HRZoneSetting{Method: workout.HRZonesCustom, Bounds: []int{120, 140, 155, 231}}, true},
		{"custom at the limits", workout.HRZoneSetting{Method: workout.HRZonesCustom, Bounds: []int{40, 41, 42, 230}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Valid(c.s)
			if (err != nil) != c.wantErr {
				t.Errorf("Valid = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestSecondsBinsIntoZones(t *testing.T) {
	// Boundaries 129.6, 144, 150.4, 160. bpm 128 (10 s) is below 129.6 so
	// Z1; bpm 130 (5 s) is at or above 129.6 and below 144, so Z2.
	h := workout.HRHistogram{Min: 128, Seconds: []int{10, 0, 5, 0, 0}, NoHR: 99}
	b := [4]float64{129.6, 144, 150.4, 160}
	z, ok := Seconds(h, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if want := [5]int{10, 5, 0, 0, 0}; z != want {
		t.Errorf("zones = %v, want %v", z, want)
	}

	// A value exactly on a boundary belongs to the higher zone: 144 (on b1)
	// and 145 both land in Z3, which starts at 144.
	h2 := workout.HRHistogram{Min: 144, Seconds: []int{7, 3}, NoHR: 0}
	z2, ok := Seconds(h2, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if want := [5]int{0, 0, 10, 0, 0}; z2 != want {
		t.Errorf("boundary zones = %v, want %v", z2, want)
	}

	// NoHR seconds are not counted anywhere.
	h3 := workout.HRHistogram{Min: 170, Seconds: []int{4}, NoHR: 500}
	z3, ok := Seconds(h3, b)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if want := [5]int{0, 0, 0, 0, 4}; z3 != want {
		t.Errorf("top zone = %v, want %v", z3, want)
	}
}

func TestSecondsWithoutHeartRateIsNotOK(t *testing.T) {
	b := [4]float64{129.6, 144, 150.4, 160}
	if _, ok := Seconds(workout.HRHistogram{NoHR: 900}, b); ok {
		t.Error("only NoHR seconds: ok = true, want false")
	}
	if _, ok := Seconds(workout.HRHistogram{}, b); ok {
		t.Error("empty histogram: ok = true, want false")
	}
}

func TestRangeForExplicitCustom(t *testing.T) {
	custom := workout.HRZoneSetting{Method: workout.HRZonesCustom, Bounds: []int{120, 140, 155, 170}}

	check := func(t *testing.T, p workout.RiderProfile, zone string, wantLow, wantHigh float64) {
		t.Helper()
		low, high, ok := Range(p, "cycling", zone)
		if !ok {
			t.Fatalf("%s: ok = false, want true", zone)
		}
		if !near(low, wantLow) || !near(high, wantHigh) {
			t.Errorf("%s = %v-%v, want %v-%v", zone, low, high, wantLow, wantHigh)
		}
	}

	withMax := workout.RiderProfile{MaxHR: 190, HRZonesCycling: custom}
	check(t, withMax, "endurance", 120, 140)
	check(t, withMax, "sweet_spot", 147.5, 162.5)
	check(t, withMax, "vo2max", 170, 190)
	check(t, withMax, "recovery", 100, 120)
	check(t, withMax, "tempo", 140, 155)
	check(t, withMax, "threshold", 155, 170)

	noMax := workout.RiderProfile{HRZonesCycling: custom}
	check(t, noMax, "vo2max", 170, 185)
	check(t, noMax, "intervals", 170, 185)
	check(t, noMax, "anaerobic", 170, 185)
}

func TestRangeIsNotOKForAutoOrMissingInputs(t *testing.T) {
	if _, _, ok := Range(workout.RiderProfile{MaxHR: 190, ThresholdHR: 160}, "cycling", "endurance"); ok {
		t.Error("auto: ok = true, want false so the caller keeps its own table")
	}
	p := workout.RiderProfile{HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesMax}}
	if _, _, ok := Range(p, "cycling", "endurance"); ok {
		t.Error("max without max HR: ok = true, want false")
	}
	p2 := workout.RiderProfile{MaxHR: 190, HRZonesCycling: workout.HRZoneSetting{Method: workout.HRZonesMax}}
	if _, _, ok := Range(p2, "cycling", "not-a-zone"); ok {
		t.Error("unknown zone: ok = true, want false")
	}
}

// TestLTHREdgesMatchTheFitnessPageTable pins the Go fractions to the
// frontend's LTHR_*_EDGES in fitnessMath.ts, the same way the rideanalysis
// test does: the page and the saved zones must draw the same boundaries.
func TestLTHREdgesMatchTheFitnessPageTable(t *testing.T) {
	src, err := os.ReadFile("../../../web/src/utils/fitnessMath.ts")
	if err != nil {
		t.Fatalf("read fitnessMath.ts: %v", err)
	}
	want := map[string][]float64{
		"cycling": lthrFractions["cycling"],
		"running": lthrFractions["running"],
	}
	for sport, fr := range want {
		name := "LTHR_" + strings.ToUpper(sport) + "_EDGES"
		m := regexp.MustCompile(name + `\s*=\s*\[([^\]]+)\]`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s not found in fitnessMath.ts", name)
		}
		var ts []float64
		for _, f := range strings.Split(string(m[1]), ",") {
			v, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
			if err != nil {
				t.Fatalf("%s: parse %q: %v", name, f, err)
			}
			ts = append(ts, v)
		}
		// The TS array leads with 0 (Z1's floor) then the four boundaries as
		// fractions of LTHR; the last one is 1.0 (the Z5 start).
		if len(ts) != 5 || ts[0] != 0 {
			t.Fatalf("%s = %v, want [0, e1, e2, e3, e4]", name, ts)
		}
		if !reflect.DeepEqual(ts[1:], fr) {
			t.Errorf("%s fractions: TS %v, Go %v", name, ts[1:], fr)
		}
	}
}

// TestMaxAndReserveFractions pins the max and reserve fractions to the
// spec's [0.6, 0.7, 0.8, 0.9] and to the fitnessMath.ts HR_ZONE_EDGES the
// max-HR page table uses (its Z1 floor is 0.5, then the four we need).
func TestMaxAndReserveFractions(t *testing.T) {
	want := []float64{0.6, 0.7, 0.8, 0.9}
	if !reflect.DeepEqual(maxFractions, want) {
		t.Errorf("maxFractions = %v, want %v", maxFractions, want)
	}
	if !reflect.DeepEqual(reserveFractions, want) {
		t.Errorf("reserveFractions = %v, want %v", reserveFractions, want)
	}

	src, err := os.ReadFile("../../../web/src/utils/fitnessMath.ts")
	if err != nil {
		t.Fatalf("read fitnessMath.ts: %v", err)
	}
	m := regexp.MustCompile(`HR_ZONE_EDGES\s*=\s*\[([^\]]+)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatal("HR_ZONE_EDGES not found in fitnessMath.ts")
	}
	var ts []float64
	for _, f := range strings.Split(string(m[1]), ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil {
			t.Fatalf("parse %q: %v", f, err)
		}
		ts = append(ts, v)
	}
	if len(ts) != 6 || !reflect.DeepEqual(ts[1:5], want) {
		t.Errorf("HR_ZONE_EDGES = %v, want the middle four to be %v", ts, want)
	}
}
