package thresholds

import "testing"

// An eFTP that beats the test by 3% but is not itself a finding against the
// current FTP must not swallow the test's own finding.
func TestATestDownIsNotHiddenByAnEFTPThatOnlyBeatsTheTest(t *testing.T) {
	rides := []Ride{
		testRide("t", 3, "ramp", map[int]float64{60: 300}), // 225
		cyclingRide("e", 1, map[int]float64{1200: 247.4}),  // 235: >= 225 x 1.03 but < 250 x 1.03
	}
	f, ok := findField(t, Detect(rides, Profile{FTPWatts: 250}, today), "ftp")
	if !ok || f.Direction != "down" || f.Value != 225 || f.SourceSessionID != "t" {
		t.Errorf("finding = %+v ok=%v, want the test's down to 225", f, ok)
	}
}
