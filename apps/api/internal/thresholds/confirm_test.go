package thresholds

import "testing"

func TestFTPConfirmedByARecentRideWithinThreePercent(t *testing.T) {
	p := Profile{FTPWatts: 250}
	// 0.95 x 260 = 247: within 3% of 250, so the ride confirms it.
	rides := []Ride{
		cyclingRide("older", 20, map[int]float64{1200: 262}), // 248.9, also within
		cyclingRide("newer", 5, map[int]float64{1200: 260}),  // 247
	}
	date, ok := FTPConfirmedBy(rides, p, today)
	if !ok || date != daysAgo(5) {
		t.Errorf("got %q ok=%v, want the newest confirming ride on %s", date, ok, daysAgo(5))
	}
}

func TestFTPNotConfirmedByAWeakOrStrongRideOrOneOutsideTheWindow(t *testing.T) {
	p := Profile{FTPWatts: 250}
	cases := map[string][]Ride{
		"too weak (0.95 x 200 = 190)":   {cyclingRide("a", 5, map[int]float64{1200: 200})},
		"too strong (0.95 x 300 = 285)": {cyclingRide("b", 5, map[int]float64{1200: 300})},
		"outside the 42-day window":     {cyclingRide("c", 43, map[int]float64{1200: 260})},
		"not a cycling ride":            {{SessionID: "d", Date: daysAgo(5), Sport: "running", PowerCurve: map[int]float64{1200: 260}}},
		"no power curve":                {cyclingRide("e", 5, nil)},
	}
	for name, rides := range cases {
		if date, ok := FTPConfirmedBy(rides, p, today); ok {
			t.Errorf("%s: confirmed by %s", name, date)
		}
	}
}

func TestFTPConfirmedByNeedsAnFTPAndIgnoresTestRides(t *testing.T) {
	ride := cyclingRide("a", 5, map[int]float64{1200: 260})
	if _, ok := FTPConfirmedBy([]Ride{ride}, Profile{}, today); ok {
		t.Error("no FTP on file: nothing to confirm")
	}
	// A test ride's 1200 window is not an eFTP; its own result is recorded by
	// the sync, not through this path.
	test := testRide("t", 5, "ramp", map[int]float64{60: 330, 1200: 260})
	if _, ok := FTPConfirmedBy([]Ride{test}, Profile{FTPWatts: 250}, today); ok {
		t.Error("a test ride must not confirm through the eFTP path")
	}
}
