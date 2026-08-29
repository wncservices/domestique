// Package routing turns waypoints, or a starting point and a target
// distance, into an actual rideable path — the foundation the manual,
// suggested and AI-native route builders all sit on.
//
// This is deliberately never asked to go the other way: a language model
// (the AI-native builder's own job) cannot draw a rideable route — it knows
// nothing about which roads exist or connect, so asking one for coordinates
// directly produces a line that ignores rivers, motorways and dead ends.
// Every builder's job ends at a distance/hilliness/waypoint choice; this
// package is what turns that choice into geometry a real bike can follow.
//
// Off by default (config.RoutingConfig.Enabled), the same reasoning as
// internal/elevation: this is the other feature in this codebase that sends
// a route's own coordinates to a service outside the deployment on its own
// initiative, not because a rider asked to connect an account.
package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

// DefaultURL is the public OpenRouteService instance — free (with a signup
// and an API key, unlike Open-Elevation's fully anonymous one), open
// source, and self-hostable via the same openrouteservice/openrouteservice
// image for an operator who would rather not depend on it. This package
// only ever needs a URL that speaks its directions API.
const DefaultURL = "https://api.openrouteservice.org"

// DefaultProfile is used whenever a caller leaves profile empty. Every
// route builder in this app is cycling-only, per AGENTS.md's own note that
// this has never produced any other kind of route.
const DefaultProfile = "cycling-regular"

// ValidProfiles is the fixed set of real ORS cycling profiles a caller may
// request — checked in directions below before profile ever reaches the
// outbound request URL. Found live: profile arrives as a plain string on
// an HTTP request body (see api.handleRouteBuilderPreview/Suggest) with
// nothing upstream constraining it, and directions splices it straight
// into a URL path with no escaping — an unvalidated value there is
// attacker-controlled request content reaching this app's own
// routing-engine credentials, not just a cosmetic input-shape concern.
var ValidProfiles = map[string]bool{
	"cycling-regular":  true,
	"cycling-road":     true,
	"cycling-mountain": true,
	"cycling-electric": true,
}

// EnvAPIKey is where the routing engine's API key comes from — never
// domestique.yaml, the same rule as GARMIN_OAUTH_CONSUMER_KEY and
// KOMOOT_EMAIL/PASSWORD. Even the public instance's free tier requires one.
// #nosec G101 -- this is the *name* of an environment variable, not a
// credential. The value it names is deliberately not in this repository.
const EnvAPIKey = "DOMESTIQUE_ROUTING_API_KEY"

const requestTimeout = 20 * time.Second

// roundTripPoints is how many turns ORS's own round_trip algorithm should
// aim to shape the loop with — a fixed, reasonable middle ground, not
// something any caller in this app has a reason to vary.
const roundTripPoints = 5

// LatLng is one location, lat then lon — the order humans say it. ORS
// itself wants a [lon, lat] array; that flip happens only at the JSON
// boundary in this file, never in a caller-facing type.
type LatLng struct{ Lat, Lon float64 }

// Client turns waypoints or a starting point into a rideable path. The one
// real implementation is ORSClient; the interface exists so acceptance
// tests can substitute a fake, the same reason api.Server.TargetFactory
// exists.
type Client interface {
	// Route snaps a path through every waypoint in order, for the manual
	// route builder.
	Route(ctx context.Context, waypoints []LatLng, profile string) ([]gpx.Point, error)
	// RoundTrip returns one candidate loop of about distanceM, starting and
	// ending at start. seed varies the loop's shape so a caller generating
	// several candidates (the suggested and AI-native builders both call
	// this three times) gets genuinely different ones, not the same loop
	// three times over.
	RoundTrip(ctx context.Context, start LatLng, distanceM float64, seed int, profile string) ([]gpx.Point, error)
}

// ORSClient calls OpenRouteService's directions API.
type ORSClient struct {
	url        string
	apiKey     string
	httpClient *http.Client
}

// New builds an ORSClient. An empty url falls back to DefaultURL.
func New(url, apiKey string) *ORSClient {
	if url == "" {
		url = DefaultURL
	}
	return &ORSClient{
		url:    strings.TrimSuffix(url, "/"),
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout:   requestTimeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

type directionsRequest struct {
	Coordinates [][2]float64    `json:"coordinates"`
	Options     *directionsOpts `json:"options,omitempty"`
}

type directionsOpts struct {
	RoundTrip *roundTripOpts `json:"round_trip"`
}

type roundTripOpts struct {
	Length float64 `json:"length"`
	Points int     `json:"points"`
	Seed   int     `json:"seed"`
}

type directionsResponse struct {
	Features []struct {
		Geometry struct {
			Coordinates [][]float64 `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

func (c *ORSClient) Route(ctx context.Context, waypoints []LatLng, profile string) ([]gpx.Point, error) {
	if len(waypoints) < 2 {
		return nil, fmt.Errorf("routing: need at least 2 waypoints, got %d", len(waypoints))
	}
	coords := make([][2]float64, len(waypoints))
	for i, w := range waypoints {
		coords[i] = [2]float64{w.Lon, w.Lat}
	}
	return c.directions(ctx, profile, directionsRequest{Coordinates: coords})
}

func (c *ORSClient) RoundTrip(ctx context.Context, start LatLng, distanceM float64, seed int, profile string) ([]gpx.Point, error) {
	if distanceM <= 0 {
		return nil, fmt.Errorf("routing: distance must be positive, got %.0fm", distanceM)
	}
	req := directionsRequest{
		Coordinates: [][2]float64{{start.Lon, start.Lat}},
		Options: &directionsOpts{RoundTrip: &roundTripOpts{
			Length: distanceM,
			Points: roundTripPoints,
			Seed:   seed,
		}},
	}
	return c.directions(ctx, profile, req)
}

func (c *ORSClient) directions(ctx context.Context, profile string, body directionsRequest) ([]gpx.Point, error) {
	if profile == "" {
		profile = DefaultProfile
	}
	if !ValidProfiles[profile] {
		return nil, fmt.Errorf("routing: unsupported profile %q", profile)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/v2/directions/%s/geojson", c.url, profile)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		// ORS wants the raw key as the header value, not a Bearer-prefixed one.
		req.Header.Set("Authorization", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// The url is operator config, not attacker input — but a self-hosted
		// instance an operator points this at is still a third party from
		// this process's own point of view, so the error body is capped the
		// same way elevation.Client's own response reading is.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("routing service returned %s: %s", resp.Status, bytes.TrimSpace(msg))
	}

	var out directionsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode routing response: %w", err)
	}
	if len(out.Features) == 0 || len(out.Features[0].Geometry.Coordinates) < 2 {
		return nil, fmt.Errorf("routing service returned no usable route")
	}

	coords := out.Features[0].Geometry.Coordinates
	points := make([]gpx.Point, len(coords))
	for i, c := range coords {
		if len(c) < 2 {
			return nil, fmt.Errorf("routing service returned a malformed coordinate")
		}
		points[i] = gpx.Point{Lat: c[1], Lon: c[0]}
	}
	return points, nil
}
