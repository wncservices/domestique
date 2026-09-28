package thresholds

import (
	"reflect"
	"testing"
	"time"
)

// today is a fixed clock with an explicit zone, per this task's testing
// rule. Its Y/M/D (2026-06-15, a Monday) drives every window in these
// tests; dateOnly reads only the date, so the process's own TZ environment
// variable cannot change what these tests see.
var today = time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)

func daysAgo(n int) string {
	return today.AddDate(0, 0, -n).Format("2006-01-02")
}

func cyclingRide(id string, daysAgo int, curve map[int]float64) Ride {
	return Ride{SessionID: id, Date: today.AddDate(0, 0, -daysAgo).Format("2006-01-02"), Sport: "cycling", PowerCurve: curve}
}

func runningRide(id string, daysAgo int, best1200, best1800 float64) Ride {
	return Ride{SessionID: id, Date: today.AddDate(0, 0, -daysAgo).Format("2006-01-02"), Sport: "running", BestSpeed1200: best1200, BestSpeed1800: best1800}
}

func hrRide(id string, daysAgo int, sport string, maxHR int) Ride {
	return Ride{SessionID: id, Date: today.AddDate(0, 0, -daysAgo).Format("2006-01-02"), Sport: sport, MaxHR: maxHR}
}

func findField(t *testing.T, findings []Finding, field string) (Finding, bool) {
	t.Helper()
	for _, f := range findings {
		if f.Field == field {
			return f, true
		}
	}
	return Finding{}, false
}

// --- FTP: source formulas ---

func TestFTPUpFrom20MinuteEffort(t *testing.T) {
	rides := []Ride{cyclingRide("s1", 2, map[int]float64{1200: 282})}
	p := Profile{FTPWatts: 255}
	got := Detect(rides, p, today)

	f, ok := findField(t, got, "ftp")
	if !ok {
		t.Fatal("want an ftp finding")
	}
	if f.Value != 268 { // 0.95 * 282 = 267.9 -> 268
		t.Errorf("value = %v, want 268", f.Value)
	}
	if f.Previous != 255 {
		t.Errorf("previous = %v, want 255", f.Previous)
	}
	if f.Direction != "up" {
		t.Errorf("direction = %q, want up", f.Direction)
	}
	if f.SourceSessionID != "s1" || f.SourceDate != daysAgo(2) {
		t.Errorf("source = %q/%q, want s1/%s", f.SourceSessionID, f.SourceDate, daysAgo(2))
	}
	wantWeekday := today.AddDate(0, 0, -2).Weekday().String()
	if want := "from " + wantWeekday + "'s 20-minute effort (282 W)"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
	if f.Auto {
		t.Error("auto = true for a rider-typed FTP, want false")
	}
}

func TestFTPUpFrom60MinuteEffort(t *testing.T) {
	rides := []Ride{cyclingRide("s1", 1, map[int]float64{3600: 220})}
	p := Profile{FTPWatts: 200}
	got := Detect(rides, p, today)

	f, ok := findField(t, got, "ftp")
	if !ok {
		t.Fatal("want an ftp finding")
	}
	if f.Value != 220 {
		t.Errorf("value = %v, want 220", f.Value)
	}
	wantWeekday := today.AddDate(0, 0, -1).Weekday().String()
	if want := "from " + wantWeekday + "'s 60-minute effort (220 W)"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
}

func TestFTPUpFromCriticalPowerWhenItWins(t *testing.T) {
	// P1200=300, P300=340: CP = (300*1200 - 340*300)/900 = 286.67, which
	// beats 0.95*300=285 (20-minute branch) and there is no 60-minute best.
	rides := []Ride{cyclingRide("s1", 3, map[int]float64{300: 340, 1200: 300})}
	p := Profile{FTPWatts: 250}
	got := Detect(rides, p, today)

	f, ok := findField(t, got, "ftp")
	if !ok {
		t.Fatal("want an ftp finding")
	}
	if f.Value != 287 { // round(286.67)
		t.Errorf("value = %v, want 287", f.Value)
	}
	wantWeekday := today.AddDate(0, 0, -3).Weekday().String()
	if want := "from " + wantWeekday + "'s 5- and 20-minute efforts (287 W)"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
}

func TestFTPCriticalPowerRequiresP300StrictlyAboveP1200(t *testing.T) {
	// P300 == P1200: CP must not be used (spec: "only ... when P300 > P1200").
	// If CP were used anyway it would equal 300, beating the 20-minute
	// branch's 285 and changing both the value and the reason.
	rides := []Ride{cyclingRide("s1", 1, map[int]float64{300: 300, 1200: 300})}
	p := Profile{FTPWatts: 250}
	got := Detect(rides, p, today)

	f, ok := findField(t, got, "ftp")
	if !ok {
		t.Fatal("want an ftp finding")
	}
	if f.Value != 285 {
		t.Errorf("value = %v, want 285 (CP must not win on P300 == P1200)", f.Value)
	}
	wantWeekday := today.AddDate(0, 0, -1).Weekday().String()
	if want := "from " + wantWeekday + "'s 20-minute effort (300 W)"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
}

func TestFTPIgnoresRideWithNoQualifyingBest(t *testing.T) {
	// Only a 5-minute best: neither 20 nor 60 minute, so no eFTP at all.
	rides := []Ride{cyclingRide("s1", 1, map[int]float64{300: 400})}
	p := Profile{FTPWatts: 250}
	got := Detect(rides, p, today)
	if _, ok := findField(t, got, "ftp"); ok {
		t.Error("want no ftp finding from a ride with only a 5-minute best")
	}
}

func TestFTPIgnoresNonCyclingRides(t *testing.T) {
	rides := []Ride{
		{SessionID: "s1", Date: daysAgo(1), Sport: "running", PowerCurve: map[int]float64{1200: 400}},
	}
	p := Profile{FTPWatts: 250}
	got := Detect(rides, p, today)
	if _, ok := findField(t, got, "ftp"); ok {
		t.Error("want no ftp finding from a running ride's power curve")
	}
}

// --- FTP: up threshold boundary (exactly 3%) ---

func TestFTPUpThresholdExactBoundaryFires(t *testing.T) {
	// current 200, estimate exactly 206 (200 * 1.03) via a 60-minute best.
	rides := []Ride{cyclingRide("s1", 1, map[int]float64{3600: 206})}
	p := Profile{FTPWatts: 200}
	got := Detect(rides, p, today)
	if _, ok := findField(t, got, "ftp"); !ok {
		t.Error("estimate exactly at current*1.03 should fire (>=), want a finding")
	}
}

func TestFTPUpThresholdJustBelowBoundaryDoesNotFire(t *testing.T) {
	rides := []Ride{cyclingRide("s1", 1, map[int]float64{3600: 205.9})}
	p := Profile{FTPWatts: 200}
	got := Detect(rides, p, today)
	if f, ok := findField(t, got, "ftp"); ok {
		t.Errorf("estimate just below current*1.03 should not fire, got %+v", f)
	}
}

func TestFTPUpWithEmptyCurrentValue(t *testing.T) {
	rides := []Ride{cyclingRide("s1", 1, map[int]float64{3600: 150})}
	p := Profile{FTPWatts: 0}
	got := Detect(rides, p, today)
	f, ok := findField(t, got, "ftp")
	if !ok {
		t.Fatal("want an ftp finding when current is empty and any estimate exists")
	}
	if f.Previous != 0 {
		t.Errorf("previous = %v, want 0", f.Previous)
	}
	if !f.Auto {
		t.Error("auto = false for an empty field, want true")
	}
}

// --- FTP: auto vs suggestion ---

func TestFTPAutoWhenFieldIsEstimated(t *testing.T) {
	rides := []Ride{cyclingRide("s1", 1, map[int]float64{3600: 220})}
	p := Profile{FTPWatts: 200, FTPEstimated: true}
	got := Detect(rides, p, today)
	f, ok := findField(t, got, "ftp")
	if !ok {
		t.Fatal("want an ftp finding")
	}
	if !f.Auto {
		t.Error("auto = false for an estimated field with an up move, want true")
	}
}

func TestFTPNotAutoWhenRiderTyped(t *testing.T) {
	rides := []Ride{cyclingRide("s1", 1, map[int]float64{3600: 220})}
	p := Profile{FTPWatts: 200, FTPEstimated: false}
	got := Detect(rides, p, today)
	f, ok := findField(t, got, "ftp")
	if !ok {
		t.Fatal("want an ftp finding")
	}
	if f.Auto {
		t.Error("auto = true for a rider-typed field, want false")
	}
}

// --- FTP: 42-day detection window ---

func TestFTPWindowIncludesExactly42Days(t *testing.T) {
	// Day 41 ago is the oldest day still inside the 42-day window
	// (today plus the 41 days before it).
	rides := []Ride{cyclingRide("s1", 41, map[int]float64{3600: 220})}
	p := Profile{FTPWatts: 200}
	got := Detect(rides, p, today)
	if _, ok := findField(t, got, "ftp"); !ok {
		t.Error("a ride 41 days ago is inside the 42-day window, want a finding")
	}
}

func TestFTPWindowExcludesDay42(t *testing.T) {
	rides := []Ride{cyclingRide("s1", 42, map[int]float64{3600: 220})}
	p := Profile{FTPWatts: 200}
	got := Detect(rides, p, today)
	if _, ok := findField(t, got, "ftp"); ok {
		t.Error("a ride 42 days ago is outside the 42-day window, want no finding")
	}
}

// --- FTP: down direction ---

func TestFTPDownRequires90DayHistory(t *testing.T) {
	// No eFTP anywhere near current, but no ride reaches back 90 days:
	// not enough history to say the silence means a real decline.
	rides := []Ride{cyclingRide("s1", 10, map[int]float64{3600: 100})}
	p := Profile{FTPWatts: 255}
	got := Detect(rides, p, today)
	if f, ok := findField(t, got, "ftp"); ok {
		t.Errorf("want no ftp finding without 90 days of history, got %+v", f)
	}
}

func TestFTPDownWhenNothingRecentReachesCurrent(t *testing.T) {
	rides := []Ride{
		cyclingRide("old", 100, map[int]float64{3600: 260}), // establishes 90-day history
		cyclingRide("recent", 30, map[int]float64{3600: 230}),
	}
	p := Profile{FTPWatts: 255} // 0.95*255 = 242.25; 230 doesn't reach it
	got := Detect(rides, p, today)
	f, ok := findField(t, got, "ftp")
	if !ok {
		t.Fatal("want a down ftp finding")
	}
	if f.Direction != "down" {
		t.Errorf("direction = %q, want down", f.Direction)
	}
	if f.Value != 230 {
		t.Errorf("value = %v, want 230 (the best recent, even though short)", f.Value)
	}
	if f.Auto {
		t.Error("auto must always be false for down, got true")
	}
	if want := "no effort near 255 W in the last 90 days"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
	if f.SourceSessionID != "recent" || f.SourceDate != daysAgo(30) {
		t.Errorf("source = %q/%q, want recent/%s (the ride that produced the recent estimate)", f.SourceSessionID, f.SourceDate, daysAgo(30))
	}
}

func TestFTPDownAutoFalseEvenWhenEstimated(t *testing.T) {
	rides := []Ride{
		cyclingRide("old", 100, map[int]float64{3600: 260}),
		cyclingRide("recent", 30, map[int]float64{3600: 230}),
	}
	p := Profile{FTPWatts: 255, FTPEstimated: true}
	got := Detect(rides, p, today)
	f, ok := findField(t, got, "ftp")
	if !ok {
		t.Fatal("want a down ftp finding")
	}
	if f.Auto {
		t.Error("down must never auto-apply, even for an estimated field")
	}
}

func TestFTPDownWithNoRecentRidesAtAllProducesNoFinding(t *testing.T) {
	// History reaches back 90 days, but nothing at all in the last 90 days
	// is not evidence of a decline — just a gap. Must not suggest 0 W.
	rides := []Ride{cyclingRide("old", 100, map[int]float64{3600: 260})}
	p := Profile{FTPWatts: 255}
	got := Detect(rides, p, today)
	if f, ok := findField(t, got, "ftp"); ok {
		t.Errorf("want no ftp finding with no qualifying recent effort, got %+v", f)
	}
}

func TestFTPDownBoundaryExactly95PercentDoesNotFire(t *testing.T) {
	rides := []Ride{
		cyclingRide("old", 100, map[int]float64{3600: 260}),
		cyclingRide("recent", 30, map[int]float64{3600: 242.25}), // exactly 0.95*255
	}
	p := Profile{FTPWatts: 255}
	got := Detect(rides, p, today)
	if f, ok := findField(t, got, "ftp"); ok {
		t.Errorf("a recent estimate exactly at 95%% reaches current, want no down finding, got %+v", f)
	}
}

func TestFTPDownNeverFiresWithoutACurrentValue(t *testing.T) {
	rides := []Ride{cyclingRide("old", 100, map[int]float64{3600: 100})}
	p := Profile{FTPWatts: 0}
	got := Detect(rides, p, today)
	if f, ok := findField(t, got, "ftp"); ok {
		t.Errorf("want no down finding with nothing to compare against, got %+v", f)
	}
}

// --- Max HR ---

func TestMaxHRUpBoundary(t *testing.T) {
	p := Profile{MaxHR: 190}

	below := Detect([]Ride{hrRide("s1", 1, "cycling", 190)}, p, today)
	if f, ok := findField(t, below, "max_hr"); ok {
		t.Errorf("peak equal to current should not fire, got %+v", f)
	}

	at := Detect([]Ride{hrRide("s1", 1, "cycling", 191)}, p, today)
	f, ok := findField(t, at, "max_hr")
	if !ok {
		t.Fatal("peak == current+1 should fire")
	}
	if f.Value != 191 || f.Previous != 190 || f.Direction != "up" {
		t.Errorf("finding = %+v, want value 191, previous 190, direction up", f)
	}
	wantWeekday := today.AddDate(0, 0, -1).Weekday().String()
	if want := "peak heart rate 191 on " + wantWeekday + "'s ride"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
}

func TestMaxHRFromAnySport(t *testing.T) {
	p := Profile{MaxHR: 180}
	got := Detect([]Ride{hrRide("s1", 1, "running", 185)}, p, today)
	if _, ok := findField(t, got, "max_hr"); !ok {
		t.Error("want max HR counted from a running ride")
	}
}

func TestMaxHRIgnoresSensorSpikeAbove230(t *testing.T) {
	p := Profile{MaxHR: 180}
	got := Detect([]Ride{hrRide("s1", 1, "cycling", 245)}, p, today)
	if f, ok := findField(t, got, "max_hr"); ok {
		t.Errorf("a >230bpm reading is a sensor spike, want it ignored, got %+v", f)
	}
}

func TestMaxHREmptyCurrentWithAnyPeak(t *testing.T) {
	p := Profile{MaxHR: 0}
	got := Detect([]Ride{hrRide("s1", 1, "cycling", 150)}, p, today)
	f, ok := findField(t, got, "max_hr")
	if !ok {
		t.Fatal("want a finding for any peak against an empty current")
	}
	if f.Previous != 0 || !f.Auto {
		t.Errorf("finding = %+v, want previous 0 and auto true", f)
	}
}

func TestMaxHRNeverDown(t *testing.T) {
	p := Profile{MaxHR: 190}
	got := Detect([]Ride{hrRide("s1", 1, "cycling", 150)}, p, today)
	if f, ok := findField(t, got, "max_hr"); ok {
		t.Errorf("max HR must never suggest a down move, got %+v", f)
	}
}

// --- Threshold pace ---

func TestPaceUpFrom30MinuteBest(t *testing.T) {
	// current 300 sec/km -> speed 1000/300 = 3.3333 m/s; up needs speed
	// >= 3.4333. best1800 = 3.5 clears it.
	p := Profile{ThresholdPaceSecPerKM: 300}
	got := Detect([]Ride{runningRide("s1", 2, 0, 3.5)}, p, today)
	f, ok := findField(t, got, "threshold_pace")
	if !ok {
		t.Fatal("want a threshold_pace finding")
	}
	wantPace := roundInt(1000 / 3.5) // 285.71 -> 286
	if f.Value != wantPace {
		t.Errorf("value = %v, want %v", f.Value, wantPace)
	}
	wantWeekday := today.AddDate(0, 0, -2).Weekday().String()
	if want := "30-minute best on " + wantWeekday + "'s run"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
	if f.Direction != "up" {
		t.Errorf("direction = %q, want up", f.Direction)
	}
}

func TestPaceUpFrom20MinuteFallback(t *testing.T) {
	p := Profile{ThresholdPaceSecPerKM: 300}
	got := Detect([]Ride{runningRide("s1", 1, 3.6, 0)}, p, today) // 0.97*3.6=3.492 >= 3.4333
	f, ok := findField(t, got, "threshold_pace")
	if !ok {
		t.Fatal("want a threshold_pace finding from the 20-minute fallback")
	}
	wantWeekday := today.AddDate(0, 0, -1).Weekday().String()
	if want := "20-minute best on " + wantWeekday + "'s run"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
}

func TestPacePrefers30MinuteOver20Minute(t *testing.T) {
	p := Profile{ThresholdPaceSecPerKM: 300}
	got := Detect([]Ride{runningRide("s1", 1, 10, 3.5)}, p, today) // huge 20-min speed, real 30-min present
	f, ok := findField(t, got, "threshold_pace")
	if !ok {
		t.Fatal("want a finding")
	}
	if f.Reason == "" || f.Value != roundInt(1000/3.5) {
		t.Errorf("want the 30-minute best used when present, got %+v", f)
	}
}

func TestPaceIgnoresNonRunningRides(t *testing.T) {
	p := Profile{ThresholdPaceSecPerKM: 300}
	rides := []Ride{{SessionID: "s1", Date: daysAgo(1), Sport: "cycling", BestSpeed1800: 10}}
	got := Detect(rides, p, today)
	if _, ok := findField(t, got, "threshold_pace"); ok {
		t.Error("want no pace finding from a cycling ride's speed fields")
	}
}

func TestPaceUpThresholdBoundary(t *testing.T) {
	// current speed 1000/300 = 3.33333; boundary speed = *1.03 = 3.433333...
	// Computed with the same runtime float64 chain currentPaceSpeed/detectPace
	// use (division then multiplication as separate steps), not a compile-time
	// constant expression, which Go evaluates at higher precision and would
	// not bit-for-bit match the runtime result.
	p := Profile{ThresholdPaceSecPerKM: 300}
	currentSpeed := 1000.0 / 300.0
	atBoundary := currentSpeed * 1.03
	got := Detect([]Ride{runningRide("s1", 1, 0, atBoundary)}, p, today)
	if _, ok := findField(t, got, "threshold_pace"); !ok {
		t.Error("estimate exactly at the 3% boundary should fire")
	}

	below := Detect([]Ride{runningRide("s1", 1, 0, atBoundary-0.001)}, p, today)
	if f, ok := findField(t, below, "threshold_pace"); ok {
		t.Errorf("estimate just below the 3%% boundary should not fire, got %+v", f)
	}
}

func TestPaceUpWithEmptyCurrentValue(t *testing.T) {
	p := Profile{ThresholdPaceSecPerKM: 0}
	got := Detect([]Ride{runningRide("s1", 1, 0, 3.0)}, p, today)
	f, ok := findField(t, got, "threshold_pace")
	if !ok {
		t.Fatal("want a finding for any estimate against an empty current pace")
	}
	if f.Previous != 0 || !f.Auto {
		t.Errorf("finding = %+v, want previous 0 and auto true", f)
	}
}

func TestPaceAutoVsSuggestion(t *testing.T) {
	estimated := Detect([]Ride{runningRide("s1", 1, 0, 3.5)}, Profile{ThresholdPaceSecPerKM: 300, PaceEstimated: true}, today)
	if f, ok := findField(t, estimated, "threshold_pace"); !ok || !f.Auto {
		t.Errorf("want auto true for an estimated field, got %+v", f)
	}

	typedIn := Detect([]Ride{runningRide("s1", 1, 0, 3.5)}, Profile{ThresholdPaceSecPerKM: 300, PaceEstimated: false}, today)
	if f, ok := findField(t, typedIn, "threshold_pace"); !ok || f.Auto {
		t.Errorf("want auto false for a rider-typed field, got %+v", f)
	}
}

func TestPaceDownRequires90DayHistory(t *testing.T) {
	got := Detect([]Ride{runningRide("s1", 10, 0, 1.0)}, Profile{ThresholdPaceSecPerKM: 300}, today)
	if f, ok := findField(t, got, "threshold_pace"); ok {
		t.Errorf("want no down finding without 90 days of history, got %+v", f)
	}
}

func TestPaceDownWhenNothingRecentReachesCurrent(t *testing.T) {
	rides := []Ride{
		runningRide("old", 100, 0, 4.0),   // establishes history
		runningRide("recent", 30, 0, 3.0), // slower than 0.95*3.3333=3.1667
	}
	p := Profile{ThresholdPaceSecPerKM: 300}
	got := Detect(rides, p, today)
	f, ok := findField(t, got, "threshold_pace")
	if !ok {
		t.Fatal("want a down threshold_pace finding")
	}
	if f.Direction != "down" {
		t.Errorf("direction = %q, want down", f.Direction)
	}
	if f.Auto {
		t.Error("auto must be false for down")
	}
	// current pace 300 sec/km renders as "5:00" -- reason must show m:ss/km,
	// never raw seconds.
	if want := "no run near 5:00 /km in the last 90 days"; f.Reason != want {
		t.Errorf("reason = %q, want %q", f.Reason, want)
	}
	wantValue := roundInt(1000 / 3.0)
	if f.Value != wantValue {
		t.Errorf("value = %v, want %v", f.Value, wantValue)
	}
	if f.SourceSessionID != "recent" || f.SourceDate != daysAgo(30) {
		t.Errorf("source = %q/%q, want recent/%s (the ride that produced the recent estimate)", f.SourceSessionID, f.SourceDate, daysAgo(30))
	}
}

func TestPaceDownWithNoRecentRunsAtAllProducesNoFinding(t *testing.T) {
	// As with FTP: history reaches back 90 days, but nothing at all in the
	// last 90 days is not evidence of a decline. Must not suggest 0.
	rides := []Ride{runningRide("old", 100, 0, 4.0)}
	p := Profile{ThresholdPaceSecPerKM: 300}
	got := Detect(rides, p, today)
	if f, ok := findField(t, got, "threshold_pace"); ok {
		t.Errorf("want no threshold_pace finding with no qualifying recent run, got %+v", f)
	}
}

func TestFormatPaceMinSecRoundsWithCorrectCarry(t *testing.T) {
	// 239.6 rounds to 240 seconds, which must carry into a whole minute
	// ("4:00"), not render as "3:60".
	if got, want := formatPaceMinSec(239.6), "4:00"; got != want {
		t.Errorf("formatPaceMinSec(239.6) = %q, want %q", got, want)
	}
	if got, want := formatPaceMinSec(250), "4:10"; got != want {
		t.Errorf("formatPaceMinSec(250) = %q, want %q", got, want)
	}
	if got, want := formatPaceMinSec(65), "1:05"; got != want {
		t.Errorf("formatPaceMinSec(65) = %q, want %q", got, want)
	}
}

// --- Determinism and ordering ---

func TestDetectReturnsFieldsInFixedOrder(t *testing.T) {
	rides := []Ride{
		runningRide("r1", 1, 0, 5.0),
		hrRide("h1", 1, "cycling", 999), // will be ignored (spike), no finding
		cyclingRide("c1", 1, map[int]float64{3600: 500}),
		hrRide("h2", 1, "running", 200),
	}
	p := Profile{FTPWatts: 100, MaxHR: 100, ThresholdPaceSecPerKM: 400}
	got := Detect(rides, p, today)

	var fields []string
	for _, f := range got {
		fields = append(fields, f.Field)
	}
	want := []string{"ftp", "max_hr", "threshold_pace"}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("field order = %v, want %v", fields, want)
	}
}

func TestDetectReturnsNoFindingsWhenNothingQualifies(t *testing.T) {
	got := Detect(nil, Profile{FTPWatts: 200, MaxHR: 180, ThresholdPaceSecPerKM: 300}, today)
	if len(got) != 0 {
		t.Errorf("want no findings from no rides, got %+v", got)
	}
}

// --- Timezone robustness ---

func TestDetectionWindowIsStableAcrossZones(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	brussels := time.Date(2026, 6, 15, 23, 30, 0, 0, loc)
	rides := []Ride{cyclingRide("s1", 41, map[int]float64{3600: 220})}
	p := Profile{FTPWatts: 200}

	gotUTC := Detect(rides, p, today)
	gotBrussels := Detect(rides, p, brussels)

	fUTC, okUTC := findField(t, gotUTC, "ftp")
	fBrussels, okBrussels := findField(t, gotBrussels, "ftp")
	if !okUTC || !okBrussels {
		t.Fatalf("want findings in both zones, utc ok=%v brussels ok=%v", okUTC, okBrussels)
	}
	if fUTC.Value != fBrussels.Value {
		t.Errorf("value differs by zone: utc=%v brussels=%v", fUTC.Value, fBrussels.Value)
	}
}
