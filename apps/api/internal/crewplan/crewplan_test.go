package crewplan

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

func stats(km, ascentM float64) model.RouteStats {
	return model.RouteStats{DistanceM: km * 1000, AscentM: ascentM}
}

func TestEstimateIsDistanceOverSpeedPlusAscent(t *testing.T) {
	// 100 km at 26 km/h plus half an hour per 1000 m: about 4.35 h.
	e := EstimateRoute(stats(100, 1000))
	want := 100.0/26 + 0.5
	if math.Abs(e.Hours-want) > 1e-9 {
		t.Errorf("hours = %v, want %v", e.Hours, want)
	}
	if e.Km != 100 || e.AscentM != 1000 {
		t.Errorf("km/ascent = %v/%v, want 100/1000", e.Km, e.AscentM)
	}
}

func TestEstimateTSSUsesAFixedIntensityFactorAndNeedsNoFTP(t *testing.T) {
	e := EstimateRoute(stats(100, 1000))
	want := e.Hours * 0.65 * 0.65 * 100
	if math.Abs(e.TSS-want) > 1e-9 {
		t.Errorf("TSS = %v, want hours x 0.65^2 x 100 = %v", e.TSS, want)
	}
}

func TestEstimateIsCappedAtEightHours(t *testing.T) {
	e := EstimateRoute(stats(400, 6000))
	if e.Hours != 8 {
		t.Errorf("hours = %v, want the 8 h cap", e.Hours)
	}
	if math.Abs(e.TSS-8*0.4225*100) > 1e-9 {
		t.Errorf("TSS = %v, want it priced on the capped duration", e.TSS)
	}
}

func TestKindThresholdsAtTheMinuteBoundaries(t *testing.T) {
	for _, tc := range []struct {
		minutes float64
		want    Kind
	}{
		{59, Short}, {60, Endurance}, {119, Endurance}, {120, Long}, {0, Short}, {300, Long},
	} {
		if got := KindForSeconds(tc.minutes * 60); got != tc.want {
			t.Errorf("%v minutes = %q, want %q", tc.minutes, got, tc.want)
		}
	}
}

func TestEstimateKindFollowsTheEstimatedDuration(t *testing.T) {
	if k := EstimateRoute(stats(100, 1000)).Kind; k != Long {
		t.Errorf("100 km/1000 m = %q, want long", k)
	}
	if k := EstimateRoute(stats(35, 0)).Kind; k != Endurance { // 1.35 h
		t.Errorf("35 km flat = %q, want endurance", k)
	}
	if k := EstimateRoute(stats(10, 0)).Kind; k != Short {
		t.Errorf("10 km flat = %q, want short", k)
	}
}

func TestSecondsRoundsTheEstimateToAWholeSecond(t *testing.T) {
	e := EstimateRoute(stats(100, 1000))
	if e.Seconds() != math.Round(e.Hours*3600) {
		t.Errorf("seconds = %v", e.Seconds())
	}
}

func TestTSSForSecondsMatchesTheEstimate(t *testing.T) {
	e := EstimateRoute(stats(100, 1000))
	if math.Abs(TSSForSeconds(e.Seconds())-e.TSS) > 0.01 {
		t.Errorf("TSSForSeconds = %v, estimate TSS = %v", TSSForSeconds(e.Seconds()), e.TSS)
	}
}
