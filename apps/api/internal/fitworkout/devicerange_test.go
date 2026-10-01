package fitworkout

import "testing"

func TestDeviceRange(t *testing.T) {
	cases := []struct {
		name           string
		target         Target
		low, high      float64
		wantLo, wantHi float64
	}{
		{"a range passes through", TargetPower, 230, 250, 230, 250},
		{"a range is ordered", TargetPower, 250, 230, 230, 250},
		{"low power gets at least 5 W either side", TargetPower, 85, 85, 80, 90},
		{"higher power gets 5 %", TargetPower, 300, 300, 285, 315},
		{"heart rate gets at least 3 bpm", TargetHeartRate, 140, 140, 137, 143},
		{"cadence gets 5 rpm", TargetCadence, 90, 90, 85, 95},
	}
	for _, c := range cases {
		lo, hi := DeviceRange(c.target, c.low, c.high)
		if lo != c.wantLo || hi != c.wantHi {
			t.Errorf("%s: got %v-%v, want %v-%v", c.name, lo, hi, c.wantLo, c.wantHi)
		}
	}
}
