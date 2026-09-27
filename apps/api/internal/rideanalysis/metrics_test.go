package rideanalysis

import (
	"math"
	"testing"
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
