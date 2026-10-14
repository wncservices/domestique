package bestefforts

import (
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func metric(t *testing.T, key string) Metric {
	t.Helper()
	for _, m := range Metrics() {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("no metric %q", key)
	return Metric{}
}

func TestMetricsAreTheNineTheSpecNames(t *testing.T) {
	var keys []string
	for _, m := range Metrics() {
		keys = append(keys, m.Key)
	}
	want := []string{"power_5", "power_60", "power_300", "power_480", "power_1200", "power_3600", "speed_1200", "speed_1800", "max_hr"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("metrics = %v, want %v", keys, want)
	}
}

func TestEffortsForACyclingRide(t *testing.T) {
	a := workout.SessionAnalysis{
		PowerCurve: map[string]float64{"5": 900, "60": 500, "300": 380, "480": 350, "1200": 300, "3600": 260},
		MaxHR:      181, BestSpeed1200: 3.2, BestSpeed1800: 3.1,
	}
	got := Efforts(a, "cycling")
	want := map[string]float64{
		"power_5": 900, "power_60": 500, "power_300": 380, "power_480": 350, "power_1200": 300, "power_3600": 260,
		"max_hr": 181,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cycling efforts = %v, want %v (speed is a running record only)", got, want)
	}
}

func TestEffortsForARunningRide(t *testing.T) {
	a := workout.SessionAnalysis{
		PowerCurve: map[string]float64{"300": 250}, MaxHR: 190, BestSpeed1200: 3.4, BestSpeed1800: 3.3,
	}
	got := Efforts(a, "running")
	want := map[string]float64{"speed_1200": 3.4, "speed_1800": 3.3, "max_hr": 190}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("running efforts = %v, want %v (a run has no power record)", got, want)
	}
}

func TestEffortsDropZeroAbsentAndUnknownWindows(t *testing.T) {
	a := workout.SessionAnalysis{PowerCurve: map[string]float64{"60": 0, "300": 310, "999": 400, "x": 5}}
	got := Efforts(a, "cycling")
	if !reflect.DeepEqual(got, map[string]float64{"power_300": 310}) {
		t.Errorf("efforts = %v, want only power_300", got)
	}
	if got := Efforts(workout.SessionAnalysis{}, "cycling"); len(got) != 0 {
		t.Errorf("an empty analysis gave %v", got)
	}
	if got := Efforts(workout.SessionAnalysis{PowerCurve: map[string]float64{"300": 310}}, "swimming"); len(got) != 0 {
		t.Errorf("an unknown sport gave %v, want nothing", got)
	}
}

func TestAwardNeedsOneWattMoreForPower(t *testing.T) {
	m := metric(t, "power_1200")
	prior := []float64{280, 290, 300, 295}
	if got := Award(m, 301, prior, prior); got != "all_time" {
		t.Errorf("exactly +1 W = %q, want all_time", got)
	}
	if got := Award(m, 300.9, prior, prior); got != "" {
		t.Errorf("0.9 W more = %q, want none", got)
	}
}

func TestAwardNeedsOnePercentForSpeedAndHeartRate(t *testing.T) {
	for _, key := range []string{"speed_1200", "speed_1800", "max_hr"} {
		m := metric(t, key)
		prior := []float64{100, 90, 95, 98}
		if got := Award(m, 101, prior, prior); got != "all_time" {
			t.Errorf("%s: exactly +1%% = %q, want all_time", key, got)
		}
		if got := Award(m, 100.99, prior, prior); got != "" {
			t.Errorf("%s: just under +1%% = %q, want none", key, got)
		}
	}
}

func TestAwardNeedsThreePriorValuesInScope(t *testing.T) {
	m := metric(t, "power_300")
	two := []float64{200, 210}
	if got := Award(m, 400, two, two); got != "" {
		t.Errorf("two prior values awarded %q, want none", got)
	}
	three := []float64{200, 210, 205}
	if got := Award(m, 400, three, three); got != "all_time" {
		t.Errorf("three prior values = %q, want all_time", got)
	}
	// The season has too few values, the history has enough: all-time does not
	// need the season to qualify.
	if got := Award(m, 400, two, three); got != "all_time" {
		t.Errorf("thin season, full history = %q, want all_time", got)
	}
	// A season of three that the ride beats, inside a history that is too thin
	// to count (not a real shape, since a season is a subset, but each scope is
	// judged on its own values).
	if got := Award(m, 400, three, two); got != "season" {
		t.Errorf("full season, thin history = %q, want season", got)
	}
}

func TestAwardAllTimeWinsOverSeason(t *testing.T) {
	m := metric(t, "power_300")
	season := []float64{250, 255, 260}
	allTime := []float64{250, 255, 260, 270}
	if got := Award(m, 280, season, allTime); got != "all_time" {
		t.Errorf("beats both = %q, want all_time", got)
	}
	if got := Award(m, 265, season, allTime); got != "season" {
		t.Errorf("beats the season but not the all-time = %q, want season", got)
	}
	if got := Award(m, 255, season, allTime); got != "" {
		t.Errorf("beats neither = %q", got)
	}
}

func TestAnEqualValueDoesNotAward(t *testing.T) {
	m := metric(t, "power_300")
	prior := []float64{250, 255, 260}
	if got := Award(m, 260, prior, prior); got != "" {
		t.Errorf("a tie = %q, want none: the earlier ride keeps the record", got)
	}
}

func TestAwardIgnoresScopeWithNoValues(t *testing.T) {
	m := metric(t, "max_hr")
	if got := Award(m, 190, nil, nil); got != "" {
		t.Errorf("no history = %q", got)
	}
}
