package rideimport

import "testing"

// The file's own total ascent (metres) is the imported ride's climbing, so a
// season review of years imported from an export has an elevation total; a file
// that does not say reads 0, "not reported".
func TestParseReadsTheSessionsTotalAscent(t *testing.T) {
	with := rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 1800, Watts: 200, HR: 140, AscentM: 640}.Build(t)
	r, err := Parse(with, profile)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ascent != 640 {
		t.Errorf("ascent = %v, want 640", r.Ascent)
	}

	without := rideSpec{Start: at(2026, 3, 5, 9, 0), Seconds: 1800, Watts: 200, HR: 140}.Build(t)
	r, err = Parse(without, profile)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ascent != 0 {
		t.Errorf("a file with no ascent reads %v, want 0", r.Ascent)
	}
}
