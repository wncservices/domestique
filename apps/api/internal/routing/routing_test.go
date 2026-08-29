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
	body := directionsResponse{
		Features: []struct {
			Geometry struct {
				Coordinates [][]float64 `json:"coordinates"`
			} `json:"geometry"`
		}{
			{Geometry: struct {
				Coordinates [][]float64 `json:"coordinates"`
			}{Coordinates: coords}},
		},
	}
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
	points, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.87, Lon: 4.37}}, "")
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

	if len(points) != 3 || points[0].Lat != 50.85 || points[0].Lon != 4.35 {
		t.Errorf("points = %v, want the geojson coordinates flipped back to lat/lon", points)
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
	points, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}}, "")
	if err != nil {
		t.Fatal(err)
	}
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
	points, err := c.Route(context.Background(), []LatLng{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if points[0].HasEle || points[1].HasEle {
		t.Errorf("points = %+v, want HasEle false with no third coordinate", points)
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
	if len(second) != len(first) || second[0] != first[0] {
		t.Errorf("cached response = %v, want the same points as the first call %v", second, first)
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
