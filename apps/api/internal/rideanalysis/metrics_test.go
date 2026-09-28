package rideanalysis

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestResampleFillsShortGapsAndZeroesLongOnes(t *testing.T) {
	// Records at 0, 3 (a 3 s gap: repeat), 24 (a 20 s gap from 4 to 24:
	// zeroed) — see the brief's "3 s gap repeats" / "20 s gap is zeroed".
	act := buildActivity(t, []fixture{
		{Sec: 0, Power: 200, HR: 140},
		{Sec: 3, Power: 210, HR: 141},
		{Sec: 24, Power: 220, HR: 142},
	}, nil)

	samples := Resample(act.Records)
	if len(samples) != 25 {
		t.Fatalf("len(samples) = %d, want 25", len(samples))
	}

	// The 3 s gap (seconds 1, 2) repeats the previous sample (second 0).
	for _, sec := range []int{1, 2} {
		s := samples[sec]
		if !s.HasPower || s.Power != 200 {
			t.Errorf("sec %d: power = %v (has=%v), want 200 repeated from sec 0", sec, s.Power, s.HasPower)
		}
		if !s.HasHR || s.HeartRate != 140 {
			t.Errorf("sec %d: HR = %v (has=%v), want 140 repeated from sec 0", sec, s.HeartRate, s.HasHR)
		}
	}

	// The 20 s gap (seconds 4..23) is zeroed, not repeated.
	for _, sec := range []int{4, 13, 23} {
		s := samples[sec]
		if s.HasPower || s.Power != 0 {
			t.Errorf("sec %d: power = %v (has=%v), want zeroed", sec, s.Power, s.HasPower)
		}
		if s.HasHR || s.HeartRate != 0 {
			t.Errorf("sec %d: HR = %v (has=%v), want zeroed", sec, s.HeartRate, s.HasHR)
		}
	}

	// The actual records still carry their own values.
	if s := samples[0]; !s.HasPower || s.Power != 200 {
		t.Errorf("sec 0 = %+v", s)
	}
	if s := samples[3]; !s.HasPower || s.Power != 210 {
		t.Errorf("sec 3 = %+v", s)
	}
	if s := samples[24]; !s.HasPower || s.Power != 220 {
		t.Errorf("sec 24 = %+v", s)
	}
}

func TestNormalizedPowerOfConstantPowerRideEqualsThatPower(t *testing.T) {
	act := buildActivity(t, constantPower(3600, 250), nil)
	samples := Resample(act.Records)

	np := NormalizedPower(samples)
	if math.Abs(np-250) > 0.5 {
		t.Errorf("NP = %v, want 250 +/- 0.5", np)
	}
}

func TestNormalizedPowerZeroWithNoPowerData(t *testing.T) {
	act := buildActivity(t, constantHR(3600, 140), nil)
	samples := Resample(act.Records)

	np := NormalizedPower(samples)
	if np != 0 {
		t.Errorf("NP = %v, want 0", np)
	}
	if math.IsNaN(np) {
		t.Error("NP is NaN")
	}
}

func TestNormalizedPowerExceedsAverageForVariableEffort(t *testing.T) {
	act := buildActivity(t, alternatingPower(3600, 60, 400, 100), nil)
	samples := Resample(act.Records)

	np := NormalizedPower(samples)
	avg := 250.0 // (400+100)/2
	if np <= avg {
		t.Errorf("NP = %v, want > average (%v) for variable effort", np, avg)
	}
	if np <= 290 {
		t.Errorf("NP = %v, want > 290", np)
	}
}

func TestPowerTSSAtThresholdForOneHourIsOneHundred(t *testing.T) {
	ifactor, tss := PowerTSS(3600, 250, 250)
	if math.Abs(ifactor-1) > 0.001 {
		t.Errorf("IF = %v, want 1", ifactor)
	}
	if math.Abs(tss-100) > 0.5 {
		t.Errorf("TSS = %v, want 100", tss)
	}
}

func TestPowerTSSZeroFTPNeverDivides(t *testing.T) {
	ifactor, tss := PowerTSS(3600, 250, 0)
	if ifactor != 0 || tss != 0 {
		t.Errorf("IF=%v TSS=%v, want 0,0 when FTP is 0", ifactor, tss)
	}
}

func TestHRLoadOneHourAtThresholdIsOneHundred(t *testing.T) {
	act := buildActivity(t, constantHR(3600, 171), nil) // 0.9 * 190
	samples := Resample(act.Records)

	load := HRLoad(samples, 190, 50)
	if math.Abs(load-100) > 1 {
		t.Errorf("HR load = %v, want 100 +/- 1", load)
	}
}

func TestHRLoadDefaultRestHRIsHalfMaxHR(t *testing.T) {
	// restHR 0 -> rest = 0.5*maxHR = 95; threshold = 0.9*190 = 171.
	act := buildActivity(t, constantHR(3600, 171), nil)
	samples := Resample(act.Records)

	load := HRLoad(samples, 190, 0)
	if math.IsNaN(load) || math.IsInf(load, 0) {
		t.Fatalf("HR load = %v", load)
	}
	if load <= 0 {
		t.Errorf("HR load = %v, want positive", load)
	}
}

func TestPowerZoneSecondsSumToSampleCount(t *testing.T) {
	act := buildActivity(t, alternatingPower(1800, 30, 400, 100), nil)
	samples := Resample(act.Records)

	zones := PowerZoneSeconds(samples, 250)
	var sum int
	for _, z := range zones {
		sum += z
	}
	if sum != len(samples) {
		t.Errorf("zone sum = %d, want %d (sample count)", sum, len(samples))
	}
}

func TestHRZoneSecondsSumToSampleCount(t *testing.T) {
	act := buildActivity(t, constantHR(1800, 150), nil)
	samples := Resample(act.Records)

	zones := HRZoneSeconds(samples, 190)
	var sum int
	for _, z := range zones {
		sum += z
	}
	if sum != len(samples) {
		t.Errorf("zone sum = %d, want %d (sample count)", sum, len(samples))
	}
}

func TestPowerZoneBoundaries(t *testing.T) {
	// FTP 200: zone edges at 110, 150, 180, 210, 240, 300 W.
	samples := []Sample{
		{Power: 50, HasPower: true},  // Z1: < 55%
		{Power: 120, HasPower: true}, // Z2: 55-75%
		{Power: 160, HasPower: true}, // Z3: 75-90%
		{Power: 190, HasPower: true}, // Z4: 90-105%
		{Power: 220, HasPower: true}, // Z5: 105-120%
		{Power: 280, HasPower: true}, // Z6: 120-150%
		{Power: 400, HasPower: true}, // Z7: >=150%
	}
	zones := PowerZoneSeconds(samples, 200)
	for i, z := range zones {
		if z != 1 {
			t.Errorf("zone %d = %d, want 1 (samples: %+v)", i+1, z, zones)
		}
	}
}

func TestHRZoneBoundaries(t *testing.T) {
	// maxHR 200: zone edges at 120, 140, 160, 180.
	samples := []Sample{
		{HeartRate: 90, HasHR: true},  // Z1: < 60% (including below 50%)
		{HeartRate: 130, HasHR: true}, // Z2: 60-70%
		{HeartRate: 150, HasHR: true}, // Z3: 70-80%
		{HeartRate: 170, HasHR: true}, // Z4: 80-90%
		{HeartRate: 190, HasHR: true}, // Z5: >=90%
	}
	zones := HRZoneSeconds(samples, 200)
	for i, z := range zones {
		if z != 1 {
			t.Errorf("zone %d = %d, want 1 (zones: %+v)", i+1, z, zones)
		}
	}
}

func TestPowerCurveOnTenMinuteRideHasOnlyShorterKeys(t *testing.T) {
	act := buildActivity(t, constantPower(600, 200), nil)
	samples := Resample(act.Records)

	curve := PowerCurve(samples)
	wantKeys := map[int]bool{5: true, 60: true, 300: true}
	for k := range curve {
		if !wantKeys[k] {
			t.Errorf("unexpected key %d in power curve %v", k, curve)
		}
	}
	for k := range wantKeys {
		if _, ok := curve[k]; !ok {
			t.Errorf("missing key %d in power curve %v", k, curve)
		}
	}
	if _, ok := curve[1200]; ok {
		t.Errorf("power curve has key 1200 on a 10-minute ride: %v", curve)
	}
	if _, ok := curve[3600]; ok {
		t.Errorf("power curve has key 3600 on a 10-minute ride: %v", curve)
	}

	// Constant power: best average over any window equals that power.
	if math.Abs(curve[5]-200) > 0.01 || math.Abs(curve[60]-200) > 0.01 || math.Abs(curve[300]-200) > 0.01 {
		t.Errorf("power curve = %v, want 200 for every window on constant power", curve)
	}
}

func TestMaxHRIgnoresSpikeAbove230(t *testing.T) {
	fixtures := constantHR(60, 150)
	fixtures = append(fixtures, fixture{Sec: 60, HR: 195})
	fixtures = append(fixtures, fixture{Sec: 61, HR: 250}) // sensor spike, ignored
	act := buildActivity(t, fixtures, nil)
	samples := Resample(act.Records)

	if got := MaxHR(samples); got != 195 {
		t.Errorf("MaxHR = %d, want 195 (250 bpm spike ignored)", got)
	}
}

func TestMaxHRZeroWithNoHRData(t *testing.T) {
	act := buildActivity(t, constantPower(60, 200), nil)
	samples := Resample(act.Records)

	if got := MaxHR(samples); got != 0 {
		t.Errorf("MaxHR = %d, want 0 with no HR data", got)
	}
}

func TestBestSpeedsOnConstantSpeedRide(t *testing.T) {
	act := buildActivity(t, constantSpeed(2000, 8.0), nil)
	samples := Resample(act.Records)

	best1200, best1800 := BestSpeeds(samples)
	if math.Abs(best1200-8.0) > 0.01 || math.Abs(best1800-8.0) > 0.01 {
		t.Errorf("BestSpeeds = (%v, %v), want (8, 8) on constant-speed ride", best1200, best1800)
	}
}

func TestBestSpeedsOnlyLongerThan20MinuteWindowGetsThatOne(t *testing.T) {
	act := buildActivity(t, constantSpeed(1500, 8.0), nil)
	samples := Resample(act.Records)

	best1200, best1800 := BestSpeeds(samples)
	if math.Abs(best1200-8.0) > 0.01 {
		t.Errorf("best1200 = %v, want 8 on a 1500 s ride", best1200)
	}
	if best1800 != 0 {
		t.Errorf("best1800 = %v, want 0 on a ride shorter than 30 minutes", best1800)
	}
}

func TestBestSpeedsZeroWhenRideShorterThanEitherWindow(t *testing.T) {
	act := buildActivity(t, constantSpeed(1000, 8.0), nil)
	samples := Resample(act.Records)

	best1200, best1800 := BestSpeeds(samples)
	if best1200 != 0 || best1800 != 0 {
		t.Errorf("BestSpeeds = (%v, %v), want (0, 0) on a ride shorter than either window", best1200, best1800)
	}
}

func TestBestSpeedsZeroWithNoSpeedData(t *testing.T) {
	act := buildActivity(t, constantPower(2000, 200), nil)
	samples := Resample(act.Records)

	best1200, best1800 := BestSpeeds(samples)
	if best1200 != 0 || best1800 != 0 {
		t.Errorf("BestSpeeds = (%v, %v), want (0, 0) with no speed data", best1200, best1800)
	}
}

func TestBestHRIsHighestTwentyMinuteMean(t *testing.T) {
	// 10 min easy, then exactly 20 min at 170 bpm, then 5 min easy: the best
	// 20-minute window is the hard block, not the whole-ride average.
	fixtures := constantHR(600, 120)
	for i := 0; i < 1200; i++ {
		fixtures = append(fixtures, fixture{Sec: 600 + i, HR: 170})
	}
	for i := 0; i < 300; i++ {
		fixtures = append(fixtures, fixture{Sec: 1800 + i, HR: 110})
	}
	samples := Resample(buildActivity(t, fixtures, nil).Records)

	if got := BestHR(samples); got != 170 {
		t.Errorf("BestHR = %d, want 170", got)
	}
}

func TestBestHRZeroForRideUnderTwentyMinutes(t *testing.T) {
	samples := Resample(buildActivity(t, constantHR(1199, 170), nil).Records)
	if got := BestHR(samples); got != 0 {
		t.Errorf("BestHR = %d, want 0 for a 19:59 ride", got)
	}
}

func TestBestHRZeroWithNoHRData(t *testing.T) {
	samples := Resample(buildActivity(t, constantPower(1500, 200), nil).Records)
	if got := BestHR(samples); got != 0 {
		t.Errorf("BestHR = %d, want 0 with no HR data", got)
	}
}

func TestBestHRUnaffectedBySingleSpike(t *testing.T) {
	fixtures := constantHR(1500, 150)
	fixtures[700] = fixture{Sec: 700, HR: 250} // sensor spike
	samples := Resample(buildActivity(t, fixtures, nil).Records)
	if got := BestHR(samples); got != 150 {
		t.Errorf("BestHR = %d, want 150 (spike must not move a 20-minute mean)", got)
	}
}

func TestHRZoneSecondsLTHRUsesFrielEdgesPerSport(t *testing.T) {
	// LTHR 100 makes bpm equal percent. One second at each of: 80 (Z1),
	// 81 (Z2 start, cycling), 84 (Z1 running), 85 (Z2 running), 90 (Z3),
	// 94 (Z4 cycling), 95 (Z4 running), 99, 100 (Z5), 110.
	var s []Sample
	for _, bpm := range []int{80, 81, 84, 85, 90, 94, 95, 99, 100, 110} {
		s = append(s, Sample{Seconds: len(s), HeartRate: float64(bpm), HasHR: true})
	}
	cyc := HRZoneSecondsLTHR(s, 100, "cycling")
	if want := [5]int{1, 3, 1, 3, 2}; cyc != want {
		t.Errorf("cycling zones = %v, want %v", cyc, want)
	}
	run := HRZoneSecondsLTHR(s, 100, "running")
	if want := [5]int{3, 1, 2, 2, 2}; run != want {
		t.Errorf("running zones = %v, want %v", run, want)
	}
	// Unknown sport falls back to the cycling table; zero LTHR puts
	// everything in Z1 rather than dividing by zero.
	if got := HRZoneSecondsLTHR(s, 100, ""); got != cyc {
		t.Errorf("unknown sport = %v, want the cycling table %v", got, cyc)
	}
	if got := HRZoneSecondsLTHR(s, 0, "cycling"); got != [5]int{len(s), 0, 0, 0, 0} {
		t.Errorf("zero LTHR = %v, want everything in Z1", got)
	}
}

// TestLTHREdgesMatchTheFitnessPageTable pins the Go edges to the frontend's
// LTHR_*_EDGES in fitnessMath.ts: a ride's stored zone-seconds must mean the
// same "Z3" as the zone list the page draws (review focus 4).
func TestLTHREdgesMatchTheFitnessPageTable(t *testing.T) {
	src, err := os.ReadFile("../../../web/src/utils/fitnessMath.ts")
	if err != nil {
		t.Fatalf("read fitnessMath.ts: %v", err)
	}
	for sport, edges := range lthrZoneEdges {
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
		// The TS array leads with 0 (Z1's floor) and ends at 1 (Z5a's start);
		// the four in between and the top are the Go edges.
		if len(ts) != 5 || ts[0] != 0 {
			t.Fatalf("%s = %v, want [0, e1, e2, e3, e4]", name, ts)
		}
		for i, e := range edges {
			if ts[i+1] != e {
				t.Errorf("%s edge %d: TS %v, Go %v", sport, i, ts[i+1], e)
			}
		}
	}
}

func TestAnalyzeHRZonesFollowThresholdHRAndSport(t *testing.T) {
	// 100 s at 90 bpm, then 1400 s at 150: with MaxHR 190 both are low
	// %MaxHR; with LTHR 150 the second block sits at 100 % (Z5).
	fixtures := constantHR(100, 90)
	for i := 0; i < 1400; i++ {
		fixtures = append(fixtures, fixture{Sec: 100 + i, HR: 150})
	}
	act := buildActivity(t, fixtures, nil)

	withLTHR := Analyze(Input{Activity: act, Sport: "cycling", Profile: workout.RiderProfile{MaxHR: 190, ThresholdHR: 150}})
	if withLTHR.HRZoneSeconds[4] != 1400 || withLTHR.HRZoneSeconds[0] != 100 {
		t.Errorf("LTHR zones = %v, want 100 in Z1 and 1400 in Z5", withLTHR.HRZoneSeconds)
	}
	maxOnly := Analyze(Input{Activity: act, Sport: "cycling", Profile: workout.RiderProfile{MaxHR: 190}})
	// 150/190 = 0.79 -> Z3 (0.70-0.80) under today's %MaxHR table.
	if maxOnly.HRZoneSeconds[2] != 1400 {
		t.Errorf("max HR zones = %v, want 1400 in Z3 (unchanged)", maxOnly.HRZoneSeconds)
	}
	// 150/180 = 0.833: Z2 under cycling's 0.81 edge, still Z1 under running's 0.85.
	cyc := Analyze(Input{Activity: act, Sport: "cycling", Profile: workout.RiderProfile{ThresholdHR: 180}})
	run := Analyze(Input{Activity: act, Sport: "running", Profile: workout.RiderProfile{ThresholdHR: 180}})
	if cyc.HRZoneSeconds[1] != 1400 || run.HRZoneSeconds[0] != 1500 {
		t.Errorf("cycling zones = %v, running zones = %v, want the sport's own edges", cyc.HRZoneSeconds, run.HRZoneSeconds)
	}
}
