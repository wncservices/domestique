package routefit_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/pacing"
	"github.com/wncservices/domestique/apps/api/internal/routefit"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// leg is a stretch of invented road: a length and a constant grade.
type leg struct{ lengthM, grade float64 }

// track lays legs end to end due north from an invented start, a point every
// 50 m, each carrying elevation. Synthetic: no real route.
func track(legs ...leg) []gpx.Point {
	const step = 50.0
	const metresPerDegree = 6371000 * math.Pi / 180
	pts := []gpx.Point{{Lat: 50, Lon: 4, Ele: 100, HasEle: true}}
	for _, l := range legs {
		n := int(math.Round(l.lengthM / step))
		for i := 0; i < n; i++ {
			last := pts[len(pts)-1]
			pts = append(pts, gpx.Point{
				Lat:    last.Lat + step/metresPerDegree,
				Lon:    4,
				Ele:    last.Ele + step*l.grade/100,
				HasEle: true,
			})
		}
	}
	return pts
}

// flatKm is n km of flat road.
func flatKm(n float64) leg { return leg{n * 1000, 0} }

// withAscentPerKm is lengthKm of road that climbs mPerKm metres per km, as a
// steady gentle grade (so no single climb stands out).
func withAscentPerKm(lengthKm, mPerKm float64) []gpx.Point {
	return track(leg{lengthKm * 1000, mPerKm / 10})
}

func ascent(points []gpx.Point) float64 { return gpx.ComputeStats(points).AscentM }

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v (+/- %v)", what, got, want, tol)
	}
}

func timed(name string, intensity workout.Intensity, sec, lo, hi float64) workout.WorkoutStep {
	s := workout.WorkoutStep{Name: name, Intensity: intensity, Duration: workout.DurationTime, Seconds: sec, Target: workout.TargetOpen}
	if lo > 0 {
		s.Target, s.TargetLow, s.TargetHigh = workout.TargetPower, lo, hi
	}
	return s
}

func session(zone workout.Zone, name string, steps ...workout.WorkoutStep) workout.Workout {
	return workout.Workout{Sport: model.SportCycling, Name: name, Zone: zone, Steps: steps}
}

func enduranceOf(minutes float64) workout.Workout {
	return session(workout.ZoneEndurance, "Endurance ride", timed("Ride", workout.IntensityActive, minutes*60, 0, 0))
}

var rider = routefit.Rider{FTP: 250, WeightKg: 75}

// AverageFraction ---------------------------------------------------------

func TestAverageFractionWeightsStepsByTimeAndReadsPowerMidpoints(t *testing.T) {
	steps := []workout.WorkoutStep{
		timed("Warm up", workout.IntensityWarmup, 600, 0, 0),                               // default 0.50
		timed("Work", workout.IntensityActive, 1200, 200, 220),                             // midpoint 210 W = 0.84
		timed("Ride", workout.IntensityActive, 600, 0, 0),                                  // default 0.65
		timed("Cool down", workout.IntensityCooldown, 600, 0, 0),                           // default 0.50
		{Name: "Open", Intensity: workout.IntensityActive, Duration: workout.DurationOpen}, // no seconds: not counted
	}
	want := (600*0.50 + 1200*0.84 + 600*0.65 + 600*0.50) / 3000
	near(t, "fraction", routefit.AverageFraction(steps, 250), want, 1e-9)
}

func TestAverageFractionMultipliesOutRepeatBlocks(t *testing.T) {
	steps := []workout.WorkoutStep{{
		Name: "Intervals", Repeat: 3,
		Steps: []workout.WorkoutStep{
			timed("On", workout.IntensityActive, 300, 250, 250),
			timed("Off", workout.IntensityRecovery, 300, 0, 0),
		},
	}}
	near(t, "fraction", routefit.AverageFraction(steps, 250), (1.0+0.50)/2, 1e-9)
}

func TestAverageFractionWithoutFTPTreatsPowerTargetsAsUntargeted(t *testing.T) {
	steps := []workout.WorkoutStep{timed("Work", workout.IntensityActive, 600, 200, 220)}
	near(t, "fraction", routefit.AverageFraction(steps, 0), 0.65, 1e-9)
}

func TestAverageFractionOfNothingTimedIsZero(t *testing.T) {
	if got := routefit.AverageFraction(nil, 250); got != 0 {
		t.Errorf("fraction of no steps = %v, want 0", got)
	}
}

// FlatSpeed and EstimateSeconds --------------------------------------------

func TestFlatSpeedUsesThePhysicsAtTheRidersPower(t *testing.T) {
	got, assumed := routefit.FlatSpeed(rider, 0.65)
	want := pacing.DefaultPhysics(75).Speed(250*0.65, 0)
	near(t, "speed", got, want, 1e-9)
	if assumed {
		t.Error("speed flagged as assumed with FTP and weight both known")
	}
}

func TestFlatSpeedFlagsAnAssumedWeight(t *testing.T) {
	_, assumed := routefit.FlatSpeed(routefit.Rider{FTP: 250}, 0.65)
	if !assumed {
		t.Error("speed not flagged as assumed with no weight entered")
	}
}

func TestFlatSpeedWithoutFTPIs25KphAndAssumed(t *testing.T) {
	got, assumed := routefit.FlatSpeed(routefit.Rider{WeightKg: 70}, 0.65)
	near(t, "speed", got, 25/3.6, 1e-9)
	if !assumed {
		t.Error("the no-FTP fallback must be flagged as assumed")
	}
}

func TestEstimateSecondsHillyIsStrictlySlowerThanFlatAtEqualLength(t *testing.T) {
	flat := track(flatKm(30))
	hilly := track(leg{5000, 0}, leg{2000, 5}, leg{2000, -5}, leg{5000, 0}, leg{2000, 5}, leg{2000, -5}, leg{5000, 0}, leg{2000, 5}, leg{2000, -5}, leg{5000, 0})
	flatSec, _ := routefit.EstimateSeconds(flat, rider, 0.65)
	hillySec, _ := routefit.EstimateSeconds(hilly, rider, 0.65)
	if hillySec <= flatSec {
		t.Errorf("hilly = %v s, flat = %v s: a hilly loop must take longer than a flat one of equal length", hillySec, flatSec)
	}
	// Sanity on the flat number: 30 km at about 28 km/h.
	if flatSec < 3000 || flatSec > 5000 {
		t.Errorf("flat 30 km = %v s, want roughly an hour and a bit", flatSec)
	}
}

func TestEstimateSecondsWithoutFTPIsDistanceAtTwentyFiveKphPlusAscent(t *testing.T) {
	flat := track(flatKm(30))
	sec, assumed := routefit.EstimateSeconds(flat, routefit.Rider{}, 0.65)
	near(t, "flat seconds", sec, 30000/(25/3.6), 30000/(25/3.6)*0.005)
	if !assumed {
		t.Error("no-FTP estimate not flagged as assumed")
	}

	hilly := track(leg{15000, 2}) // 300 m of ascent over 15 km
	sec, _ = routefit.EstimateSeconds(hilly, routefit.Rider{}, 0.65)
	want := 15000/(25/3.6) + 1.4*ascent(hilly)
	near(t, "hilly seconds", sec, want, want*0.001)
}

func TestEstimateSecondsWithoutElevationFallsBackToFlatSpeed(t *testing.T) {
	pts := track(flatKm(20))
	for i := range pts {
		pts[i].HasEle = false
	}
	sec, _ := routefit.EstimateSeconds(pts, rider, 0.65)
	v, _ := routefit.FlatSpeed(rider, 0.65)
	near(t, "seconds", sec, 20000/v, 20000/v*0.005)
}

func TestEstimateSecondsOfNothingIsZero(t *testing.T) {
	if sec, _ := routefit.EstimateSeconds(nil, rider, 0.65); sec != 0 {
		t.Errorf("seconds of no points = %v, want 0", sec)
	}
}

// FamilyOf ------------------------------------------------------------------

func TestFamilyOfEachRowOfTheTable(t *testing.T) {
	long := session(workout.ZoneEndurance, scheduler.ClimbingLongRideName, timed("Ride", workout.IntensityActive, 60*60, 0, 0))
	cases := []struct {
		name string
		wk   workout.Workout
		want routefit.Family
	}{
		{"endurance under 75 min is recovery", enduranceOf(74), routefit.FamilyRecovery},
		{"endurance at 75 min", enduranceOf(75), routefit.FamilyEndurance},
		{"endurance at 150 min", enduranceOf(150), routefit.FamilyEndurance},
		{"endurance over 150 min", enduranceOf(151), routefit.FamilyLong},
		{"the climbing long ride by name", long, routefit.FamilyLong},
		{"tempo", session(workout.ZoneTempo, "Tempo"), routefit.FamilySteady},
		{"sweet spot", session(workout.ZoneSweetSpot, "Sweet spot"), routefit.FamilySteady},
		{"threshold", session(workout.ZoneThreshold, "Threshold"), routefit.FamilySteady},
		{"vo2max", session(workout.ZoneVO2Max, "VO2max"), routefit.FamilyClimb},
		{"anaerobic", session(workout.ZoneAnaerobic, "Anaerobic"), routefit.FamilyClimb},
		{"intervals", session(workout.ZoneIntervals, "Intervals"), routefit.FamilyClimb},
		{"unknown zone with no length is endurance", session("", "Ride"), routefit.FamilyEndurance},
	}
	for _, c := range cases {
		if got := routefit.FamilyOf(c.wk); got != c.want {
			t.Errorf("%s: family = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestEveryFamilyHasAProfileAndHilliness(t *testing.T) {
	want := map[routefit.Family]struct {
		profile   string
		hilliness int
	}{
		routefit.FamilyRecovery:  {"cycling-regular", 0},
		routefit.FamilyEndurance: {"cycling-road", 1},
		routefit.FamilyLong:      {"cycling-road", 3},
		routefit.FamilySteady:    {"cycling-road", 1},
		routefit.FamilyClimb:     {"cycling-road", 3},
	}
	for f, w := range want {
		profile, hilliness := f.Engine()
		if profile != w.profile || hilliness != w.hilliness {
			t.Errorf("%s engine = (%s, %d), want (%s, %d)", f, profile, hilliness, w.profile, w.hilliness)
		}
	}
}

// Fit -----------------------------------------------------------------------

func fit(t *testing.T, wk workout.Workout, pts []gpx.Point) (float64, string) {
	t.Helper()
	return routefit.Fit(routefit.FamilyOf(wk), wk, pts, rider)
}

func TestFitRecoveryWantsFlatRoads(t *testing.T) {
	wk := enduranceOf(60)
	for _, tc := range []struct {
		mPerKm, want float64
	}{{0, 1}, {3, 1}, {4, 1}, {8, 0.5}, {12, 0}, {20, 0}} {
		pts := withAscentPerKm(20, tc.mPerKm)
		if got := ascent(pts) / 20; math.Abs(got-tc.mPerKm) > 0.2 {
			t.Fatalf("fixture climbs %v m/km, want %v", got, tc.mPerKm)
		}
		score, _ := fit(t, wk, pts)
		near(t, fmt.Sprintf("recovery fit at %v m/km", tc.mPerKm), score, tc.want, 0.05)
	}
}

func TestFitRecoveryCostsThirtyPercentPerKilometreClimb(t *testing.T) {
	pts := track(flatKm(10), leg{1200, 4}, leg{1200, -4}, flatKm(10)) // one 1.2 km climb at 4 %
	score, note := fit(t, enduranceOf(60), pts)
	near(t, "fit", score, 1-0.3, 0.08) // ascent 48 m over 22 km is 2.2 m/km: no taper cost
	if note == "" {
		t.Error("no note for a recovery ride past a real climb")
	}
}

func TestFitEnduranceWantsFiveToTenMetresPerKilometre(t *testing.T) {
	wk := enduranceOf(120)
	for _, tc := range []struct {
		mPerKm, want float64
	}{{5, 1}, {7, 1}, {10, 1}, {1, 0.5}, {14, 0.5}, {18, 0}, {30, 0}} {
		score, _ := fit(t, wk, withAscentPerKm(40, tc.mPerKm))
		near(t, fmt.Sprintf("endurance fit at %v m/km", tc.mPerKm), score, tc.want, 0.1)
	}
}

func TestFitLongWantsTenMetresPerKilometreOrMore(t *testing.T) {
	wk := enduranceOf(200)
	for _, tc := range []struct {
		mPerKm, want float64
	}{{10, 1}, {15, 1}, {5, 0.5}, {0, 0}} {
		score, _ := fit(t, wk, withAscentPerKm(60, tc.mPerKm))
		near(t, fmt.Sprintf("long fit at %v m/km", tc.mPerKm), score, tc.want, 0.06)
	}
}

func steadyWk() workout.Workout {
	return session(workout.ZoneThreshold, "Threshold",
		timed("Warm up", workout.IntensityWarmup, 600, 0, 0),
		workout.WorkoutStep{Name: "Threshold", Repeat: 2, Steps: []workout.WorkoutStep{
			timed("On", workout.IntensityActive, 1200, 250, 250),
			timed("Off", workout.IntensityRecovery, 300, 0, 0),
		}},
	)
}

func TestFitSteadyNeedsARunOfRoadAsLongAsTheWorkStep(t *testing.T) {
	// 20 minutes at 250 W covers well over 10 km: flat 30 km has the run.
	score, _ := fit(t, steadyWk(), track(flatKm(30)))
	near(t, "steady fit on a flat 30 km", score, 1, 1e-9)

	// A sawtooth that never sits within 2 % grade for long has no run and no
	// climb of 1 km, so the session has nowhere to be done.
	var legs []leg
	for i := 0; i < 6; i++ {
		legs = append(legs, leg{600, 4}, leg{600, -4})
	}
	score, note := fit(t, steadyWk(), track(legs...))
	if score >= 0.5 {
		t.Errorf("steady fit on a sawtooth = %v, want below 0.5", score)
	}
	if !strings.Contains(note, "20-minute") {
		t.Errorf("note = %q, want it to name the 20-minute effort", note)
	}

	// A run of about half the needed distance is a partial fit.
	short := track(leg{4000, 4}, leg{4000, -4}, flatKm(5), leg{4000, 4}, leg{4000, -4})
	score, _ = fit(t, steadyWk(), short)
	if score <= 0.2 || score >= 0.9 {
		t.Errorf("steady fit with a 5 km run = %v, want strictly between none and a full fit", score)
	}
}

func TestFitSteadyAcceptsOneClimbLastingEightyPercentOfTheStep(t *testing.T) {
	// A 6 km climb at 5 % takes over 20 minutes at 250 W; no flat run at all.
	pts := track(leg{6000, 5}, leg{6000, -5})
	score, _ := fit(t, steadyWk(), pts)
	near(t, "steady fit with one long climb", score, 1, 1e-9)
}

func climbWk() workout.Workout {
	return session(workout.ZoneVO2Max, "VO2max",
		workout.WorkoutStep{Name: "VO2", Repeat: 5, Steps: []workout.WorkoutStep{
			timed("On", workout.IntensityActive, 300, 300, 300),
			timed("Off", workout.IntensityRecovery, 300, 0, 0),
		}},
	)
}

func TestFitClimbWantsAClimbAtLeast80PercentOfTheWorkStep(t *testing.T) {
	// A 1.5 km climb at 6 % holds a 5-minute effort at 300 W.
	score, _ := fit(t, climbWk(), track(flatKm(5), leg{1500, 6}, leg{1500, -6}, flatKm(5)))
	near(t, "climb fit with a long enough climb", score, 1, 1e-9)

	// The same 1.1 km at 5 % against an 8-minute effort at 300 W is only a
	// partial fit: the climb lasts about four minutes.
	long := session(workout.ZoneVO2Max, "VO2max", timed("On", workout.IntensityActive, 480, 300, 300))
	score, note := fit(t, long, track(flatKm(5), leg{1100, 5}, leg{1100, -5}, flatKm(5)))
	if score <= 0.3 || score >= 0.9 {
		t.Errorf("climb fit with a short climb = %v, want partial", score)
	}
	if !strings.Contains(note, "8-minute") {
		t.Errorf("note = %q, want it to name the 8-minute effort", note)
	}
}

func TestFitClimbWithNoClimbHalvesTheRollingScore(t *testing.T) {
	// 10 m/km of gentle rolling road holds no climb of 1 km at 3 %: half.
	score, note := fit(t, climbWk(), withAscentPerKm(30, 10))
	near(t, "rolling", score, 0.5, 0.05)
	if !strings.Contains(note, "5-minute") {
		t.Errorf("note = %q, want it to name the 5-minute effort", note)
	}
	// Dead flat: nothing to climb and nothing rolling.
	score, _ = fit(t, climbWk(), track(flatKm(30)))
	near(t, "flat", score, 0, 1e-9)
}

func TestFitNeverClaimsPerfect(t *testing.T) {
	for _, wk := range []workout.Workout{enduranceOf(60), enduranceOf(120), enduranceOf(200), steadyWk(), climbWk()} {
		_, note := fit(t, wk, track(flatKm(30)))
		if strings.Contains(strings.ToLower(note), "perfect") {
			t.Errorf("note %q claims a perfect fit", note)
		}
	}
}

func TestFitWithoutElevationIsNeutralAndSaysSo(t *testing.T) {
	pts := track(flatKm(20))
	for i := range pts {
		pts[i].HasEle = false
	}
	score, note := fit(t, enduranceOf(120), pts)
	near(t, "fit", score, 0.5, 1e-9)
	if !strings.Contains(note, "elevation") {
		t.Errorf("note = %q, want it to say there was no elevation data", note)
	}
}
