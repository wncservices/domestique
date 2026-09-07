package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/routing"
)

// Direct unit tests for selectByHilliness/ascentPerKm/medianOf — see
// TestRouteBuilderSuggestPicksBestFitByHilliness (routebuilder_test.go,
// package api_test) for the end-to-end version of the same behaviour
// through the real HTTP handler.

func poolEntry(ascentM float64) suggestPoolEntry {
	const distanceM = 10000 // fixed, so ascentPerKm ranks identically to raw ascentM
	return poolEntryDist(distanceM, ascentM)
}

func poolEntryDist(distanceM, ascentM float64) suggestPoolEntry {
	return suggestPoolEntry{
		candidate:   routeBuilderCandidate{AscentM: ascentM, DistanceM: distanceM},
		ascentPerKm: ascentPerKm(ascentM, distanceM),
	}
}

func TestAscentPerKmGuardsZeroDistance(t *testing.T) {
	if got := ascentPerKm(50, 0); got != 0 {
		t.Errorf("ascentPerKm(50, 0) = %v, want 0 (a stray 0/0 must not read as \"flattest possible\")", got)
	}
}

func TestMedianOfEvenAndOddCounts(t *testing.T) {
	if got := medianOf([]float64{10, 30, 50}); got != 30 {
		t.Errorf("median of odd count = %v, want 30", got)
	}
	if got := medianOf([]float64{10, 30, 50, 70}); got != 40 {
		t.Errorf("median of even count = %v, want 40 (average of the two middle values)", got)
	}
	if got := medianOf(nil); got != 0 {
		t.Errorf("median of empty = %v, want 0", got)
	}
}

// Flat (0) must keep the lowest-climbing candidates in the pool — this is
// the exact behaviour the "Flat gave more height metres than Hilly" report
// was about: without this selection step, whichever 3 seeds happened to
// succeed first were returned regardless of how they climbed.
func TestSelectByHillinessFlatPicksLowestAscent(t *testing.T) {
	pool := []suggestPoolEntry{poolEntry(110), poolEntry(10), poolEntry(70), poolEntry(20), poolEntry(90), poolEntry(45)}
	got := selectByHilliness(pool, 0, 3)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	for _, c := range got {
		if c.AscentM > 45 {
			t.Errorf("selected ascent %v, want one of the 3 lowest (10, 20, 45)", c.AscentM)
		}
	}
}

// Very hilly (MaxSteepnessDifficulty) must keep the highest-climbing
// candidates — the opposite selection from Flat above, same pool.
func TestSelectByHillinessVeryHillyPicksHighestAscent(t *testing.T) {
	pool := []suggestPoolEntry{poolEntry(110), poolEntry(10), poolEntry(70), poolEntry(20), poolEntry(90), poolEntry(45)}
	got := selectByHilliness(pool, 3, 3)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	for _, c := range got {
		if c.AscentM < 70 {
			t.Errorf("selected ascent %v, want one of the 3 highest (70, 90, 110)", c.AscentM)
		}
	}
}

// Moderate (the default) has no obvious direction to sort by, so it picks
// whichever are closest to the pool's own median instead — excluding the
// flattest and hilliest extremes on either side, not favouring either one.
func TestSelectByHillinessModerateExcludesBothExtremes(t *testing.T) {
	// An odd-sized pool so the median is a real pool member with a unique
	// (non-tied) closest distance to itself, keeping the assertion exact.
	pool := []suggestPoolEntry{poolEntry(5), poolEntry(20), poolEntry(35), poolEntry(50), poolEntry(180)}
	got := selectByHilliness(pool, routing.DefaultSteepnessDifficulty, 3)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	for _, c := range got {
		if c.AscentM == 5 || c.AscentM == 180 {
			t.Errorf("selected ascent %v, want the flattest (5) and hilliest (180) extremes excluded", c.AscentM)
		}
	}
}

// A pool smaller than n (every attempt but a couple failed) must return
// whatever it has rather than pad or error — same "partial success still
// succeeds" shape handleRouteBuilderSuggest's own caller already relies on.
func TestSelectByHillinessReturnsWhateverThePoolHas(t *testing.T) {
	pool := []suggestPoolEntry{poolEntry(10)}
	got := selectByHilliness(pool, 0, 3)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 (the whole pool)", len(got))
	}
}

// Reported live: asking for 80km returned 90-102km loops. Reproduces that
// exact shape — a wild distance outlier that also happens to be the
// steepest-climbing entry in the pool, so selectByHilliness alone (no
// notion of distance at all) would pick it for a "Very hilly" request even
// though it missed the target by 27%. selectSuggestCandidates must exclude
// it from consideration before hilliness ever gets a vote.
//
// Requested explicitly: "if asking for a distance of 60 only return plus
// and minus 5 km" — maxDistanceDeviation is 10%, so at an 80km target
// (±8000m, 72000-88000 allowed) only 3 of these 6 entries actually
// qualify; the other 3 (including the steepest) are hard-excluded, not
// merely deprioritised.
func TestSelectSuggestCandidatesExcludesOutOfToleranceEntries(t *testing.T) {
	const targetM = 80000
	pool := []suggestPoolEntry{
		poolEntryDist(102100, 636), // +27.6% — climbs the most per km, but wildly over tolerance
		poolEntryDist(90000, 394),  // +12.5% — still outside the 10% band
		poolEntryDist(89700, 369),  // +12.1% — same
		poolEntryDist(85000, 300),  // +6.25% — within tolerance
		poolEntryDist(81000, 250),  // +1.25% — within tolerance
		poolEntryDist(79000, 200),  // -1.25% — within tolerance
	}
	got := selectSuggestCandidates(pool, targetM, routing.MaxSteepnessDifficulty, 3)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3 (only 3 of the 6 pool entries are within maxDistanceDeviation of 80km)", len(got))
	}
	for _, c := range got {
		if c.DistanceM != 85000 && c.DistanceM != 81000 && c.DistanceM != 79000 {
			t.Errorf("selected distance %v, want one of the 3 within tolerance (79000, 81000, 85000)", c.DistanceM)
		}
	}
}

// A pool where nothing lands within tolerance must come back empty, not
// padded with the least-bad option — handleRouteBuilderSuggest's own nil
// lastErr guard depends on this being a real, distinguishable "found
// nothing" case, not a fallback to "closest anyway."
func TestSelectSuggestCandidatesReturnsEmptyWhenNoneWithinTolerance(t *testing.T) {
	pool := []suggestPoolEntry{poolEntryDist(200000, 100), poolEntryDist(100, 50), poolEntryDist(50000, 10)}
	got := selectSuggestCandidates(pool, 80000, 0, 3)
	if len(got) != 0 {
		t.Fatalf("got %d candidates, want 0 (none of the pool is within maxDistanceDeviation of 80km)", len(got))
	}
}

// targetDistanceM <= 0 shouldn't happen past handleRouteBuilderSuggest's
// own validation, but this function doesn't assume its caller — it must
// skip the tolerance filter rather than dividing by a non-positive target.
func TestSelectSuggestCandidatesSkipsFilterWithoutATarget(t *testing.T) {
	pool := []suggestPoolEntry{
		poolEntryDist(200000, 100), poolEntryDist(100, 50), poolEntryDist(50000, 10),
		poolEntryDist(300000, 5), poolEntryDist(400000, 1), poolEntryDist(500000, 1),
	}
	got := selectSuggestCandidates(pool, 0, 0, 3)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
}

// straightOutAndBack builds a path that rides straight out from
// (startLat, startLon) for legs steps of stepDeg latitude each, then rides
// straight back along the identical coordinates — the clearest possible
// "same street, opposite direction" shape backtrackFraction exists to
// catch.
func straightOutAndBack(startLat, startLon float64, legs int, stepDeg float64) [][2]float64 {
	points := make([][2]float64, 0, 2*legs+1)
	for i := 0; i <= legs; i++ {
		points = append(points, [2]float64{startLat + float64(i)*stepDeg, startLon})
	}
	for i := legs - 1; i >= 0; i-- {
		points = append(points, [2]float64{startLat + float64(i)*stepDeg, startLon})
	}
	return points
}

// The clearest possible backtrack shape: ride straight out, turn around,
// ride straight back on the identical coordinates. Only the two legs
// immediately either side of the turnaround point escape being flagged
// (backtrackMinArcGapM excludes them — see its own doc comment for why),
// so with 4 legs each way this must land comfortably over half.
func TestBacktrackFractionFlagsAStraightOutAndBackSpur(t *testing.T) {
	points := straightOutAndBack(50.000, 4.000, 4, 0.001)
	if got := backtrackFraction(points); got < 0.5 {
		t.Errorf("backtrackFraction = %v, want at least 0.5 for a clear out-and-back spur", got)
	}
}

// A real loop's opposite sides point in close to opposite directions too
// (north vs. south, east vs. west) — the fraction must come out at 0
// anyway, because those sides sit far apart geometrically. Distinguishes
// "points the other way" from "is the same street" — the whole reason the
// distance check exists alongside the direction one.
func TestBacktrackFractionLoopIsNotFlagged(t *testing.T) {
	points := [][2]float64{
		{50.000, 4.000},
		{50.001, 4.000},
		{50.001, 4.001},
		{50.000, 4.001},
		{50.000, 4.000},
	}
	if got := backtrackFraction(points); got != 0 {
		t.Errorf("backtrackFraction of a square loop = %v, want 0 (opposite sides are far apart, not the same street)", got)
	}
}

func TestBacktrackFractionTooFewPointsToCompare(t *testing.T) {
	if got := backtrackFraction([][2]float64{{50, 4}, {50.001, 4}}); got != 0 {
		t.Errorf("backtrackFraction of a 2-point path = %v, want 0", got)
	}
	if got := backtrackFraction(nil); got != 0 {
		t.Errorf("backtrackFraction(nil) = %v, want 0", got)
	}
}

// If every surviving candidate backtracks (a sparse road network near the
// start point, most likely), selectSuggestCandidates must still return
// something — a rider seeing no suggestions at all is worse than one that
// isn't a perfect loop. Mirrors maxDistanceDeviation's own "fewer honestly
// close beats padding the list" trade-off, but in the opposite direction:
// here, degrading gracefully beats an empty result.
func TestSelectSuggestCandidatesFallsBackWhenEveryCandidateBacktracks(t *testing.T) {
	spur := straightOutAndBack(50.000, 4.000, 4, 0.001)
	pool := []suggestPoolEntry{
		{candidate: routeBuilderCandidate{Points: spur, DistanceM: 888, AscentM: 10}, ascentPerKm: ascentPerKm(10, 888)},
	}
	got := selectSuggestCandidates(pool, 888, 1, 1)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 (a backtracking loop is still better than none)", len(got))
	}
}

// When a low-backtrack candidate exists alongside a spur, it must win even
// when hilliness alone would have favoured the spur — proves the backtrack
// filter actually runs before hilliness gets a vote, not just that the
// loop happens to also win some other way.
func TestSelectSuggestCandidatesPrefersLowBacktrackOverHillinessFit(t *testing.T) {
	spur := straightOutAndBack(50.000, 4.000, 4, 0.001)
	loop := [][2]float64{
		{50.000, 4.000}, {50.001, 4.000}, {50.001, 4.001}, {50.000, 4.001}, {50.000, 4.000},
	}
	pool := []suggestPoolEntry{
		{candidate: routeBuilderCandidate{Points: spur, DistanceM: 1000, AscentM: 10}, ascentPerKm: 10},
		{candidate: routeBuilderCandidate{Points: loop, DistanceM: 1000, AscentM: 50}, ascentPerKm: 50},
	}
	// Flat (0) would normally pick the lowest ascent-per-km — the spur.
	got := selectSuggestCandidates(pool, 1000, 0, 2)
	if len(got) != 1 || got[0].AscentM != 50 {
		t.Fatalf("got %+v, want only the non-backtracking loop (ascent 50) even though Flat favours lower ascent", got)
	}
}
