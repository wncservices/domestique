package workout

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A measured load is never replaced by an effort rating: RPE only stands in for
// the flat hours x 50 guess.
func TestEffortNeverOverridesAMeasuredLoad(t *testing.T) {
	profile := RiderProfile{FTPWatts: 250, MaxHR: 190}
	for effort := 0; effort <= 5; effort++ {
		power := TrainingLoadWithEffort(3600, 200, 0, profile, effort)
		if !near(power, TrainingLoad(3600, 200, 0, profile)) {
			t.Errorf("effort %d changed a power load: %v", effort, power)
		}
		hr := TrainingLoadWithEffort(3600, 0, 150, profile, effort)
		if !near(hr, TrainingLoad(3600, 0, 150, profile)) {
			t.Errorf("effort %d changed an HR load: %v", effort, hr)
		}
	}
}

func TestWithNeitherPowerNorHeartRateEffortSetsTheLoad(t *testing.T) {
	// hours x 100 x IF^2, IF = 0.55, 0.65, 0.78, 0.90, 1.00 for efforts 1-5.
	want := map[int]float64{
		1: 0.55 * 0.55 * 100,
		2: 0.65 * 0.65 * 100,
		3: 0.78 * 0.78 * 100,
		4: 0.90 * 0.90 * 100,
		5: 100,
	}
	for effort, w := range want {
		if got := TrainingLoadWithEffort(3600, 0, 0, RiderProfile{}, effort); !near(got, w) {
			t.Errorf("effort %d, one hour = %v, want %v", effort, got, w)
		}
	}
	if got := TrainingLoadWithEffort(5400, 0, 0, RiderProfile{}, 3); !near(got, 1.5*0.78*0.78*100) {
		t.Errorf("an hour and a half at effort 3 = %v", got)
	}
}

func TestWithoutAnEffortTheFlatGuessStays(t *testing.T) {
	if got := TrainingLoadWithEffort(7200, 0, 0, RiderProfile{}, 0); !near(got, 100) {
		t.Errorf("two hours, unrated = %v, want the flat hours x 50", got)
	}
	for _, effort := range []int{-1, 6, 99} {
		if got := TrainingLoadWithEffort(3600, 0, 0, RiderProfile{}, effort); !near(got, 50) {
			t.Errorf("effort %d = %v, want the flat 50: not a rating", effort, got)
		}
	}
}

// Power without an FTP, or heart rate without a max, is not a measurement
// TrainingLoad could use, so it is the fallback and effort applies.
func TestAnUnusableMeasurementIsTheFallback(t *testing.T) {
	if got := TrainingLoadWithEffort(3600, 200, 0, RiderProfile{}, 5); !near(got, 100) {
		t.Errorf("power but no FTP, effort 5 = %v, want 100", got)
	}
	if HasMeasuredLoad(200, 0, RiderProfile{}) || HasMeasuredLoad(0, 150, RiderProfile{}) {
		t.Error("an unusable reading counted as measured")
	}
	if !HasMeasuredLoad(200, 0, RiderProfile{FTPWatts: 250}) || !HasMeasuredLoad(0, 150, RiderProfile{MaxHR: 190}) {
		t.Error("a usable reading did not count as measured")
	}
}
