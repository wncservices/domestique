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
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/routing"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

// stubRoutingClient substitutes for a real ORS instance in tests, the same
// reason fakeTarget substitutes for a real Garmin/Wahoo adapter — it always
// returns the same fixed route regardless of what it's asked to snap or
// generate, which is all TestRouteBuilderPreviewSnapsWaypoints needs.
type stubRoutingClient struct {
	route []gpx.Point
	err   error
}

func (s stubRoutingClient) Route(context.Context, []routing.LatLng, string) ([]gpx.Point, error) {
	return s.route, s.err
}

func (s stubRoutingClient) RoundTrip(context.Context, routing.LatLng, float64, int, string) ([]gpx.Point, error) {
	return s.route, s.err
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
