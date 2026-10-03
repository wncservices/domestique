package loops_test

import (
	"context"
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/loops"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routefit"
	"github.com/wncservices/domestique/apps/api/internal/routing"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var timeRider = routefit.Rider{FTP: 250, WeightKg: 75}

// ride is a two-hour endurance session at a single easy step.
func ride(minutes float64) workout.Workout {
	return workout.Workout{
		Sport: model.SportCycling, Name: "Endurance ride", Zone: workout.ZoneEndurance,
		Steps: []workout.WorkoutStep{{
			Name: "Ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
			Seconds: minutes * 60, Target: workout.TargetOpen,
		}},
	}
}

// climbyLoop is a square of the given perimeter whose road climbs ascentM in
// total over its first half and descends over the second, so a loop of the
// same length can differ in how much it climbs. Points are 50 m apart.
func climbyLoop(perimeterM, ascentM float64) routing.Path {
	const lat0, lon0 = 50.0, 4.0
	side := perimeterM / 4
	dLat := side / 111320
	dLon := side / (111320 * math.Cos(lat0*math.Pi/180))
	corners := [][2]float64{{lat0, lon0}, {lat0 + dLat, lon0}, {lat0 + dLat, lon0 + dLon}, {lat0, lon0 + dLon}, {lat0, lon0}}
	var pts []gpx.Point
	n := int(side / 50)
	total := 4 * n
	for e := 0; e < 4; e++ {
		for i := 0; i < n; i++ {
			f := float64(i) / float64(n)
			k := e*n + i
			// A tent: up for the first half of the loop, down for the second.
			x := float64(k) / float64(total)
			ele := 100 + ascentM*(1-math.Abs(2*x-1))
			pts = append(pts, gpx.Point{
				Lat: corners[e][0] + f*(corners[e+1][0]-corners[e][0]),
				Lon: corners[e][1] + f*(corners[e+1][1]-corners[e][1]),
				Ele: ele, HasEle: true,
			})
		}
	}
	pts = append(pts, pts[0])
	return routing.Path{Points: pts}
}

func newTimeObjective(planned workout.Workout) *loops.TimeObjective {
	return loops.NewTimeObjective(planned, timeRider)
}

func TestTimeObjectiveFirstGuessIsFlatSpeedTimesPlannedSeconds(t *testing.T) {
	wk := ride(120)
	obj := newTimeObjective(wk)
	frac := routefit.AverageFraction(wk.Steps, timeRider.FTP)
	v, _ := routefit.FlatSpeed(timeRider, frac)
	if got, want := obj.TargetLength(), v*7200; math.Abs(got-want) > 1 {
		t.Errorf("target = %v m, want flat speed x planned seconds = %v", got, want)
	}
	if obj.PlannedSeconds() != 7200 {
		t.Errorf("planned = %v, want 7200", obj.PlannedSeconds())
	}
}

func TestTimeObjectiveRefinesByMeasuredSecondsPerMetreAndOvershoot(t *testing.T) {
	wk := ride(120)
	obj := newTimeObjective(wk)
	target := obj.TargetLength()
	// Round one: the engine returned loops 20 percent over what was asked.
	r1 := []loops.Loop{loops.FromPath(1, climbyLoop(target*1.2, 0)), loops.FromPath(2, climbyLoop(target*1.2, 0))}
	got := obj.Refine(r1)

	frac := routefit.AverageFraction(wk.Steps, timeRider.FTP)
	sec, _ := routefit.EstimateSeconds(r1[0].Path.Points, timeRider, frac)
	secPerM := sec / r1[0].DistanceM
	overshoot := r1[0].DistanceM / target
	want := 7200 / secPerM / overshoot
	if math.Abs(got-want) > want*0.005 {
		t.Errorf("refined = %v m, want planned / seconds-per-metre / overshoot = %v", got, want)
	}
	if got >= target {
		t.Errorf("refined = %v, want less than the %v asked first: the engine overshot", got, target)
	}
}

func TestTimeObjectiveRefineWithNothingMeasuredKeepsTheFirstGuess(t *testing.T) {
	obj := newTimeObjective(ride(120))
	if got := obj.Refine(nil); got != obj.TargetLength() {
		t.Errorf("refined = %v, want the first guess %v", got, obj.TargetLength())
	}
}

func TestTimeObjectiveKeepsLoopsWithinFifteenPercentOfPlannedTime(t *testing.T) {
	wk := ride(120)
	obj := newTimeObjective(wk)
	frac := routefit.AverageFraction(wk.Steps, timeRider.FTP)
	v, _ := routefit.FlatSpeed(timeRider, frac)
	flat := v * 7200 // a flat loop that takes about the planned time

	cases := []struct {
		name      string
		perimeter float64
		keep      bool
	}{
		{"on time", flat, true},
		{"14 percent long", flat * 1.14, true},
		{"14 percent short", flat * 0.86, true},
		{"20 percent long", flat * 1.20, false},
		{"20 percent short", flat * 0.80, false},
	}
	for _, c := range cases {
		l := loops.FromPath(1, climbyLoop(c.perimeter, 0))
		if got := obj.Keep(l); got != c.keep {
			t.Errorf("%s: Keep = %v, want %v", c.name, got, c.keep)
		}
	}
}

func TestTimeObjectiveCountsClimbingAgainstTheTime(t *testing.T) {
	obj := newTimeObjective(ride(120))
	frac := routefit.AverageFraction(obj.Workout().Steps, timeRider.FTP)
	v, _ := routefit.FlatSpeed(timeRider, frac)
	flat := v * 7200
	// The same length: dead flat takes the planned time, a hilly loop longer.
	if !obj.Keep(loops.FromPath(1, climbyLoop(flat, 0))) {
		t.Fatal("the flat loop of the planned length was dropped")
	}
	if obj.Keep(loops.FromPath(2, climbyLoop(flat, 2500))) {
		t.Error("a loop of the same length with 2500 m of climbing was kept: the extra time is not counted")
	}
}

func TestTimeObjectiveRanksByTimeFitAndTerrainFit(t *testing.T) {
	wk := ride(120) // endurance family: wants 5 to 10 m/km
	obj := newTimeObjective(wk)
	frac := routefit.AverageFraction(wk.Steps, timeRider.FTP)
	v, _ := routefit.FlatSpeed(timeRider, frac)
	flat := v * 7200

	onTimeFlat := loops.FromPath(1, climbyLoop(flat, 0))           // time fit about 1, terrain 0.375
	onTimeRolling := loops.FromPath(2, climbyLoop(flat*0.97, 600)) // rolling 600 m over ~70 km: ~8 m/km
	got := obj.Rank([]loops.Loop{onTimeFlat, onTimeRolling})
	if len(got) != 2 || got[0].Seed != 2 {
		t.Fatalf("rank = %+v, want the loop with the right terrain first", seeds(got))
	}
	d := obj.Detail(got[0])
	want := 0.5*d.TimeFit + 0.5*d.TerrainFit
	if math.Abs(d.Score-want) > 1e-9 {
		t.Errorf("score = %v, want 0.5 x timeFit + 0.5 x terrainFit = %v", d.Score, want)
	}
	if d.Family != routefit.FamilyEndurance {
		t.Errorf("family = %q, want endurance", d.Family)
	}
	if d.EstimatedSeconds <= 0 {
		t.Error("no estimated seconds on the detail")
	}
}

func seeds(ls []loops.Loop) []int {
	var out []int
	for _, l := range ls {
		out = append(out, l.Seed)
	}
	return out
}

func TestTimeObjectiveKeepsAtMostThreeThatDiffer(t *testing.T) {
	wk := ride(120)
	obj := newTimeObjective(wk)
	frac := routefit.AverageFraction(wk.Steps, timeRider.FTP)
	v, _ := routefit.FlatSpeed(timeRider, frac)
	flat := v * 7200

	// Five loops, the first three essentially the same shape (within 3 percent
	// of length and 10 percent of ascent), then two clearly different ones.
	pool := []loops.Loop{
		loops.FromPath(1, climbyLoop(flat*1.00, 500)),
		loops.FromPath(2, climbyLoop(flat*1.01, 510)),
		loops.FromPath(3, climbyLoop(flat*1.02, 520)),
		loops.FromPath(4, climbyLoop(flat*0.90, 800)),
		loops.FromPath(5, climbyLoop(flat*1.10, 300)),
	}
	got := obj.Rank(pool)
	if len(got) > 3 {
		t.Fatalf("kept %d, want at most 3", len(got))
	}
	for i := range got {
		for j := i + 1; j < len(got); j++ {
			a, b := got[i], got[j]
			sameLen := math.Abs(a.DistanceM-b.DistanceM) <= 0.03*a.DistanceM
			sameClimb := math.Abs(a.AscentM-b.AscentM) <= 0.10*a.AscentM
			if sameLen && sameClimb {
				t.Errorf("kept two near-identical loops %d and %d", a.Seed, b.Seed)
			}
		}
	}
	if len(got) != 3 {
		t.Errorf("kept %d, want 3: there are three distinct shapes", len(got))
	}
}

func TestTimeObjectiveThroughGenerateSpendsTenCalls(t *testing.T) {
	wk := ride(120)
	obj := newTimeObjective(wk)
	eng := &fakeEngine{overrun: 1.15}
	eng.path = func(lengthM float64, seed int) routing.Path { return climbyLoop(lengthM, float64(200+seed%7*60)) }
	_, stats := loops.Generate(context.Background(), eng, loops.Request{
		Start: routing.LatLng{Lat: 50, Lon: 4}, Profile: "cycling-road", Hilliness: 1, SeedBase: 10,
		CalibrationSeeds: loops.TimeCalibrationSeeds, RefinementSeeds: loops.TimeRefinementSeeds,
		Objective: obj,
	})
	if stats.Attempts != 10 || len(eng.calls) != 10 {
		t.Fatalf("attempts = %d, calls = %d, want 10: three calibration and seven refinement", stats.Attempts, len(eng.calls))
	}
	n3, nRefined := 0, 0
	for _, c := range eng.calls {
		if math.Abs(c.lengthM-obj.TargetLength()) < 1 {
			n3++
		} else {
			nRefined++
		}
	}
	if n3 != 3 || nRefined != 7 {
		t.Errorf("%d calls at the first-guess length and %d at the refined one, want 3 and 7", n3, nRefined)
	}
}
