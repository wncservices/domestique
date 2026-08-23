package basemap

import (
	"reflect"
	"testing"

	"github.com/paulmach/orb/maptile"
)

// TestChooseZoomAndTilesMatchesReferenceJS pins chooseZoomAndTiles against
// the exact zoom and tile list utils/staticBasemap.ts's own
// chooseZoomAndTiles produces for the same bbox (verified independently in
// Node against the live basemap before this test was written) — the two
// implementations sharing the same formula only means something if they
// are actually checked to agree, not just eyeballed as "looks the same."
func TestChooseZoomAndTilesMatchesReferenceJS(t *testing.T) {
	zoom, tiles := chooseZoomAndTiles(4.55, 50.75, 4.85, 50.95)
	if zoom != 11 {
		t.Fatalf("zoom = %d, want 11", zoom)
	}
	want := []maptile.Tile{
		{X: 1049, Y: 686, Z: 11},
		{X: 1049, Y: 687, Z: 11},
		{X: 1050, Y: 686, Z: 11},
		{X: 1050, Y: 687, Z: 11},
		{X: 1051, Y: 686, Z: 11},
		{X: 1051, Y: 687, Z: 11},
	}
	if !reflect.DeepEqual(tiles, want) {
		t.Fatalf("tiles = %+v, want %+v", tiles, want)
	}
}

// TestZxyToPMTileIDMatchesSpecTable checks every z/x/y -> tileID row in the
// PMTiles v3 spec's own reference table
// (github.com/protomaps/PMTiles/blob/main/spec/v3/spec.md) — the Hilbert
// curve mapping has no pseudocode in the spec itself, so this is the only
// direct evidence the port is correct rather than merely "compiles."
func TestZxyToPMTileIDMatchesSpecTable(t *testing.T) {
	cases := []struct {
		z          uint32
		x, y, want uint64
	}{
		{0, 0, 0, 0},
		{1, 0, 0, 1},
		{1, 0, 1, 2},
		{1, 1, 1, 3},
		{1, 1, 0, 4},
		{2, 0, 0, 5},
	}
	for _, c := range cases {
		if got := zxyToPMTileID(c.z, c.x, c.y); got != c.want {
			t.Errorf("zxyToPMTileID(%d,%d,%d) = %d, want %d", c.z, c.x, c.y, got, c.want)
		}
	}
}

func TestRouteBBoxFillsFullCanvas(t *testing.T) {
	// A tall, narrow synthetic route — the shape that most exposed the
	// letterboxing bug this mirrors (see TrackPreview.vue's own fix).
	var points [][2]float64
	for i := 0; i <= 20; i++ {
		points = append(points, [2]float64{50.80 + float64(i)*0.01, 4.70})
	}

	west, south, east, north, ok := RouteBBox(points)
	if !ok {
		t.Fatal("RouteBBox reported not ok for a valid route")
	}

	tightWest, tightEast := 4.70, 4.70
	if west >= tightWest || east <= tightEast {
		t.Errorf("bbox [%f,%f] does not extend beyond the route's own tight longitude (%f); "+
			"the whole point of this bbox is to reach the card's letterboxed edges",
			west, east, tightWest)
	}
	if !(south < 50.80 && north > 51.00) {
		t.Errorf("bbox south=%f north=%f does not cover the route's own latitude span [50.80,51.00] plus padding",
			south, north)
	}
}
