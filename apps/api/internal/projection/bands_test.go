package projection

import (
	"math"
	"testing"
)

func TestEventDurationIsDistanceOver28PlusClimbOver1000(t *testing.T) {
	h, stated := EventDuration(100_000, 1500)
	if !stated || math.Abs(h-(100.0/28+1.5)) > 1e-9 {
		t.Errorf("100 km / 1500 m = %v (%v), want %v stated", h, stated, 100.0/28+1.5)
	}
	if h, _ := EventDuration(56_000, 0); math.Abs(h-2) > 1e-9 {
		t.Errorf("distance only = %v, want 2", h)
	}
	if h, stated := EventDuration(0, 0); stated || h != MediumHours {
		t.Errorf("no distance = %v (%v), want the medium default, not stated", h, stated)
	}
	if h, stated := EventDuration(0, 800); stated || h != MediumHours {
		t.Errorf("elevation without distance = %v (%v), want the medium default", h, stated)
	}
}

func TestBandForDurationEdges(t *testing.T) {
	cases := []struct {
		hours    float64
		low, hi  float64
		ifFactor float64
	}{
		{1.99, 10, 25, 0.95},
		{2, 5, 20, 0.85},
		{4, 5, 20, 0.85},
		{4.01, 5, 15, 0.75},
	}
	for _, c := range cases {
		b := BandFor(c.hours)
		if b.Low != c.low || b.High != c.hi || b.IF != c.ifFactor {
			t.Errorf("%v h = %+v, want %v..%v IF %v", c.hours, b, c.low, c.hi, c.ifFactor)
		}
	}
}

func TestTargetCTLIsEventTSSOverFourClamped(t *testing.T) {
	// A 5 h fondo: 5 * 0.75^2 * 100 = 281.25 TSS, /4 = 70.3.
	if got := TargetCTL(5); math.Abs(got-281.25/4) > 1e-9 {
		t.Errorf("5 h target = %v, want %v", got, 281.25/4)
	}
	if got := TargetCTL(0.5); got != 30 {
		t.Errorf("a 30-minute crit = %v, want the floor of 30", got)
	}
	if got := TargetCTL(12); got != 120 {
		t.Errorf("12 h = %v, want the ceiling of 120", got)
	}
}
