package basemap

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

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

// TestFetchLayersReturnsEmptySlicesNotNilOutsideCoverage pins the fix for a
// route whose bbox has zero tile coverage in the archive — not just near an
// admin's extracted edge, genuinely outside it (e.g. a route in a different
// country from whatever region was extracted). Before this fix,
// PreviewLayers' fields were left at their nil zero value in that case, and
// encoding/json marshals a nil slice as JSON null rather than [] — which
// crashed TrackPreview.vue's ringsToPath/pointsToPath, both of which call
// .map() on these fields with no null-guard. A fake archive whose
// min/max zoom excludes every zoom FetchLayers ever requests
// (previewMinZoom..previewMaxZoom, 8..13) reproduces "no coverage at all"
// without needing a real tile directory: GetTile's own zoom check rejects
// every tile before it ever touches directory data.
func TestFetchLayersReturnsEmptySlicesNotNilOutsideCoverage(t *testing.T) {
	header := make([]byte, pmtilesHeaderSize)
	copy(header, "PMTiles")
	header[7] = 3  // spec version
	header[97] = 1 // internal compression: none
	header[98] = 1 // tile compression: none
	header[100] = 14
	header[101] = 14 // min/max zoom, both outside [previewMinZoom, previewMaxZoom]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "basemap.pmtiles", time.Time{}, bytes.NewReader(header))
	}))
	defer srv.Close()

	tiles := NewPreviewTiles(srv.URL)
	layers, err := tiles.FetchLayers(context.Background(), 4.55, 50.80, 4.85, 50.95)
	if err != nil {
		t.Fatalf("FetchLayers: %v", err)
	}

	for name, got := range map[string]any{
		"Earth": layers.Earth, "Landuse": layers.Landuse, "Water": layers.Water,
		"WaterLines": layers.WaterLines, "Roads": layers.Roads,
	} {
		if reflect.ValueOf(got).IsNil() {
			t.Errorf("%s is nil, want a non-nil empty slice — a nil slice marshals as JSON null, "+
				"which crashes TrackPreview.vue's ringsToPath/pointsToPath", name)
		}
	}
}
