// Package loops generates round-trip loop candidates from one start point
// through the routing engine and shortlists them against an objective.
//
// It was lifted out of the route builder's suggest handler so a second
// caller (a loop sized for a planned ride, see the time objective) shares
// the same generation and the same filters instead of growing a copy. The
// behaviour of the suggest handler is unchanged: two rounds (a calibration
// round at the raw length to measure the engine's systematic overshoot, a
// refinement round at a corrected length), a distance filter, a backtrack
// filter, then a ranking the objective owns.
package loops

import (
	"context"
	"math"
	"math/rand/v2"
	"sort"
	"sync"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/routing"
)

// Loop is one successful RoundTrip result with the numbers every objective
// needs, derived once.
type Loop struct {
	// Seed is the seed that produced the loop. Callers that wrap their own
	// pool in Loops may reuse it as an index.
	Seed   int
	Path   routing.Path
	Coords [][2]float64
	// DistanceM and AscentM use the same derivation as the route builder's
	// own preview, so a suggestion and a candidate never disagree.
	DistanceM   float64
	AscentM     float64
	AscentPerKm float64
}

// FromPath derives a Loop's numbers from a routing path.
func FromPath(seed int, path routing.Path) Loop {
	coords := make([][2]float64, len(path.Points))
	var distance float64
	for i, p := range path.Points {
		coords[i] = [2]float64{p.Lat, p.Lon}
		if i > 0 {
			distance += gpx.DistanceM(path.Points[i-1], p)
		}
	}
	ascent := gpx.ComputeStats(path.Points).AscentM
	return Loop{
		Seed:        seed,
		Path:        path,
		Coords:      coords,
		DistanceM:   distance,
		AscentM:     ascent,
		AscentPerKm: AscentPerKm(ascent, distance),
	}
}

// Failure is one seed the engine could not route.
type Failure struct {
	Seed int
	Err  error
}

// Stats says how a Generate call went, so the caller can log it the way its
// own endpoint should: a partial failure is a Warn, an empty result an Error.
type Stats struct {
	Attempts int
	Failures []Failure
	// LastErr is the last failure in seed order, nil when every seed routed.
	LastErr error
}

// Objective is what a caller wants from the generator.
type Objective interface {
	// TargetLength is the raw length asked of the engine in the calibration
	// round, in metres.
	TargetLength() float64
	// Refine returns the length to ask for in the refinement round, given
	// the calibration loops. Returning TargetLength means "no correction".
	Refine(round1 []Loop) float64
	// Keep drops a loop that misses the objective outright.
	Keep(Loop) bool
	// Rank orders (and may truncate) what survived Keep and the backtrack
	// filter. It takes the whole shortlist rather than scoring one loop at a
	// time because some rankings are relative to the pool (the hilliness
	// "moderate" case picks whatever sits closest to the pool's median).
	Rank(shortlist []Loop) []Loop
}

// Request is one generation: where from, how, and against what.
type Request struct {
	Start     routing.LatLng
	Profile   string
	Hilliness int
	// SeedBase is the base of the seed sequence; attempts use SeedBase+1,
	// SeedBase+2, and so on, so one request gets distinct shapes without any
	// collision checking. See NewSeedBase.
	SeedBase int
	// CalibrationSeeds and RefinementSeeds are the sizes of the two rounds;
	// together they are the most engine calls the request can spend.
	CalibrationSeeds int
	RefinementSeeds  int
	Objective        Objective
}

// NewSeedBase returns a fresh random base for one request — not a fixed
// value, so asking again with the same start and length shows genuinely
// different loops (the engine's own algorithm is deterministic per seed).
func NewSeedBase() int {
	// #nosec G404 -- picking which loop *shape* a rider sees, not a secret
	// or anything an attacker gains from predicting; math/rand/v2 is the
	// right tool for cosmetic variety, crypto/rand's cost buys nothing here.
	return rand.IntN(1_000_000)
}

// Generate runs both rounds and returns the objective's ranking of what
// survived. Calls within a round run concurrently, all with ctx, so each is
// a child span of the caller's request and a cancelled request stops them.
// An empty result is not an error: Stats says whether the engine failed
// (LastErr) or nothing landed within the objective.
func Generate(ctx context.Context, client routing.Client, req Request) ([]Loop, Stats) {
	obj := req.Objective
	target := obj.TargetLength()

	calibration := make([]int, req.CalibrationSeeds)
	for i := range calibration {
		calibration[i] = req.SeedBase + i + 1
	}
	round1 := fireRound(ctx, client, req.Start, target, calibration, req.Profile, req.Hilliness)

	refined := obj.Refine(successes(round1))
	refinement := make([]int, req.RefinementSeeds)
	for i := range refinement {
		refinement[i] = req.SeedBase + req.CalibrationSeeds + i + 1
	}
	round2 := fireRound(ctx, client, req.Start, refined, refinement, req.Profile, req.Hilliness)

	all := append(round1, round2...)
	stats := Stats{Attempts: len(all)}
	for _, a := range all {
		if a.err != nil {
			stats.Failures = append(stats.Failures, Failure{Seed: a.seed, Err: a.err})
			stats.LastErr = a.err
		}
	}
	return Shortlist(obj, successes(all)), stats
}

// Shortlist applies the objective's Keep, then the backtrack preference,
// then its Rank.
func Shortlist(obj Objective, pool []Loop) []Loop {
	shortlist := make([]Loop, 0, len(pool))
	for _, l := range pool {
		if obj.Keep(l) {
			shortlist = append(shortlist, l)
		}
	}
	// A candidate that mostly rides itself twice — out along a road, back
	// the same way — is a valid loop by distance and the engine's own path
	// cost, but a bad suggestion: nobody wants to hear the same road's
	// traffic twice. Preferred whenever any low-backtrack candidate exists;
	// if every survivor backtracks (a sparse road network near the start
	// point, most likely), the shortlist is left alone rather than handing
	// back nothing — fewer honestly close beats padding the list.
	if low := FilterLowBacktrack(shortlist); len(low) > 0 {
		shortlist = low
	}
	return obj.Rank(shortlist)
}

type attempt struct {
	seed int
	path routing.Path
	err  error
}

func successes(attempts []attempt) []Loop {
	var out []Loop
	for _, a := range attempts {
		if a.err == nil {
			out = append(out, FromPath(a.seed, a.path))
		}
	}
	return out
}

// fireRound issues one RoundTrip call per seed, all at the same requested
// length, concurrently: bounds the round's wall-clock time by its single
// slowest call rather than their sum.
func fireRound(ctx context.Context, client routing.Client, start routing.LatLng, requestedLengthM float64, seeds []int, profile string, hilliness int) []attempt {
	results := make([]attempt, len(seeds))
	var wg sync.WaitGroup
	for i, seed := range seeds {
		wg.Add(1)
		go func(i, seed int) {
			defer wg.Done()
			path, err := client.RoundTrip(ctx, start, requestedLengthM, seed, profile, hilliness)
			results[i] = attempt{seed: seed, path: path, err: err}
		}(i, seed)
	}
	wg.Wait()
	return results
}

// OvershootRatio is the empirical actual-distance/requested-distance ratio
// across a round's loops. The engine's round_trip systematically overshoots
// a requested length rather than missing it randomly in both directions
// (confirmed live: a 60km request averaged +13% over 15 seeds, every single
// one over, never under), and that overshoot scales with distance in a way
// no single fixed constant captures (a separate live sample at 15km
// averaged +33%), hence measuring it fresh per request. Returns 0
// ("nothing to correct by") when requestedLengthM isn't positive or no loop
// has a positive distance.
func OvershootRatio(loops []Loop, requestedLengthM float64) float64 {
	if requestedLengthM <= 0 {
		return 0
	}
	var sum float64
	var n int
	for _, l := range loops {
		if l.DistanceM <= 0 {
			continue
		}
		sum += l.DistanceM / requestedLengthM
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// AscentPerKm is the one number a hilliness preference can actually be
// judged against — see ByHilliness. Guards a zero distance (a stray 0/0
// reads as "flattest possible" otherwise, which would bias selection toward
// a result that was never really measured).
func AscentPerKm(ascentM, distanceM float64) float64 {
	if distanceM <= 0 {
		return 0
	}
	return ascentM / (distanceM / 1000)
}

// Median returns the median of vals; a plain sort is plenty over the
// handful of loops one request generates.
func Median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// ByHilliness picks the n loops from pool that best fit hilliness, rather
// than just however many of the first few seeds happened to succeed.
//
// Found live: the engine's own steepness_difficulty weighting does bias
// climbing rate the right way on average, but round_trip's own loop length
// varies by up to 50% from the requested distance independently of that
// weighting. That variance swamps the signal in *total* ascent: a "Flat"
// request that lands a longer loop can easily show more total climbing than
// a "Hilly" one that lands a shorter one, even though the Hilly one climbs
// faster the whole way. Ranking the whole pool by ascent-per-km and keeping
// the best-fitting n is what makes "Flat" read as flatter than "Hilly".
//
// hilliness is a resolved 0-3 value: 0-1 favours the lowest climbing rates
// in the pool, 2-3 the highest, and exactly routing.DefaultSteepnessDifficulty
// picks whichever are closest to the pool's own median.
func ByHilliness(pool []Loop, hilliness, n int) []Loop {
	sorted := append([]Loop(nil), pool...)
	switch {
	case hilliness <= 0:
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].AscentPerKm < sorted[j].AscentPerKm })
	case hilliness >= routing.MaxSteepnessDifficulty-1:
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].AscentPerKm > sorted[j].AscentPerKm })
	default:
		rates := make([]float64, len(sorted))
		for i, e := range sorted {
			rates[i] = e.AscentPerKm
		}
		median := Median(rates)
		sort.Slice(sorted, func(i, j int) bool {
			return math.Abs(sorted[i].AscentPerKm-median) < math.Abs(sorted[j].AscentPerKm-median)
		})
	}
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

// FilterLowBacktrack keeps only the loops whose BacktrackFraction is at or
// under MaxBacktrackFraction — split out so the "if nothing survives, don't
// empty the shortlist" fallback in Shortlist reads as one decision.
//
// Each loop's fraction is independent, CPU-only work over a full route
// geometry — found live to cost tens to a few hundred milliseconds per
// candidate on a realistic ORS response (a few thousand points). A
// sequential loop paid that once per shortlisted candidate, stacking on top
// of the network latency, so it is fanned out the same way the network
// calls are: bounded by the slowest single candidate rather than the sum.
func FilterLowBacktrack(pool []Loop) []Loop {
	fractions := make([]float64, len(pool))
	var wg sync.WaitGroup
	for i, e := range pool {
		wg.Add(1)
		go func(i int, points [][2]float64) {
			defer wg.Done()
			fractions[i] = BacktrackFraction(points)
		}(i, e.Coords)
	}
	wg.Wait()

	kept := make([]Loop, 0, len(pool))
	for i, e := range pool {
		if fractions[i] <= MaxBacktrackFraction {
			kept = append(kept, e)
		}
	}
	return kept
}

// MaxBacktrackFraction is how much of a loop's own distance may retrace a
// street it already rode, in the opposite direction, before
// FilterLowBacktrack drops it — round_trip has no lever to ask ORS for a
// loop that doesn't do this (routing.go's own package doc explains why:
// there's no avoid_features or profile option that targets "don't reuse a
// road," only the steps/fords avoidance every request already sets), so
// this is a post-hoc filter over what came back rather than something the
// routing-engine request itself can prevent. 0.15: a loop that's more than
// an eighth-to-a-sixth "there and back" reads as a real out-and-back to a
// rider looking at the map, not an incidental few dozen metres of overlap
// near a junction.
const MaxBacktrackFraction = 0.15

// backtrackMatchDistanceM is how close two segments of a route's own path
// must sit before they count as "the same street" rather than two
// different, merely nearby ones — loose enough to absorb GPS/geometry
// noise between two decodes of the same road (ORS doesn't return
// byte-identical vertices for the same tarmac ridden twice), tight enough
// to stay well under the smallest realistic separation between two
// distinct parallel streets in a dense grid.
const backtrackMatchDistanceM = 15.0

// backtrackMinArcGapM is how far apart along the route's own cumulative
// distance two segments must be before they're even compared — an
// index-based gap would scale wrong across a sparse vs. GPS-dense
// geometry; an arc-length one doesn't. Without this, a tight hairpin (a
// real, single visit to one physical curve) would flag itself: two
// samples a few metres apart along a sharp bend can easily point close to
// opposite directions despite being the same curve, not a return visit.
const backtrackMinArcGapM = 150.0

// backtrackOppositeDotThreshold bounds how close to exactly opposite two
// segments' directions must point to count as retracing rather than
// merely converging — a normalized 2D dot product, so -1 is exactly
// opposite and 0 is perpendicular. -0.7 corresponds to roughly 135°+ of
// difference (allowing up to ~45° of noise from a straight reversal),
// loose enough that a real road's own gentle curvature along the "out" and
// "back" legs doesn't slip under a stricter threshold and go undetected.
const backtrackOppositeDotThreshold = -0.7

// BacktrackFraction estimates how much of a route's own distance is spent
// retracing a street it already rode, in the opposite direction — the
// geometric signature of an out-and-back spur, as opposed to a loop that
// merely passes near itself once where it closes back at the start. See
// the three backtrack* constants above for what "close" and "opposite"
// mean here, and Shortlist for how the result is used.
//
// O(n²) in the number of points — n is a single round-trip loop's own
// geometry, which is not the few hundred vertices this was first written
// against: ORS's directions response is unsimplified, so a realistic
// 60-100km loop comes back with several thousand points, and at that size
// this is measured in the tens to hundreds of milliseconds, not trivial
// once it's paid once per shortlisted candidate. See FilterLowBacktrack's
// own comment for why that caller fans this out across candidates rather
// than running it in a sequential loop.
func BacktrackFraction(points [][2]float64) float64 {
	n := len(points)
	if n < 4 {
		return 0
	}

	segLen := make([]float64, n-1)
	cum := make([]float64, n)
	dir := make([][2]float64, n-1)
	for i := 0; i < n-1; i++ {
		a := gpx.Point{Lat: points[i][0], Lon: points[i][1]}
		b := gpx.Point{Lat: points[i+1][0], Lon: points[i+1][1]}
		segLen[i] = gpx.DistanceM(a, b)
		cum[i+1] = cum[i] + segLen[i]
		dir[i] = segmentDirection(points[i], points[i+1])
	}
	total := cum[n-1]
	if total <= 0 {
		return 0
	}

	flagged := make([]bool, n-1)
	for i := 0; i < n-1; i++ {
		if dir[i] == ([2]float64{}) {
			continue // zero-length segment (a duplicate vertex) — no direction to compare
		}
		for j := i + 1; j < n-1; j++ {
			if cum[j]-cum[i+1] < backtrackMinArcGapM {
				continue
			}
			if dir[j] == ([2]float64{}) {
				continue
			}
			if dot2(dir[i], dir[j]) > backtrackOppositeDotThreshold {
				continue
			}
			mi := midpoint(points[i], points[i+1])
			mj := midpoint(points[j], points[j+1])
			if gpx.DistanceM(gpx.Point{Lat: mi[0], Lon: mi[1]}, gpx.Point{Lat: mj[0], Lon: mj[1]}) <= backtrackMatchDistanceM {
				flagged[i] = true
				flagged[j] = true
			}
		}
	}

	var backtrack float64
	for i, f := range flagged {
		if f {
			backtrack += segLen[i]
		}
	}
	return backtrack / total
}

// segmentDirection is a's-to-b's normalized direction, in a local
// equirectangular approximation (longitude scaled by cos(latitude)) rather
// than true lat/lon degrees — needed so a dot product between two
// segments actually reflects their real-world angle instead of being
// skewed by longitude lines converging toward the poles. Returns the zero
// vector for a zero-length segment (two identical points): BacktrackFraction
// treats that as "no direction to compare" rather than an arbitrary one.
func segmentDirection(a, b [2]float64) [2]float64 {
	latMid := (a[0] + b[0]) / 2 * math.Pi / 180
	dLat := b[0] - a[0]
	dLon := (b[1] - a[1]) * math.Cos(latMid)
	length := math.Hypot(dLat, dLon)
	if length == 0 {
		return [2]float64{}
	}
	return [2]float64{dLat / length, dLon / length}
}

func dot2(a, b [2]float64) float64 { return a[0]*b[0] + a[1]*b[1] }

func midpoint(a, b [2]float64) [2]float64 {
	return [2]float64{(a[0] + b[0]) / 2, (a[1] + b[1]) / 2}
}
