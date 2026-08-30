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
}

func (s stubRoutingClient) Route(_ context.Context, _ []routing.LatLng, profile string) (routing.Path, error) {
	if s.onProfile != nil {
		s.onProfile(profile)
	}
	return routing.Path{Points: s.route, Surface: s.surface}, s.err
}

func (s stubRoutingClient) RoundTrip(_ context.Context, _ routing.LatLng, _ float64, seed int, profile string) (routing.Path, error) {
	if s.onRoundTrip != nil {
		s.onRoundTrip(seed)
	}
	if s.onProfile != nil {
		s.onProfile(profile)
	}
	if s.callCount != nil {
		n := int(s.callCount.Add(1))
		if s.failCallNums[n] {
			return routing.Path{}, errors.New("stub: this call is configured to fail")
		}
	}
	return routing.Path{Points: s.route, Surface: s.surface}, s.err
}

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
		route:     []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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
		route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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
		route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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
			route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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
		route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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
		route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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
	var got string
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route:     []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
		onProfile: func(profile string) { got = profile },
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
	if got != "cycling-road" {
		t.Errorf("routing.Client.RoundTrip received profile %q, want %q", got, "cycling-road")
	}
}

func TestRouteBuilderSuggestRejectsAnUnsupportedProfile(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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
			route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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

func TestRouteBuilderSuggestReturnsThreeCandidates(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route: []gpx.Point{
			{Lat: 50.85, Lon: 4.35},
			{Lat: 50.86, Lon: 4.37},
			{Lat: 50.85, Lon: 4.35},
		},
	})

	resp := doJSON(t, client, http.MethodPost, base+"/api/routebuilder/suggest", map[string]any{
		"start":      map[string]float64{"lat": 50.85, "lon": 4.35},
		"distanceKm": 20,
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
	if len(out.Candidates) != 3 {
		t.Fatalf("got %d candidates, want 3", len(out.Candidates))
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
		route: []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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

	if len(perRequest) != 2 || len(perRequest[0]) != 3 || len(perRequest[1]) != 3 {
		t.Fatalf("recorded seeds = %v, want two requests of three seeds each", perRequest)
	}
	if perRequest[0][0] == perRequest[0][1] || perRequest[0][1] == perRequest[0][2] {
		t.Errorf("seeds within one request = %v, want three distinct values", perRequest[0])
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
func TestRouteBuilderSuggestRetriesAFailedSeedToStillReachThreeCandidates(t *testing.T) {
	var calls atomic.Int32
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route:        []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
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
	if len(out.Candidates) != 3 {
		t.Fatalf("got %d candidates, want 3 (the failed 2nd seed should have been retried)", len(out.Candidates))
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("routing engine was called %d times, want 4 (3 successes + the 1 retried failure)", got)
	}
}

// A genuinely unlucky start point (very little rideable loop nearby at this
// distance) shouldn't retry forever — once maxSuggestAttempts is exhausted,
// the request still succeeds with whatever it found, same "one bad route
// never aborts a run" shape, just fewer than 3.
func TestRouteBuilderSuggestReturnsPartialWhenRetriesExhausted(t *testing.T) {
	client, base := newRouteBuilderHarness(t, stubRoutingClient{
		route:        []gpx.Point{{Lat: 50.85, Lon: 4.35}, {Lat: 50.86, Lon: 4.36}},
		failCallNums: map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true},
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
	if len(out.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1 (only the 6th and final attempt succeeded)", len(out.Candidates))
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
