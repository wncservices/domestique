// Acceptance tests for the route-builder foundation: snapping a preview
// path through waypoints, and saving whatever a builder tab produces as a
// real route — see internal/routing's own package doc for why an AI-native
// "suggest a route" feature is built on this same real-routing-engine
// pipeline rather than ever asking a language model for coordinates.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/geocoding"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/routing"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

// stubRoutingClient substitutes for a real ORS instance in tests, the same
// reason fakeTarget substitutes for a real Garmin/Wahoo adapter — it always
// returns the same fixed route regardless of what it's asked to snap or
// generate, which is all most tests here need. failCallNums lets a suggest
// test make one specific call (the 1st, 2nd, ...) of a multi-seed request
// fail without a fake HTTP server of its own — see
// TestRouteBuilderSuggestReturnsWhicheverCandidatesSucceed. Keyed by call
// number rather than the seed value itself: server.go's suggestSeeds picks
// a fresh random base per request, so a test can no longer predict which
// literal seed a given call will use.
type stubRoutingClient struct {
	route        []gpx.Point
	surface      []routing.SurfaceSummary
	err          error
	failCallNums map[int]bool
	callCount    *atomic.Int32
	// onRoundTrip, when set, is called with every seed RoundTrip receives —
	// TestSuggestSeedsVaryBetweenRequests' own way of observing what
	// server.go's suggestSeeds actually generated, since it is unexported
	// and this file is package api_test.
	onRoundTrip func(seed int)
	// onProfile, when set, is called with the profile string every Route or
	// RoundTrip call receives — the road-type selector's own way of proving
	// a rider's chosen bike type actually reaches the routing engine, not
	// just that it passes request validation.
	onProfile func(profile string)
	// onHilliness, when set, is called with the hilliness int every
	// RoundTrip call receives — the hilliness selector's own way of
	// proving a rider's chosen fitness level actually reaches the routing
	// engine. -1 means "unspecified" (server.go's own sentinel for an
	// omitted request field), the same shape onProfile's "" already has.
	onHilliness func(hilliness int)
	// onLength, when set, is called with the requestedLengthM every
	// RoundTrip call receives — the calibration/refinement split's own way
	// of proving the refinement round actually asks for a compensated
	// length, not the rider's raw target repeated a second time.
	onLength func(lengthM float64)
	// routeForCall, when set, overrides route per call (1-indexed, needs
	// callCount set too) — TestRouteBuilderSuggestPicksBestFitByHilliness's
	// own way of giving each of the 6 pool attempts a different ascent, to
	// prove selectByHilliness actually picks by climbing rate rather than
	// just returning whichever 3 calls happened to succeed first.
	routeForCall map[int][]gpx.Point
	// delay, when set, is slept before RoundTrip returns —
	// TestRouteBuilderSuggestFiresAttemptsConcurrently's own way of
	// proving the suggest handler's maxSuggestAttempts calls run in
	// parallel: a sequential loop of 6 calls each sleeping delay would
	// take roughly 6*delay; concurrent calls take roughly one delay.
	delay time.Duration
}

func (s stubRoutingClient) Route(_ context.Context, _ []routing.LatLng, profile string) (routing.Path, error) {
	if s.onProfile != nil {
		s.onProfile(profile)
	}
	return routing.Path{Points: s.route, Surface: s.surface}, s.err
}

func (s stubRoutingClient) RoundTrip(_ context.Context, _ routing.LatLng, requestedLengthM float64, seed int, profile string, hilliness int) (routing.Path, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.onLength != nil {
		s.onLength(requestedLengthM)
	}
	if s.onHilliness != nil {
		s.onHilliness(hilliness)
	}
	if s.onRoundTrip != nil {
		s.onRoundTrip(seed)
	}
	if s.onProfile != nil {
		s.onProfile(profile)
	}
	route := s.route
	if s.callCount != nil {
		n := int(s.callCount.Add(1))
		if s.failCallNums[n] {
			return routing.Path{}, errors.New("stub: this call is configured to fail")
		}
		if r, ok := s.routeForCall[n]; ok {
			route = r
		}
	}
	return routing.Path{Points: route, Surface: s.surface}, s.err
}

// suggestFixtureRoute is the fixed stub route most suggest tests in this
// file use — ~20.0km (confirmed: haversine(50.85,4.35 -> 51.03,4.35) =
// 20015m), matching the "distanceKm": 20 most of those requests send. Has
// to actually be close to that: selectSuggestCandidates now drops any
// candidate whose distance misses the requested one by more than
// maxDistanceDeviation, so a fixture route at some unrelated distance
// would make every suggest response in this file come back empty
// regardless of what's actually being tested.
var suggestFixtureRoute = []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 51.03, Lon: 4.35}}

func newRouteBuilderHarness(t *testing.T, rt routing.Client) (client *http.Client, base string) {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	acct, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{
		Source:   db,
		Store:    st,
		Accounts: acct,
		Config:   &config.Config{},
		Routing:  rt,
	}

	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	return server.Client(), server.URL
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestRouteBuilderPreviewRequiresARoutingEngine(t *testing.T) {
	client, base := newRouteBuilderHarness(t, nil)

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/preview", map[string]any{
		"waypoints": []map[string]float64{{"lat": 50.85, "lon": 4.35}, {"lat": 50.87, "lon": 4.37}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusPreconditionFailed)
	}
}

func TestRouteBuilderPreviewSnapsWaypoints(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}, {Lat: 50.87, Lon: 4.37}},
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/preview", map[string]any{
		"waypoints": []map[string]float64{{"lat": 50.85, "lon": 4.35}, {"lat": 50.87, "lon": 4.37}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Points    [][2]float64 `json:"points"`
		DistanceM float64      `json:"distanceM"`
		AscentM   float64      `json:"ascentM"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Points) != 3 {
		t.Errorf("got %d points, want 3", len(out.Points))
	}
	if out.DistanceM <= 0 {
		t.Errorf("distanceM = %v, want a positive distance", out.DistanceM)
	}
}

// The elevation ORSClient.Route/RoundTrip already asks the routing engine
// for on the same request must reach the preview response as ascentM — see
// coordsDistanceAscent, which derives it from gpx.ComputeStats rather than a
// second elevation lookup.
func TestRouteBuilderPreviewIncludesAscent(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: []gpx.Point{
			{Lat: 50.85, Lon: 4.35, Ele: 100, HasEle: true},
			{Lat: 50.86, Lon: 4.36, Ele: 150, HasEle: true},
			{Lat: 50.87, Lon: 4.37, Ele: 120, HasEle: true},
		},
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/preview", map[string]any{
		"waypoints": []map[string]float64{{"lat": 50.85, "lon": 4.35}, {"lat": 50.87, "lon": 4.37}},
	})
	defer resp.Body.Close()

	var out struct {
		AscentM float64 `json:"ascentM"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.AscentM != 50 {
		t.Errorf("ascentM = %v, want 50 (100 -> 150, the only climb)", out.AscentM)
	}
}

// The route builder's own "type of ground" and elevation-profile figures —
// routing.SurfaceSummary and the per-point elevation ORSClient.Route
// already asked for on the same request — must reach the preview response
// too, not just distance/ascent.
func TestRouteBuilderPreviewIncludesSurfaceAndElevationProfile(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: []gpx.Point{
			{Lat: 50.85, Lon: 4.35, Ele: 100, HasEle: true},
			{Lat: 50.86, Lon: 4.36, Ele: 150, HasEle: true},
		},
		surface: []routing.SurfaceSummary{
			{Type: "Asphalt", DistanceM: 900, Fraction: 0.9},
			{Type: "Gravel", DistanceM: 100, Fraction: 0.1},
		},
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/preview", map[string]any{
		"waypoints": []map[string]float64{{"lat": 50.85, "lon": 4.35}, {"lat": 50.86, "lon": 4.36}},
	})
	defer resp.Body.Close()

	var out struct {
		Surface []struct {
			Type      string  `json:"type"`
			DistanceM float64 `json:"distanceM"`
			Fraction  float64 `json:"fraction"`
		} `json:"surface"`
		ElevationProfile []struct {
			DistanceM float64 `json:"distanceM"`
			EleM      float64 `json:"eleM"`
		} `json:"elevationProfile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Surface) != 2 || out.Surface[0].Type != "Asphalt" || out.Surface[0].Fraction != 0.9 {
		t.Errorf("surface = %+v, want the two entries the routing engine returned", out.Surface)
	}
	if len(out.ElevationProfile) != 2 || out.ElevationProfile[0].EleM != 100 || out.ElevationProfile[1].EleM != 150 {
		t.Errorf("elevationProfile = %+v, want one sample per point carrying elevation", out.ElevationProfile)
	}
	if out.ElevationProfile[0].DistanceM != 0 {
		t.Errorf("elevationProfile[0].distanceM = %v, want 0 at the start", out.ElevationProfile[0].DistanceM)
	}
}

// The road-type selector (apps/web/src/components/RouteBuilderPanel.vue)
// is only worth anything if the profile it sends actually reaches the
// routing engine — TestRouteBuilderPreviewRejectsAnUnsupportedProfile below
// only proves an invalid one is rejected, not that a valid one is forwarded.
func TestRouteBuilderPreviewForwardsChosenProfile(t *testing.T) {
	var got string
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route:     suggestFixtureRoute,
		onProfile: func(profile string) { got = profile },
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/preview", map[string]any{
		"waypoints": []map[string]float64{{"lat": 50.85, "lon": 4.35}, {"lat": 50.87, "lon": 4.37}},
		"profile":   "cycling-mountain",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got != "cycling-mountain" {
		t.Errorf("routing.Client.Route received profile %q, want %q", got, "cycling-mountain")
	}
}

func TestRouteBuilderPreviewRejectsAnUnsupportedProfile(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: suggestFixtureRoute,
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/preview", map[string]any{
		"waypoints": []map[string]float64{{"lat": 50.85, "lon": 4.35}, {"lat": 50.87, "lon": 4.37}},
		"profile":   "../admin",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unsupported profile", resp.StatusCode)
	}
}

func TestRouteBuilderPreviewRejectsTooManyWaypoints(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: suggestFixtureRoute,
	})

	// One more than the server's own maxRouteBuilderWaypoints (50).
	waypoints := make([]map[string]float64, 51)
	for i := range waypoints {
		waypoints[i] = map[string]float64{"lat": 50.85, "lon": 4.35}
	}

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/preview", map[string]any{
		"waypoints": waypoints,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for too many waypoints", resp.StatusCode)
	}
}

// A separate, inline harness rather than newRouteBuilderHarness above: this
// is the one test that needs a real (tiny) RouteBuilderLimiter, and every
// other test in this file relies on that field staying nil (unlimited) —
// see ratelimit.Limiter.Allow's own doc comment for why nil fails open.
func TestRouteBuilderPreviewIsRateLimited(t *testing.T) {
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	acct, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{
		Source:   db,
		Store:    st,
		Accounts: acct,
		Config:   &config.Config{},
		Routing: stubRoutingClient{
			route: suggestFixtureRoute,
		},
		RouteBuilderLimiter: ratelimit.New(1, time.Hour),
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	client := server.Client()

	body := map[string]any{
		"waypoints": []map[string]float64{{"lat": 50.85, "lon": 4.35}, {"lat": 50.87, "lon": 4.37}},
	}
	first := doJSON(t, client, http.MethodPost, server.URL+"/api/routebuilder/preview", body)
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", first.StatusCode)
	}

	second := doJSON(t, client, http.MethodPost, server.URL+"/api/routebuilder/preview", body)
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want %d", second.StatusCode, http.StatusTooManyRequests)
	}
}

func TestCreateRouteFromPointsSavesARoute(t *testing.T) {
	client, base := newRouteBuilderHarness(t, nil)

	resp := doJSON(t, client, http.MethodPost, base+"/api/routes/from-points", map[string]any{
		"name": "Built by hand",
		"points": []map[string]float64{
			{"lat": 50.85, "lon": 4.35},
			{"lat": 50.86, "lon": 4.36},
			{"lat": 50.87, "lon": 4.37},
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	var route struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&route); err != nil {
		t.Fatal(err)
	}
	if route.Name != "Built by hand" || route.Slug == "" {
		t.Fatalf("got route %+v, want a saved route named 'Built by hand'", route)
	}

	listResp, err := client.Get(base + "/api/routes")
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	var list struct {
		Routes []struct {
			Slug string `json:"slug"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range list.Routes {
		if r.Slug == route.Slug {
			found = true
		}
	}
	if !found {
		t.Errorf("saved route %q not found in the library listing", route.Slug)
	}
}

func TestCreateRouteFromPointsRejectsTooFewPoints(t *testing.T) {
	client, base := newRouteBuilderHarness(t, nil)

	resp := doJSON(t, client, http.MethodPost, base+"/api/routes/from-points", map[string]any{
		"name":   "Too short",
		"points": []map[string]float64{{"lat": 50.85, "lon": 4.35}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a single-point route", resp.StatusCode)
	}
}

func TestRouteBuilderSuggestRequiresARoutingEngine(t *testing.T) {
	client, base := newRouteBuilderHarness(t, nil)

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusPreconditionFailed)
	}
}

func TestRouteBuilderSuggestRejectsNonPositiveDistance(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: suggestFixtureRoute,
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 0,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-positive distance", resp.StatusCode)
	}
}

func TestRouteBuilderSuggestRejectsAnExcessiveDistance(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: suggestFixtureRoute,
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 100000,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a wildly excessive distance", resp.StatusCode)
	}
}

// Same reasoning as TestRouteBuilderPreviewForwardsChosenProfile, for the
// Suggest tab's own RoundTrip call.
func TestRouteBuilderSuggestForwardsChosenProfile(t *testing.T) {
	// A plain string/int written from onProfile/onHilliness below is a real
	// data race, not just untidy — the suggest handler now fires all
	// maxSuggestAttempts RoundTrip calls concurrently (see server.go's own
	// comment on why), so every one of these tests observes the callback
	// from multiple goroutines at once, even though every call in a given
	// test sees the identical value.
	var got atomic.Value
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route:     suggestFixtureRoute,
		onProfile: func(profile string) { got.Store(profile) },
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
		"profile":    "cycling-road",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := got.Load(); got != "cycling-road" {
		t.Errorf("routing.Client.RoundTrip received profile %q, want %q", got, "cycling-road")
	}
}

// The hilliness (fitness level) selector is only worth anything if what it
// sends actually reaches the routing engine — same reasoning as
// TestRouteBuilderSuggestForwardsChosenProfile above.
func TestRouteBuilderSuggestForwardsChosenHilliness(t *testing.T) {
	// -99: a value neither -1 (unspecified) nor 0-3 could ever be, so a
	// missed call is obvious. See TestRouteBuilderSuggestForwardsChosenProfile's
	// own comment on why this needs to be an atomic, not a plain int.
	var got atomic.Int32
	got.Store(-99)
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route:       suggestFixtureRoute,
		onHilliness: func(hilliness int) { got.Store(int32(hilliness)) },
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
		"hilliness":  0,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := got.Load(); got != 0 {
		t.Errorf("routing.Client.RoundTrip received hilliness %d, want the explicit 0 (Novice)", got)
	}
}

// An omitted hilliness must reach RoundTrip as -1 ("unspecified") rather
// than silently defaulting to 0 (Novice) at the JSON-decode boundary — a
// *int field distinguishes "not sent" from "sent as 0" for exactly this
// reason (see server.go's own comment on the request body's Hilliness field).
func TestRouteBuilderSuggestOmittedHillinessIsUnspecified(t *testing.T) {
	var got atomic.Int32
	got.Store(-99)
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route:       suggestFixtureRoute,
		onHilliness: func(hilliness int) { got.Store(int32(hilliness)) },
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := got.Load(); got != -1 {
		t.Errorf("routing.Client.RoundTrip received hilliness %d, want -1 (unspecified) for an omitted field", got)
	}
}

func TestRouteBuilderSuggestRejectsAnOutOfRangeHilliness(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: suggestFixtureRoute,
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
		"hilliness":  4,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an out-of-range hilliness", resp.StatusCode)
	}
}

// rideWithAscent is a 3-point route with a fixed shape (so every candidate
// built from it has the same distance) and a controlled total climb — the
// end-to-end fixture for TestRouteBuilderSuggestPicksBestFitByHilliness
// below, mirroring poolEntry's own reasoning in
// suggestselection_test.go's unit tests of selectByHilliness directly.
// rideWithAscent's own two hops are ~20.0km total — the same distance as
// suggestFixtureRoute (both computed from the same 0.09°-latitude step),
// so a pool built entirely from this stays within
// selectSuggestCandidates' own maxDistanceDeviation of a "distanceKm": 20
// request regardless of ascentM, which only changes the elevation, never
// the path.
func rideWithAscent(ascentM float64) []gpx.Point {
	return []gpx.Point{
		{Lat: 50.85, Lon: 4.35, Ele: 100, HasEle: true},
		{Lat: 50.94, Lon: 4.35, Ele: 100 + ascentM, HasEle: true},
		{Lat: 51.03, Lon: 4.35, Ele: 100 + ascentM, HasEle: true},
	}
}

// rideWithAscentFarFromTarget is rideWithAscent's own shape stretched out
// much longer, so selectSuggestCandidates' own distance tolerance always
// excludes it before hilliness selection ever runs — used to prove the
// tolerance filter actually rejects a candidate that fits the hilliness
// preference perfectly but landed nowhere near the requested distance.
func rideWithAscentFarFromTarget(ascentM float64) []gpx.Point {
	return []gpx.Point{
		{Lat: 50.85, Lon: 4.35, Ele: 100, HasEle: true},
		{Lat: 51.85, Lon: 5.35, Ele: 100 + ascentM, HasEle: true},
		{Lat: 52.87, Lon: 6.37, Ele: 100 + ascentM, HasEle: true},
	}
}

// The exact regression case the "Flat gave more height metres than Hilly"
// report described: several seeds succeed with a spread of real climbing
// amounts, and the hilliness preference must pick the best-fitting
// suggestCandidateCount out of that pool, not just the first few that
// happened to succeed (which, with ORS's own round_trip loop length
// varying independently of the hilliness weighting, was never reliably
// correlated with which setting a rider chose — see selectByHilliness's
// own doc comment). Each subtest's pool has one more in-tolerance entry
// than suggestCandidateCount needs (10, not 9), so there is always exactly
// one genuine exclusion to assert on — plus one entry pushed far from the
// requested distance, proving selectSuggestCandidates' own tolerance filter
// runs before hilliness ever gets a vote.
func TestRouteBuilderSuggestPicksBestFitByHilliness(t *testing.T) {
	suggest := func(t *testing.T, routeForCall map[int][]gpx.Point, hilliness int) []float64 {
		t.Helper()
		client, base := newRouteBuilderHarness(t, stubRoutingClient{
			callCount:    &atomic.Int32{},
			routeForCall: routeForCall,
		})
		resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
			"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
			"distanceKm": 20,
			"hilliness":  hilliness,
		})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var out struct {
			Candidates []struct {
				AscentM float64 `json:"ascentM"`
			} `json:"candidates"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if len(out.Candidates) != 9 {
			t.Fatalf("got %d candidates, want 9", len(out.Candidates))
		}
		got := make([]float64, len(out.Candidates))
		for i, c := range out.Candidates {
			got[i] = c.AscentM
		}
		return got
	}

	t.Run("flat picks the 9 lowest-climbing candidates", func(t *testing.T) {
		// 10 in-tolerance ascents (5..50 in steps of 5) — one more than
		// suggestCandidateCount, so excluding the single highest (50) is a
		// real, meaningful selection, not "the pool only had 9 anyway."
		// The far-from-target entry (999, an ascent value neither cluster
		// uses) proves the tolerance filter runs first — if it didn't,
		// this call being the single steepest of the whole set would win
		// a "very hilly" ranking outright.
		routeForCall := map[int][]gpx.Point{1: rideWithAscentFarFromTarget(999)}
		for i, ascent := range []float64{5, 10, 15, 20, 25, 30, 35, 40, 45, 50} {
			routeForCall[i+2] = rideWithAscent(ascent)
		}
		for _, a := range suggest(t, routeForCall, 0) {
			if a > 45 {
				t.Errorf("candidate ascent = %v, want one of the 9 lowest (5-45), not the excluded 50", a)
			}
		}
	})

	t.Run("very hilly picks the 9 highest-climbing candidates", func(t *testing.T) {
		// Mirrors the flat subtest: 10 in-tolerance ascents, excluding the
		// single lowest (60) is the real selection this time.
		routeForCall := map[int][]gpx.Point{1: rideWithAscentFarFromTarget(1)}
		for i, ascent := range []float64{60, 70, 80, 90, 100, 110, 120, 130, 140, 150} {
			routeForCall[i+2] = rideWithAscent(ascent)
		}
		for _, a := range suggest(t, routeForCall, 3) {
			if a < 70 {
				t.Errorf("candidate ascent = %v, want one of the 9 highest (70-150), not the excluded 60", a)
			}
		}
	})
}

// Found live on preview.domestique.dev: a single slow/flaky moment from
// ORS (three 20-second Client.Timeouts inside one request) made the whole
// suggest request take up to maxSuggestAttempts*requestTimeout when the 6
// attempts ran one after another — long enough to read as "stopped
// loading." This is the regression test for firing them concurrently
// instead: 6 attempts each sleeping 200ms take ~1.2s sequentially, but
// well under that run in parallel.
func TestRouteBuilderSuggestFiresAttemptsConcurrently(t *testing.T) {
	const delay = 200 * time.Millisecond
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: suggestFixtureRoute,
		delay: delay,
	})

	start := time.Now()
	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	})
	elapsed := time.Since(start)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// A generous ceiling (3 delays, not 6) — comfortably below "ran
	// sequentially" while tolerant of scheduling noise on a busy CI runner.
	if max := delay * 3; elapsed > max {
		t.Errorf("suggest request took %v, want well under %v (6 attempts of %v should run concurrently, not sequentially)", elapsed, max, delay)
	}
}

func TestRouteBuilderSuggestRejectsAnUnsupportedProfile(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: suggestFixtureRoute,
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
		"profile":    "../admin",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unsupported profile", resp.StatusCode)
	}
}

func TestRouteBuilderSuggestIsRateLimited(t *testing.T) {
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	acct, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{
		Source:   db,
		Store:    st,
		Accounts: acct,
		Config:   &config.Config{},
		Routing: stubRoutingClient{
			route: suggestFixtureRoute,
		},
		RouteBuilderLimiter: ratelimit.New(1, time.Hour),
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	client := server.Client()

	body := map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	}
	first := doJSON(t, client, http.MethodPost, server.URL+"/api/routebuilder/suggest", body)
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", first.StatusCode)
	}

	second := doJSON(t, client, http.MethodPost, server.URL+"/api/routebuilder/suggest", body)
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want %d", second.StatusCode, http.StatusTooManyRequests)
	}
}

func TestRouteBuilderSuggestReturnsNineCandidates(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: []gpx.Point{
			{Lat: 50.85, Lon: 4.35},
			{Lat: 50.86, Lon: 4.37},
			{Lat: 50.85, Lon: 4.35},
		},
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start": map[string]float64{"lat": 50.85, "lon": 4.35},
		// 3.58: haversine(50.85,4.35 -> 50.86,4.37 -> 50.85,4.35) — matches
		// the fixed stub route's own real distance, within
		// selectSuggestCandidates' own maxDistanceDeviation.
		"distanceKm": 3.58,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Candidates []struct {
			Points    [][2]float64 `json:"points"`
			DistanceM float64      `json:"distanceM"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Candidates) != 9 {
		t.Fatalf("got %d candidates, want 9", len(out.Candidates))
	}
	for i, c := range out.Candidates {
		if len(c.Points) != 3 || c.DistanceM <= 0 {
			t.Errorf("candidate %d = %+v, want 3 points and a positive distance", i, c)
		}
	}
}

// Each suggested candidate gets its own "type of ground" breakdown, the
// same as a manual preview — RouteCandidatePreview's own card shows this
// per candidate, not just an aggregate for the whole request.
func TestRouteBuilderSuggestCandidatesIncludeSurface(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: []gpx.Point{
			{Lat: 50.85, Lon: 4.35},
			{Lat: 50.86, Lon: 4.37},
			{Lat: 50.85, Lon: 4.35},
		},
		surface: []routing.SurfaceSummary{{Type: "Gravel", DistanceM: 500, Fraction: 1}},
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	})
	defer resp.Body.Close()

	var out struct {
		Candidates []struct {
			Surface []struct {
				Type string `json:"type"`
			} `json:"surface"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for i, c := range out.Candidates {
		if len(c.Surface) != 1 || c.Surface[0].Type != "Gravel" {
			t.Errorf("candidate %d surface = %+v, want [Gravel]", i, c.Surface)
		}
	}
}

// A fixed set of seeds would make every "Generate 3 options" click show the
// exact same three loops forever — server.go's suggestSeeds instead picks a
// fresh random base per request. Checked directly against what RoundTrip
// actually receives, not just trusting the doc comment.
func TestSuggestSeedsVaryBetweenRequests(t *testing.T) {
	var mu sync.Mutex
	var perRequest [][]int
	recordSeeds := func() *[]int {
		mu.Lock()
		defer mu.Unlock()
		perRequest = append(perRequest, nil)
		i := len(perRequest) - 1
		return &perRequest[i]
	}

	seeds := recordSeeds()
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: suggestFixtureRoute,
		onRoundTrip: func(seed int) {
			mu.Lock()
			defer mu.Unlock()
			*seeds = append(*seeds, seed)
		},
	})

	body := map[string]any{"start": map[string]float64{"lat": 50.85, "lon": 4.35}, "distanceKm": 20}
	doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", body).Body.Close()
	seeds = recordSeeds()
	doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", body).Body.Close()

	// server.go's own maxSuggestAttempts (15) — every attempt now always
	// runs, to build a pool selectSuggestCandidates picks the best-fitting
	// suggestCandidateCount from, rather than stopping as soon as enough
	// succeed. See that function's own doc comment for why.
	const wantSeedsPerRequest = 15
	if len(perRequest) != 2 || len(perRequest[0]) != wantSeedsPerRequest || len(perRequest[1]) != wantSeedsPerRequest {
		t.Fatalf("recorded seeds = %v, want two requests of %d seeds each", perRequest, wantSeedsPerRequest)
	}
	seen := map[int]bool{}
	for _, s := range perRequest[0] {
		if seen[s] {
			t.Errorf("seeds within one request = %v, want every value distinct", perRequest[0])
			break
		}
		seen[s] = true
	}
	if perRequest[0][0] == perRequest[1][0] {
		t.Errorf("both requests picked the same base seed (%d) — suggestSeeds is not varying between requests", perRequest[0][0])
	}
}

// A routing engine stumbling on one seed shouldn't cost a candidate at all
// — found live: ORS's own round_trip algorithm can pick a seed that lands
// on a genuinely unroutable point (a real 404 for one seed in three, the
// other two fine), which used to just mean 2 candidates shown instead of 3.
// The retry budget (maxSuggestAttempts) means a rider still gets all 3.
func TestRouteBuilderSuggestRetriesAFailedSeedToStillReachNineCandidates(t *testing.T) {
	var calls atomic.Int32
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route:        suggestFixtureRoute,
		failCallNums: map[int]bool{2: true},
		callCount:    &calls,
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with one failed seed", resp.StatusCode)
	}

	var out struct {
		Candidates []struct{} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Candidates) != 9 {
		t.Fatalf("got %d candidates, want 9 (the failed 2nd seed should have been retried)", len(out.Candidates))
	}
	// server.go's own maxSuggestAttempts, unexported and this file is
	// package api_test — kept as a literal, same as this file's other
	// references to unexported server.go behaviour.
	const wantAttempts = 15
	if got := calls.Load(); got != wantAttempts {
		// selectSuggestCandidates picks the best-fitting candidates out of
		// a full pool now, not just the first few that succeed — every
		// attempt in the budget runs regardless of early success, so a
		// failed seed being "retried" no longer means the request stops
		// early; it means 14 (not 15) of the 15 attempts landed a usable
		// path.
		t.Errorf("routing engine was called %d times, want %d (the full attempt budget)", got, wantAttempts)
	}
}

// A genuinely unlucky start point (very little rideable loop nearby at this
// distance) shouldn't retry forever — once maxSuggestAttempts is exhausted,
// the request still succeeds with whatever it found, same "one bad route
// never aborts a run" shape, just fewer than 3.
func TestRouteBuilderSuggestReturnsPartialWhenRetriesExhausted(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: suggestFixtureRoute,
		// 13 of the 15-attempt budget fail, leaving exactly 2 successes —
		// fewer than suggestCandidateCount (9), so the request still
		// returns whatever it found rather than erroring outright.
		failCallNums: map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true, 9: true, 10: true, 11: true, 12: true, 13: true},
		callCount:    &atomic.Int32{},
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the request itself still succeeds", resp.StatusCode)
	}

	var out struct {
		Candidates []struct{} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Candidates) != 2 {
		t.Fatalf("got %d candidates, want 2 (only the 14th and 15th attempts succeeded)", len(out.Candidates))
	}
}

func TestRouteBuilderSuggestFailsWhenEverySeedFails(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		err: errors.New("routing engine unreachable"),
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d when every seed fails", resp.StatusCode, http.StatusBadGateway)
	}
}

// Every attempt routing the engine's own way — no errors at all — but none
// of them land within maxDistanceDeviation of the requested distance. This
// is a distinct failure mode from TestRouteBuilderSuggestFailsWhenEverySeedFails
// above: lastErr is nil here (every RoundTrip call genuinely succeeded), so
// naively calling lastErr.Error() in that same 502 path would panic — this
// is the regression test for the dedicated nil check that avoids it.
func TestRouteBuilderSuggestFailsWhenNothingIsWithinDistanceTolerance(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		// Half of suggestFixtureRoute's own ~20km — every attempt succeeds,
		// but every one of them is 50% off a "distanceKm": 20 request,
		// nowhere near maxDistanceDeviation (10%).
		route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.94, Lon: 4.35}},
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d when nothing lands within the distance tolerance", resp.StatusCode, http.StatusBadGateway)
	}
}

// TestRouteBuilderSuggestRefinementRoundCompensatesForMeasuredOvershoot
// proves the two-round design end to end: found live (real ORS API, not
// assumed — see the calibration/refinement comment on the RoundTrip calls
// in handleRouteBuilderSuggest) that round_trip systematically overshoots
// its requested length, so a calibration round's own measured ratio has to
// actually reach the refinement round's request rather than just being
// computed and discarded. stubRoutingClient's fixed route stands in for
// "every calibration attempt comes back ~20% over the 20km target," which
// is exactly the shape a real biased routing engine produces.
func TestRouteBuilderSuggestRefinementRoundCompensatesForMeasuredOvershoot(t *testing.T) {
	// suggestFixtureRoute's own points span ~20015m (its doc comment); this
	// route's far point is pushed out by the same proportion (1.2x the
	// latitude delta) so the fixed route it returns for every attempt reads
	// as ~24km — a consistent ~20% overshoot of the 20km target below.
	overshootRoute := []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 51.066, Lon: 4.35}}

	var mu sync.Mutex
	var lengths []float64
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: overshootRoute,
		onLength: func(l float64) {
			mu.Lock()
			lengths = append(lengths, l)
			mu.Unlock()
		},
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
	})
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(lengths) != 15 {
		t.Fatalf("RoundTrip was called %d times, want 15 (5 calibration + 10 refinement)", len(lengths))
	}
	calibration, refinement := lengths[:5], lengths[5:]
	for _, l := range calibration {
		if l != 20000 {
			t.Errorf("calibration round requested %v, want the raw 20000m target uncompensated", l)
		}
	}
	for _, l := range refinement {
		if l >= 20000 {
			t.Errorf("refinement round requested %v, want less than the 20000m target given the measured ~20%% overshoot", l)
		}
	}
}

// fakeGeocoder substitutes for a real Nominatim instance in tests, the same
// reason stubRoutingClient substitutes for a real ORS one.
type fakeGeocoder struct {
	results []geocoding.Result
	err     error
}

func (f fakeGeocoder) Search(context.Context, string) ([]geocoding.Result, error) {
	return f.results, f.err
}

func newGeocodeHarness(t *testing.T, geocoder geocoding.Client) (client *http.Client, base string) {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	acct, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{
		Source:   db,
		Store:    st,
		Accounts: acct,
		Config:   &config.Config{},
		Geocoder: geocoder,
	}

	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	return server.Client(), server.URL
}

func TestGeocodeSearchReturnsResults(t *testing.T) {
	client, base := newGeocodeHarness(t, fakeGeocoder{
		results: []geocoding.Result{{Name: "Brussels, Belgium", Lat: 50.85, Lon: 4.35}},
	})

	resp, err := client.Get(base + "/api/geocode?q=Brussels")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Results []struct {
			Name string  `json:"name"`
			Lat  float64 `json:"lat"`
			Lon  float64 `json:"lon"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].Name != "Brussels, Belgium" {
		t.Errorf("results = %+v, want one Brussels result", out.Results)
	}
}

func TestGeocodeSearchRejectsAnEmptyQuery(t *testing.T) {
	client, base := newGeocodeHarness(t, fakeGeocoder{})

	resp, err := client.Get(base + "/api/geocode?q=")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d for an empty query", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestGeocodeSearchRequiresAGeocoder(t *testing.T) {
	client, base := newGeocodeHarness(t, nil)

	resp, err := client.Get(base + "/api/geocode?q=Brussels")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
}

func TestGeocodeSearchFailurePropagatesAsBadGateway(t *testing.T) {
	client, base := newGeocodeHarness(t, fakeGeocoder{err: errors.New("nominatim unreachable")})

	resp, err := client.Get(base + "/api/geocode?q=Brussels")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
}

// GeocodeGlobalLimiter protects Nominatim's own shared usage policy across
// every rider, not any one rider's own budget — checked here with
// GeocodeLimiter left nil (unlimited) so only the global limiter can be
// what blocks the second request.
func TestGeocodeSearchIsRateLimitedGlobally(t *testing.T) {
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	acct, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{
		Source:               db,
		Store:                st,
		Accounts:             acct,
		Config:               &config.Config{},
		Geocoder:             fakeGeocoder{results: []geocoding.Result{{Name: "Brussels", Lat: 50.85, Lon: 4.35}}},
		GeocodeGlobalLimiter: ratelimit.New(1, time.Hour),
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	client := server.Client()

	first, err := client.Get(server.URL + "/api/geocode?q=Brussels")
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", first.StatusCode)
	}

	second, err := client.Get(server.URL + "/api/geocode?q=Ghent")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want %d — a different query should still share the global budget", second.StatusCode, http.StatusTooManyRequests)
	}
}
