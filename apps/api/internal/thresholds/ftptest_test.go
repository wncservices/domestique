package thresholds

import (
	"strings"
	"testing"
)

// testRide is a completed FTP test: the ride whose workout carries
// test_protocol. days is how long ago it was ridden.
func testRide(id string, days int, protocol string, curve map[int]float64) Ride {
	r := cyclingRide(id, days, curve)
	r.TestProtocol = protocol
	return r
}

func TestTestRideProducesTheTestValueAndNoCompetingEFTP(t *testing.T) {
	// Best minute 416 W -> 0.75 x 416 = 312. The same ride's 20-minute power
	// would read as an eFTP of 0.95 x 400 = 380 if it were treated as an
	// ordinary ride; it must not be.
	rides := []Ride{testRide("t1", 2, "ramp", map[int]float64{60: 416, 1200: 400})}
	got := Detect(rides, Profile{FTPWatts: 255}, today)

	var ftp []Finding
	for _, f := range got {
		if f.Field == "ftp" {
			ftp = append(ftp, f)
		}
	}
	if len(ftp) != 1 {
		t.Fatalf("want exactly one ftp finding, got %+v", ftp)
	}
	f := ftp[0]
	if f.Value != 312 || f.Direction != "up" || f.Previous != 255 {
		t.Errorf("finding = %+v, want 312 up from 255", f)
	}
	wantDay := today.AddDate(0, 0, -2).Weekday().String()
	if want := "from " + wantDay + "'s ramp test (416 W best minute)"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
	if f.SourceSessionID != "t1" {
		t.Errorf("source = %q, want t1", f.SourceSessionID)
	}
	if f.Auto {
		t.Error("auto = true on a rider-typed FTP: a test result is a suggestion there")
	}
}

func TestTestValueUsesEachProtocolsOwnFormula(t *testing.T) {
	cases := map[string]struct {
		curve  map[int]float64
		want   float64
		reason string
	}{
		"twenty_minute": {map[int]float64{1200: 300}, 285, "20-minute test (300 W best 20 minutes)"},
		"two_by_eight":  {map[int]float64{480: 300}, 270, "2 x 8-minute test (300 W best 8 minutes)"},
	}
	for protocol, c := range cases {
		got, _ := findField(t, Detect([]Ride{testRide("t", 1, protocol, c.curve)}, Profile{FTPWatts: 200}, today), "ftp")
		if got.Value != c.want || !strings.Contains(got.Reason, c.reason) {
			t.Errorf("%s: %+v, want %v / %q", protocol, got, c.want, c.reason)
		}
	}
}

func TestTestFiresAtOnePercentUpAndDownButNotBelow(t *testing.T) {
	ftp := Profile{FTPWatts: 300}
	cases := []struct {
		name string
		best float64 // best minute; FTP = 0.75 x this
		dir  string  // "" = no finding
	}{
		{"exactly +1%", 404, "up"},    // 303
		{"just under +1%", 403.9, ""}, // 302.925
		{"exactly -1%", 396, "down"},  // 297
		{"just over -1%", 396.4, ""},  // 297.3
		{"much higher", 440, "up"},    // 330
		{"identical", 400, ""},        // 300
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Detect([]Ride{testRide("t", 1, "ramp", map[int]float64{60: c.best})}, ftp, today)
			f, ok := findField(t, got, "ftp")
			if c.dir == "" {
				if ok {
					t.Fatalf("want no finding, got %+v", f)
				}
				return
			}
			if !ok || f.Direction != c.dir {
				t.Fatalf("want %s finding, got %+v ok=%v", c.dir, f, ok)
			}
		})
	}
}

func TestTestDownNeedsNeitherHistoryNorTheNinetyFivePercentRule(t *testing.T) {
	// Only the test ride exists: no rides 90+ days back, and 0.99 x FTP is far
	// above the 95% line an eFTP down move has to clear.
	got := Detect([]Ride{testRide("t", 1, "ramp", map[int]float64{60: 396})}, Profile{FTPWatts: 300, FTPEstimated: true}, today)
	f, ok := findField(t, got, "ftp")
	if !ok || f.Direction != "down" || f.Value != 297 {
		t.Fatalf("finding = %+v ok=%v, want down to 297", f, ok)
	}
	if f.Auto {
		t.Error("a down move is always a suggestion, even for an estimated FTP")
	}
}

func TestEFTPBeatsARecentTestOnlyAtThreePercentAbove(t *testing.T) {
	test := testRide("t", 3, "ramp", map[int]float64{60: 416}) // 312
	profile := Profile{FTPWatts: 250}

	// 0.95 x 340 = 323 >= 312 x 1.03 = 321.36: a real breakthrough, eFTP wins.
	got := Detect([]Ride{test, cyclingRide("e", 1, map[int]float64{1200: 340})}, profile, today)
	f, _ := findField(t, got, "ftp")
	if f.Value != 323 || f.SourceSessionID != "e" {
		t.Errorf("breakthrough: finding = %+v, want eFTP 323 from e", f)
	}

	// 0.95 x 335 = 318.25 < 321.36: the test stands.
	got = Detect([]Ride{test, cyclingRide("e", 1, map[int]float64{1200: 335})}, profile, today)
	f, _ = findField(t, got, "ftp")
	if f.Value != 312 || f.SourceSessionID != "t" {
		t.Errorf("no breakthrough: finding = %+v, want the test's 312", f)
	}
}

func TestEFTPAloneNeverLowersAnFTPARecentTestSet(t *testing.T) {
	profile := Profile{FTPWatts: 312}
	old := cyclingRide("old", 120, map[int]float64{1200: 340}) // history exists
	weak := cyclingRide("weak", 20, map[int]float64{1200: 200})

	// Baseline: without a test ride, the ordinary down rule fires.
	if f, ok := findField(t, Detect([]Ride{old, weak}, profile, today), "ftp"); !ok || f.Direction != "down" {
		t.Fatalf("baseline should be a down finding, got %+v ok=%v", f, ok)
	}
	// A test ride inside the window that confirms 312 silences it.
	test := testRide("t", 10, "ramp", map[int]float64{60: 416})
	if f, ok := findField(t, Detect([]Ride{old, weak, test}, profile, today), "ftp"); ok {
		t.Errorf("eFTP proposed lowering a test-set FTP: %+v", f)
	}
}

func TestATestOutsideTheWindowNoLongerProtectsFTP(t *testing.T) {
	profile := Profile{FTPWatts: 312}
	rides := []Ride{
		cyclingRide("old", 130, map[int]float64{1200: 340}),
		cyclingRide("weak", 20, map[int]float64{1200: 200}),
		testRide("t", 60, "ramp", map[int]float64{60: 416}), // 60 days: outside 42
	}
	if f, ok := findField(t, Detect(rides, profile, today), "ftp"); !ok || f.Direction != "down" {
		t.Errorf("finding = %+v ok=%v, want the ordinary down rule to apply again", f, ok)
	}
}

func TestTestMovingFTPByMoreThanTwentyFivePercentNeverAutoApplies(t *testing.T) {
	cases := []struct {
		name     string
		profile  Profile
		best     float64
		wantAuto bool
		capped   bool
	}{
		{"+50% on an estimated FTP", Profile{FTPWatts: 200, FTPEstimated: true}, 400, false, true}, // 300
		{"exactly +25% is still auto", Profile{FTPWatts: 240, FTPEstimated: true}, 400, true, false},
		{"+24% on an estimated FTP is auto", Profile{FTPWatts: 242, FTPEstimated: true}, 400, true, false},
		{"empty FTP has no ratio and stays auto", Profile{}, 400, true, false},
		{"-40% is a suggestion and says so", Profile{FTPWatts: 500, FTPEstimated: true}, 400, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, ok := findField(t, Detect([]Ride{testRide("t", 1, "ramp", map[int]float64{60: c.best})}, c.profile, today), "ftp")
			if !ok {
				t.Fatal("want a finding")
			}
			if f.Auto != c.wantAuto {
				t.Errorf("auto = %v, want %v (%+v)", f.Auto, c.wantAuto, f)
			}
			if got := strings.Contains(f.Reason, "25%"); got != c.capped {
				t.Errorf("reason %q mentions 25%% = %v, want %v", f.Reason, got, c.capped)
			}
		})
	}
}

func TestAnEstimatedFTPAutoAppliesAnUpwardTestResult(t *testing.T) {
	f, _ := findField(t, Detect([]Ride{testRide("t", 1, "ramp", map[int]float64{60: 416})}, Profile{FTPWatts: 300, FTPEstimated: true}, today), "ftp")
	if !f.Auto || f.Value != 312 {
		t.Errorf("finding = %+v, want an auto 312", f)
	}
}

func TestTheLatestTestInTheWindowWins(t *testing.T) {
	rides := []Ride{
		testRide("old", 30, "ramp", map[int]float64{60: 500}), // 375
		testRide("new", 2, "ramp", map[int]float64{60: 416}),  // 312
	}
	f, _ := findField(t, Detect(rides, Profile{FTPWatts: 250}, today), "ftp")
	if f.Value != 312 || f.SourceSessionID != "new" {
		t.Errorf("finding = %+v, want the newer test's 312", f)
	}
}

func TestATestRideWithoutAUsableValueProducesNothing(t *testing.T) {
	// A ramp with no 60 s window (a ride under a minute) yields no test value,
	// and the ride must not fall back to eFTP either.
	rides := []Ride{testRide("t", 1, "ramp", map[int]float64{1200: 400})}
	if f, ok := findField(t, Detect(rides, Profile{FTPWatts: 200}, today), "ftp"); ok {
		t.Errorf("finding = %+v, want none", f)
	}
}

func TestARideWithoutATestProtocolStillProducesEFTP(t *testing.T) {
	rides := []Ride{cyclingRide("r", 1, map[int]float64{60: 416, 1200: 400})}
	f, _ := findField(t, Detect(rides, Profile{FTPWatts: 255}, today), "ftp")
	if f.Value != 380 {
		t.Errorf("value = %v, want the ordinary eFTP 380", f.Value)
	}
}
