package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func geojsonResponse(coords [][]float64) string {
	return geojsonResponseWithSurface(coords, nil)
}

// surfaceFixture is one entry of a fake ORS response's own
// extras.surface.summary — matching the real API's shape (confirmed live,
// see routing.go's own comment on why that matters here), not guessed.
type surfaceFixture struct {
	Value    float64
	Distance float64
	Amount   float64
}

// geojsonResponseWithSurface builds a fake ORS directions response as raw
// JSON text — deliberately not a directionsResponse{} literal, since that
// struct's own anonymous nested types would have to be repeated exactly
// here; marshaling a small local type into the same JSON shape is what a
// real server actually sends, which is all a test double needs to match.
func geojsonResponseWithSurface(coords [][]float64, surface []surfaceFixture) string {
	type summaryEntry struct {
		Value    float64 `json:"value"`
		Distance float64 `json:"distance"`
		Amount   float64 `json:"amount"`
	}
	summary := make([]summaryEntry, len(surface))
	for i, s := range surface {
		summary[i] = summaryEntry(s)
	}

	type feature struct {
		Geometry struct {
			Coordinates [][]float64 `json:"coordinates"`
		} `json:"geometry"`
		Properties struct {
			Extras struct {
				Surface struct {
					Summary []summaryEntry `json:"summary"`
				} `json:"surface"`
			} `json:"extras"`
		} `json:"properties"`
	}
	var f feature
	f.Geometry.Coordinates = coords
	f.Properties.Extras.Surface.Summary = summary

	body := struct {
		Features []feature `json:"features"`
	}{Features: []feature{f}}

	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestRouteSnapsThroughEveryWaypoint(t *testing.T) {
	var gotPath string
	var gotBody directionsRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		if got := r.Header.Get("Authorization"); got != "test-key" {
			t.Errorf("Authorization header = %q, want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse([][]float64{{4.35, 50.85}, {4.36, 50.86}, {4.37, 50.87}})))
	}))
	defer server.Close()

	c := New(server.URL, "test-key")
	path, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.87, Lon: 4.37}}, "")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(gotPath, "/v2/directions/"+DefaultProfile+"/geojson") {
		t.Errorf("path = %q, want a %s directions request", gotPath, DefaultProfile)
	}
	if len(gotBody.Coordinates) != 2 || gotBody.Coordinates[0] != [2]float64{4.35, 50.85} {
		t.Errorf("request coordinates = %v, want [lon,lat] pairs matching the input waypoints", gotBody.Coordinates)
	}
	if gotBody.Options == nil || gotBody.Options.RoundTrip != nil {
		t.Error("a plain Route call must not set round_trip options")
	}
	if got := gotBody.Options.AvoidFeatures; len(got) != 2 || got[0] != "steps" || got[1] != "fords" {
		t.Errorf("avoid_features = %v, want [\"steps\",\"fords\"] on every request", got)
	}
	if !gotBody.Elevation {
		t.Error("a Route call must ask ORS for elevation")
	}
	if len(gotBody.ExtraInfo) != 1 || gotBody.ExtraInfo[0] != "surface" {
		t.Errorf("extra_info = %v, want [\"surface\"] on every request", gotBody.ExtraInfo)
	}

	if len(path.Points) != 3 || path.Points[0].Lat != 50.85 || path.Points[0].Lon != 4.35 {
		t.Errorf("points = %v, want the geojson coordinates flipped back to lat/lon", path.Points)
	}
}

// The third coordinate value ORS returns when elevation is requested must
// become the point's own Ele/HasEle — gpx.ComputeStats derives the route
// builder's height-gain figure from exactly this, not a second lookup.
func TestElevationIsReadFromTheThirdCoordinate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse([][]float64{{4.35, 50.85, 12.5}, {4.36, 50.86, 40}})))
	}))
	defer server.Close()

	c := New(server.URL, "")
	path, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}}, "")
	if err != nil {
		t.Fatal(err)
	}
	points := path.Points
	if !points[0].HasEle || points[0].Ele != 12.5 || !points[1].HasEle || points[1].Ele != 40 {
		t.Errorf("points = %+v, want elevation carried over from the third coordinate", points)
	}
}

// A self-hosted instance an operator forgot to enable elevation support on
// still answers with plain [lon,lat] pairs — that must degrade to "no
// elevation", not an error.
func TestMissingElevationCoordinateIsNotAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse([][]float64{{4.35, 50.85}, {4.36, 50.86}})))
	}))
	defer server.Close()

	c := New(server.URL, "")
	path, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if path.Points[0].HasEle || path.Points[1].HasEle {
		t.Errorf("points = %+v, want HasEle false with no third coordinate", path.Points)
	}
}

// The route builder's own "type of ground" figure comes from exactly this —
// ORS's own extras.surface.summary, confirmed against the real API's
// response shape (see routing.go's own comment on why that matters here),
// translated from numeric code to a human label and sorted most-distance-
// first so a caller can show the dominant surface without sorting itself.
func TestSurfaceSummaryIsParsedAndSortedByDistance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponseWithSurface(
			[][]float64{{4.35, 50.85}, {4.36, 50.86}},
			[]surfaceFixture{
				{Value: 0, Distance: 134.0, Amount: 4.21},   // Unknown
				{Value: 3, Distance: 2995.1, Amount: 94.03}, // Asphalt
				{Value: 14, Distance: 56.2, Amount: 1.76},   // Paving Stones
			},
		)))
	}))
	defer server.Close()

	c := New(server.URL, "")
	path, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}}, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(path.Surface) != 3 {
		t.Fatalf("surface = %+v, want 3 entries", path.Surface)
	}
	// Sorted by distance descending: Asphalt (2995.1) first, Unknown (134.0)
	// second, Paving Stones (56.2) last — not ORS's own summary order, which
	// listed Unknown before Asphalt.
	if path.Surface[0].Type != "Asphalt" || path.Surface[0].DistanceM != 2995.1 {
		t.Errorf("surface[0] = %+v, want Asphalt at 2995.1m first", path.Surface[0])
	}
	if got := path.Surface[0].Fraction; got < 0.9403-0.0001 || got > 0.9403+0.0001 {
		t.Errorf("surface[0].Fraction = %v, want 0.9403 (ORS's 94.03%% divided by 100)", got)
	}
	if path.Surface[1].Type != "Unknown" {
		t.Errorf("surface[1] = %+v, want Unknown second", path.Surface[1])
	}
	if path.Surface[2].Type != "Paving Stones" {
		t.Errorf("surface[2] = %+v, want Paving Stones last", path.Surface[2])
	}
}

// A future ORS release adding a surface code this table doesn't have yet
// must not silently drop that stretch of road's distance from the total —
// it shows up labelled as unrecognised instead.
func TestUnrecognisedSurfaceCodeIsLabelledNotDropped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponseWithSurface(
			[][]float64{{4.35, 50.85}, {4.36, 50.86}},
			[]surfaceFixture{{Value: 99, Distance: 500, Amount: 100}},
		)))
	}))
	defer server.Close()

	c := New(server.URL, "")
	path, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(path.Surface) != 1 || path.Surface[0].DistanceM != 500 || path.Surface[0].Type != "Unrecognised (99)" {
		t.Errorf("surface = %+v, want one entry labelled Unrecognised (99) at 500m", path.Surface)
	}
}

// A self-hosted instance without the surface extra_info enabled (or
// without data for this stretch of road) must degrade to an empty
// breakdown, not an error — the same shape as missing elevation.
func TestMissingSurfaceDataIsNotAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse([][]float64{{4.35, 50.85}, {4.36, 50.86}})))
	}))
	defer server.Close()

	c := New(server.URL, "")
	path, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(path.Surface) != 0 {
		t.Errorf("surface = %+v, want empty with no surface data in the response", path.Surface)
	}
}

func TestRoundTripSendsLengthAndSeed(t *testing.T) {
	var gotBody directionsRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse([][]float64{{4.35, 50.85}, {4.36, 50.86}, {4.35, 50.85}})))
	}))
	defer server.Close()

	c := New(server.URL, "")
	_, err := c.RoundTrip(context.Background(), LatLng{Lat: 50.85, Lon: 4.35}, 20000, 7, "cycling-road")
	if err != nil {
		t.Fatal(err)
	}

	if gotBody.Options == nil || gotBody.Options.RoundTrip == nil {
		t.Fatal("a RoundTrip call must set round_trip options")
	}
	if gotBody.Options.RoundTrip.Length != 20000 || gotBody.Options.RoundTrip.Seed != 7 {
		t.Errorf("round_trip options = %+v, want length=20000 seed=7", gotBody.Options.RoundTrip)
	}
	if got := gotBody.Options.AvoidFeatures; len(got) != 2 || got[0] != "steps" || got[1] != "fords" {
		t.Errorf("avoid_features = %v, want [\"steps\",\"fords\"] on every request", got)
	}
}

func TestRouteRejectsFewerThanTwoWaypoints(t *testing.T) {
	c := New("http://unused.invalid", "")
	if _, err := c.Route(context.Background(), []LatLng{{Lat: 1, Lon: 1}}, ""); err == nil {
		t.Fatal("expected an error for a single waypoint")
	}
}

func TestRoundTripRejectsNonPositiveDistance(t *testing.T) {
	c := New("http://unused.invalid", "")
	if _, err := c.RoundTrip(context.Background(), LatLng{Lat: 1, Lon: 1}, 0, 1, ""); err == nil {
		t.Fatal("expected an error for a zero distance")
	}
}

func TestNonOKStatusIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer server.Close()

	c := New(server.URL, "wrong-key")
	if _, err := c.Route(context.Background(), []LatLng{{Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}}, ""); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestEmptyFeaturesIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse(nil)))
	}))
	defer server.Close()

	c := New(server.URL, "")
	if _, err := c.Route(context.Background(), []LatLng{{Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}}, ""); err == nil {
		t.Fatal("expected an error when the engine returns no usable geometry")
	}
}

func TestMalformedJSONIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	c := New(server.URL, "")
	if _, err := c.Route(context.Background(), []LatLng{{Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}}, ""); err == nil {
		t.Fatal("expected an error for a malformed response body")
	}
}

// An unrecognised profile must be rejected before it ever reaches the
// outbound request URL — directions splices it in unescaped, so the test
// server below fails the test outright if a request lands at all.
func TestUnsupportedProfileIsRejectedWithoutCallingOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("routing engine was called despite an unsupported profile")
	}))
	defer server.Close()

	c := New(server.URL, "")
	waypoints := []LatLng{{Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}}
	for _, bad := range []string{"../admin", "cycling-regular/../secret", "walking", "cycling-regular?x=1"} {
		if _, err := c.Route(context.Background(), waypoints, bad); err == nil {
			t.Errorf("profile %q: expected an error, got none", bad)
		}
	}
}

// An identical request repeated — the debounced preview firing again for
// waypoints that did not change, or "Generate 3 options" clicked twice with
// the same start and distance — must not spend the API key's own quota
// twice for an answer already in hand.
func TestIdenticalRouteRequestsAreServedFromCache(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse([][]float64{{4.35, 50.85}, {4.36, 50.86}, {4.37, 50.87}})))
	}))
	defer server.Close()

	c := New(server.URL, "test-key")
	waypoints := []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.87, Lon: 4.37}}

	first, err := c.Route(context.Background(), waypoints, "cycling-road")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Route(context.Background(), waypoints, "cycling-road")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("routing engine was called %d times for two identical requests, want 1", calls)
	}
	if len(second.Points) != len(first.Points) || second.Points[0] != first.Points[0] {
		t.Errorf("cached response = %v, want the same points as the first call %v", second.Points, first.Points)
	}

	// A genuinely different request (a moved waypoint) is not the same
	// question and must not be answered from the first request's cache entry.
	if _, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 51.0, Lon: 4.5}}, "cycling-road"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("routing engine was called %d times after a genuinely different request, want 2", calls)
	}
}

// RoundTrip's seed is part of what a request asks for — two different
// seeds are two different candidate loops, not a repeat of one question,
// so caching must not collapse them into the same cached answer.
func TestDifferentSeedsAreNotCachedTogether(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse([][]float64{{4.35, 50.85}, {4.36, 50.86}, {4.35, 50.85}})))
	}))
	defer server.Close()

	c := New(server.URL, "")
	start := LatLng{Lat: 50.85, Lon: 4.35}
	if _, err := c.RoundTrip(context.Background(), start, 20000, 1, "cycling-road"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RoundTrip(context.Background(), start, 20000, 2, "cycling-road"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("routing engine was called %d times for two different seeds, want 2", calls)
	}

	// But the same seed again is the same question and should hit the cache.
	if _, err := c.RoundTrip(context.Background(), start, 20000, 1, "cycling-road"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("routing engine was called %d times after repeating a seed, want still 2", calls)
	}
}

// cacheKey's own doc comment promises to capture everything that can change
// a directions response — AvoidFeatures included, even though every caller
// today sets it to the same fixed value. A future caller that varies it
// must not silently collide with a cached answer computed under a
// different constraint.
func TestCacheKeyDiffersByAvoidFeatures(t *testing.T) {
	base := directionsRequest{Coordinates: [][2]float64{{4.35, 50.85}, {4.36, 50.86}}}
	withAvoid := base
	withAvoid.Options = &directionsOpts{AvoidFeatures: []string{"steps"}}
	withDifferentAvoid := base
	withDifferentAvoid.Options = &directionsOpts{AvoidFeatures: []string{"fords"}}

	keyPlain := cacheKey("cycling-regular", base)
	keySteps := cacheKey("cycling-regular", withAvoid)
	keyFords := cacheKey("cycling-regular", withDifferentAvoid)

	if keyPlain == keySteps || keySteps == keyFords || keyPlain == keyFords {
		t.Errorf("cache keys = %q, %q, %q — want three distinct keys for three different avoid_features", keyPlain, keySteps, keyFords)
	}
}

// Two callers asking for the exact same route at the same moment must not
// each spend the routing engine's own rate-limited quota independently —
// this is exactly the "double-click Generate" / two-tabs-open shape the
// response cache exists to protect against, but only if concurrent misses
// are coalesced into one real call rather than each firing its own.
func TestConcurrentIdenticalRequestsAreCoalesced(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-release // hold every request that reaches the server open
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse([][]float64{{4.35, 50.85}, {4.36, 50.86}})))
	}))
	defer server.Close()

	c := New(server.URL, "")
	waypoints := []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}}

	const concurrent = 5
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for range concurrent {
		go func() {
			defer wg.Done()
			if _, err := c.Route(context.Background(), waypoints, "cycling-road"); err != nil {
				t.Error(err)
			}
		}()
	}

	// Give every goroutine time to reach either the server or the in-flight
	// wait, whichever this call actually does, before letting the one real
	// request (if coalescing worked) or every request (if it didn't)
	// through — either way this unblocks everything and the call count
	// alone tells us which happened.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("routing engine was called %d times for %d concurrent identical requests, want 1", got, concurrent)
	}
}

func TestValidProfilesAreAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geojsonResponse([][]float64{{1, 1}, {2, 2}})))
	}))
	defer server.Close()

	c := New(server.URL, "")
	waypoints := []LatLng{{Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}}
	for profile := range ValidProfiles {
		if _, err := c.Route(context.Background(), waypoints, profile); err != nil {
			t.Errorf("profile %q: unexpected error: %v", profile, err)
		}
	}
}
