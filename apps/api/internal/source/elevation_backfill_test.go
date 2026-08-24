package source

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/elevation"
)

// allZeroElevationGPX mirrors the real-world file that prompted this
// feature — afstandmeten.nl, a route-planning site, stamps every point
// with a literal 0.00000 rather than ever querying real terrain. Four
// points is enough to prove ascent gets computed from the backfilled
// values, not to model a real run.
const allZeroElevationGPX = `<?xml version="1.0"?>
<gpx version="1.1"><trk><trkseg>
<trkpt lat="50.8000" lon="4.7000"><ele>0.00000</ele></trkpt>
<trkpt lat="50.8010" lon="4.7000"><ele>0.00000</ele></trkpt>
<trkpt lat="50.8020" lon="4.7000"><ele>0.00000</ele></trkpt>
<trkpt lat="50.8030" lon="4.7000"><ele>0.00000</ele></trkpt>
</trkseg></trk></gpx>`

// fakeElevationServer returns a climbing profile — elevation rises with
// latitude — so a test can assert a specific, nonzero ascent came from
// the backfill rather than merely "some number, who knows."
func fakeElevationServer(t *testing.T) *elevation.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Locations []struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"locations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		results := make([]struct {
			Elevation float64 `json:"elevation"`
		}, len(body.Locations))
		for i, loc := range body.Locations {
			// 50.800 -> 0m, 50.803 -> 30m: a clean, checkable 10m per
			// 0.001 degrees of latitude.
			results[i].Elevation = (loc.Latitude - 50.800) * 10000
		}
		_ = json.NewEncoder(w).Encode(struct {
			Results []struct {
				Elevation float64 `json:"elevation"`
			} `json:"results"`
		}{Results: results})
	}))
	t.Cleanup(srv.Close)
	return elevation.New(srv.URL)
}

// TestDBCreateBackfillsElevationWhenTheFileHasNone proves the feature
// end to end: a route uploaded with only placeholder (all-zero)
// elevation gets a real ascent computed from the configured terrain
// API's own response, not left at a misleading 0.
func TestDBCreateBackfillsElevationWhenTheFileHasNone(t *testing.T) {
	db := openTestDB(t)
	db.SetElevationClient(fakeElevationServer(t))

	route, err := db.Create(t.Context(), CreateRequest{
		Name: "Night Run",
		GPX:  []byte(allZeroElevationGPX),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if math.Abs(route.Stats.AscentM-30) > 0.01 {
		t.Errorf("ascent = %.4f m, want 30 m from the backfilled elevation", route.Stats.AscentM)
	}
}

// TestDBCreateLeavesRealElevationAlone proves the backfill never runs —
// and so never overwrites a file's own genuine, if flat, elevation data —
// when there is something real to begin with.
func TestDBCreateLeavesRealElevationAlone(t *testing.T) {
	db := openTestDB(t)
	// A server that would answer with a completely different profile —
	// if this got called at all, the assertion below would catch it.
	db.SetElevationClient(fakeElevationServer(t))

	const realGPX = `<?xml version="1.0"?>
<gpx version="1.1"><trk><trkseg>
<trkpt lat="50.800" lon="4.700"><ele>12.4</ele></trkpt>
<trkpt lat="50.801" lon="4.700"><ele>19.8</ele></trkpt>
</trkseg></trk></gpx>`

	route, err := db.Create(t.Context(), CreateRequest{Name: "Real Elevation", GPX: []byte(realGPX)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if math.Abs(route.Stats.AscentM-7.4) > 0.01 {
		t.Errorf("ascent = %.2f m, want 7.4 m from the file's own elevation, not the terrain API's", route.Stats.AscentM)
	}
}

// TestDBCreateSurvivesAnUnreachableElevationService proves a third party
// being down never fails the upload — it just leaves elevation exactly as
// gpx.ParsePoints found it, the same as if backfilling were off.
func TestDBCreateSurvivesAnUnreachableElevationService(t *testing.T) {
	db := openTestDB(t)
	db.SetElevationClient(elevation.New("http://127.0.0.1:1")) // nothing listens here

	route, err := db.Create(t.Context(), CreateRequest{Name: "Night Run", GPX: []byte(allZeroElevationGPX)})
	if err != nil {
		t.Fatalf("create: %v, want the upload to succeed even though elevation lookup fails", err)
	}
	if route.Stats.AscentM != 0 {
		t.Errorf("ascent = %.1f m, want 0 — no backfill happened, so nothing should have changed", route.Stats.AscentM)
	}
}

// TestDBCreateDoesNotBackfillWithoutAnElevationClient proves the feature
// is genuinely off by default — the same all-zero file, with no
// SetElevationClient call at all, must behave exactly as it always has.
func TestDBCreateDoesNotBackfillWithoutAnElevationClient(t *testing.T) {
	db := openTestDB(t)

	route, err := db.Create(t.Context(), CreateRequest{Name: "Night Run", GPX: []byte(allZeroElevationGPX)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if route.Stats.AscentM != 0 {
		t.Errorf("ascent = %.1f m, want 0 — elevation.Client is nil, backfill must not run", route.Stats.AscentM)
	}
}
