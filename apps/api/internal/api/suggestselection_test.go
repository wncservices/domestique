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
