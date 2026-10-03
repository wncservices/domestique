package loops_test

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/loops"
	"github.com/wncservices/domestique/apps/api/internal/routing"
)

// squareLoop is a synthetic square of about perimeterM around an invented
// start, so a fake engine can return a loop of a chosen length. A square has
// no backtracking (opposite sides are far apart).
func squareLoop(perimeterM float64) routing.Path {
	const lat0, lon0 = 50.0, 4.0
	side := perimeterM / 4
	dLat := side / 111320
	dLon := side / (111320 * math.Cos(lat0*math.Pi/180))
	pts := []gpx.Point{
		{Lat: lat0, Lon: lon0},
		{Lat: lat0 + dLat, Lon: lon0},
		{Lat: lat0 + dLat, Lon: lon0 + dLon},
		{Lat: lat0, Lon: lon0 + dLon},
		{Lat: lat0, Lon: lon0},
	}
	return routing.Path{Points: pts}
}

// spurLoop goes out along one road and straight back: all backtrack.
func spurLoop(lengthM float64) routing.Path {
	const lat0, lon0 = 50.0, 4.0
	n := 40
	var pts []gpx.Point
	for i := 0; i <= n; i++ {
		pts = append(pts, gpx.Point{Lat: lat0 + float64(i)*lengthM/2/float64(n)/111320, Lon: lon0})
	}
	for i := n - 1; i >= 0; i-- {
		pts = append(pts, gpx.Point{Lat: lat0 + float64(i)*lengthM/2/float64(n)/111320, Lon: lon0 + 0.00002})
	}
	return routing.Path{Points: pts}
}

type call struct {
	lengthM   float64
	seed      int
	profile   string
	hilliness int
}

type fakeEngine struct {
	mu      sync.Mutex
	calls   []call
	overrun float64 // returned length / requested length
	fail    func(seed int) error
	path    func(lengthM float64, seed int) routing.Path
}

func (f *fakeEngine) Route(context.Context, []routing.LatLng, string) (routing.Path, error) {
	return routing.Path{}, errors.New("not used")
}

func (f *fakeEngine) RoundTrip(ctx context.Context, _ routing.LatLng, lengthM float64, seed int, profile string, hilliness int) (routing.Path, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{lengthM, seed, profile, hilliness})
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return routing.Path{}, err
	}
	if f.fail != nil {
		if err := f.fail(seed); err != nil {
			return routing.Path{}, err
		}
	}
	if f.path != nil {
		return f.path(lengthM*f.overrun, seed), nil
	}
	return squareLoop(lengthM * f.overrun), nil
}

// distanceObjective is the suggest handler's shape: keep within 10 percent,
// rank by hilliness. It is local so these tests pin the generator, not the
// api package's wrapper.
type distanceObjective struct{ target float64 }

func (o distanceObjective) TargetLength() float64 { return o.target }
func (o distanceObjective) Refine(r1 []loops.Loop) float64 {
	if ratio := loops.OvershootRatio(r1, o.target); ratio > 0 {
		return o.target / ratio
	}
	return o.target
}
func (o distanceObjective) Keep(l loops.Loop) bool {
	return math.Abs(l.DistanceM-o.target) <= o.target*0.10
}
func (o distanceObjective) Rank(s []loops.Loop) []loops.Loop { return loops.ByHilliness(s, 0, 20) }

func request(obj loops.Objective) loops.Request {
	return loops.Request{
		Start:            routing.LatLng{Lat: 50, Lon: 4},
		Profile:          "cycling-road",
		Hilliness:        2,
		SeedBase:         100,
		CalibrationSeeds: 5,
		RefinementSeeds:  10,
		Objective:        obj,
	}
}

func TestGenerateSpendsBothRoundsWithSequentialSeeds(t *testing.T) {
	eng := &fakeEngine{overrun: 1.0}
	_, stats := loops.Generate(context.Background(), eng, request(distanceObjective{target: 40000}))
	if stats.Attempts != 15 || len(eng.calls) != 15 {
		t.Fatalf("attempts = %d, calls = %d, want 15 and 15", stats.Attempts, len(eng.calls))
	}
	var seeds []int
	for _, c := range eng.calls {
		seeds = append(seeds, c.seed)
		if c.profile != "cycling-road" || c.hilliness != 2 {
			t.Errorf("call asked for profile %q hilliness %d, want the request's own", c.profile, c.hilliness)
		}
	}
	sort.Ints(seeds)
	for i, s := range seeds {
		if s != 101+i {
			t.Fatalf("seeds = %v, want 101..115 in sequence", seeds)
		}
	}
}

// The calibration round asks for the raw target; the refinement round asks
// for the target divided by the overshoot the calibration round measured.
func TestGenerateRefinesTheLengthByTheMeasuredOvershoot(t *testing.T) {
	eng := &fakeEngine{overrun: 1.25}
	loops.Generate(context.Background(), eng, request(distanceObjective{target: 40000}))
	raw, refined := 0, 0
	for _, c := range eng.calls {
		switch {
		case math.Abs(c.lengthM-40000) < 1:
			raw++
		case math.Abs(c.lengthM-32000) < 200:
			refined++
		}
	}
	if raw != 5 || refined != 10 {
		t.Errorf("%d calls at the raw length and %d at the corrected one, want 5 and 10 (calls: %+v)", raw, refined, eng.calls)
	}
}

func TestGenerateReportsEveryFailureAndTheLastError(t *testing.T) {
	eng := &fakeEngine{fail: func(int) error { return errors.New("no route") }}
	got, stats := loops.Generate(context.Background(), eng, request(distanceObjective{target: 20000}))
	if len(got) != 0 {
		t.Errorf("got %d loops from a dead engine, want 0", len(got))
	}
	if len(stats.Failures) != 15 || stats.LastErr == nil {
		t.Errorf("failures = %d, lastErr = %v, want 15 and an error", len(stats.Failures), stats.LastErr)
	}
}

func TestGenerateAllWithinNothingSurvivingIsNotAnEngineFailure(t *testing.T) {
	eng := &fakeEngine{overrun: 1.0}
	eng.path = func(float64, int) routing.Path { return squareLoop(50000) } // never near the 20 km asked for
	got, stats := loops.Generate(context.Background(), eng, request(distanceObjective{target: 20000}))
	if len(got) != 0 || stats.LastErr != nil {
		t.Errorf("got %d loops, lastErr %v, want none and no error", len(got), stats.LastErr)
	}
}

func TestGenerateStopsWhenTheRequestIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	eng := &fakeEngine{overrun: 1.0}
	got, stats := loops.Generate(ctx, eng, request(distanceObjective{target: 20000}))
	if len(got) != 0 || stats.LastErr == nil {
		t.Errorf("got %d loops, lastErr %v from a cancelled context, want none and the context error", len(got), stats.LastErr)
	}
}

func TestShortlistPrefersLowBacktrackButNeverEmptiesTheList(t *testing.T) {
	good := loops.FromPath(1, squareLoop(20000))
	spur := loops.FromPath(2, spurLoop(20000))
	obj := distanceObjective{target: good.DistanceM}

	got := loops.Shortlist(obj, []loops.Loop{spur, good})
	if len(got) != 1 || got[0].Seed != 1 {
		t.Fatalf("shortlist = %+v, want only the real loop", got)
	}

	obj = distanceObjective{target: spur.DistanceM}
	got = loops.Shortlist(obj, []loops.Loop{spur})
	if len(got) != 1 {
		t.Fatalf("shortlist of only a spur = %+v, want it kept rather than nothing", got)
	}
}

func TestOvershootRatioIgnoresEmptyAndGuardsTheTarget(t *testing.T) {
	if got := loops.OvershootRatio(nil, 1000); got != 0 {
		t.Errorf("ratio of nothing = %v, want 0", got)
	}
	l := loops.FromPath(1, squareLoop(1000))
	if got := loops.OvershootRatio([]loops.Loop{l}, 0); got != 0 {
		t.Errorf("ratio at zero target = %v, want 0", got)
	}
	if got := loops.OvershootRatio([]loops.Loop{l}, 500); math.Abs(got-2) > 0.05 {
		t.Errorf("ratio = %v, want about 2", got)
	}
}

func TestBacktrackFractionSeparatesASpurFromALoop(t *testing.T) {
	spur := loops.FromPath(1, spurLoop(20000))
	loop := loops.FromPath(2, squareLoop(20000))
	if got := loops.BacktrackFraction(spur.Coords); got < 0.5 {
		t.Errorf("spur backtrack = %v, want at least 0.5", got)
	}
	if got := loops.BacktrackFraction(loop.Coords); got != 0 {
		t.Errorf("square backtrack = %v, want 0", got)
	}
}

func TestByHillinessKeepsTheRightEnd(t *testing.T) {
	mk := func(ascentPerKm float64) loops.Loop { return loops.Loop{AscentPerKm: ascentPerKm} }
	pool := []loops.Loop{mk(11), mk(1), mk(7), mk(2), mk(9), mk(4)}
	flat := loops.ByHilliness(pool, 0, 3)
	hilly := loops.ByHilliness(pool, routing.MaxSteepnessDifficulty, 3)
	for _, l := range flat {
		if l.AscentPerKm > 4 {
			t.Errorf("flat kept %v, want one of the three lowest", l.AscentPerKm)
		}
	}
	for _, l := range hilly {
		if l.AscentPerKm < 7 {
			t.Errorf("hilly kept %v, want one of the three highest", l.AscentPerKm)
		}
	}
}
