package pacing

import "testing"

func TestClimbKindBoundaries(t *testing.T) {
	cases := []struct {
		sec  float64
		want string
	}{
		{30, "short"}, {239, "short"},
		{240, "medium"}, {479, "medium"},
		{480, "sustained"}, {1200, "sustained"},
		{1201, "long"}, {3600, "long"},
	}
	for _, c := range cases {
		if got := ClimbKind(c.sec); got != c.want {
			t.Errorf("ClimbKind(%v) = %q, want %q", c.sec, got, c.want)
		}
	}
}

func flat(lengthM float64) []Seg {
	var out []Seg
	for x := 0.0; x < lengthM; x += 500 {
		out = append(out, Seg{StartM: x, EndM: min(x+500, lengthM), Grade: 0})
	}
	return out
}

// Hand-worked on the flat: at 0.85 x 250 W = 212.5 W, 207 W at the wheel,
// 0.192 v^3 + 4.07 v = 207 gives about 9.6 m/s (34.6 km/h). So 60 km is
// about 1.7 h, 100 km about 2.9 h and 150 km about 4.3 h, one in each band.
func TestDerivedIFPicksTheBandOfTheFirstPassDuration(t *testing.T) {
	ph := DefaultPhysics(75)
	for _, c := range []struct {
		km   float64
		want float64
	}{{60, 0.95}, {100, 0.85}, {150, 0.75}} {
		if got := DerivedIF(flat(c.km*1000), ph, 250); got != c.want {
			t.Errorf("%v km: IF = %v, want %v", c.km, got, c.want)
		}
	}
}

func TestTotalSecondsIsDistanceOverSpeed(t *testing.T) {
	ph := DefaultPhysics(75)
	got := TotalSeconds(flat(10_000), ph, 250)
	want := 10_000 / ph.Speed(250, 0)
	if got < want*0.999 || got > want*1.001 {
		t.Errorf("TotalSeconds = %.1f, want %.1f", got, want)
	}
	if TotalSeconds(nil, ph, 250) != 0 {
		t.Error("no segments should take no time")
	}
}
