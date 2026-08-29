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
//
// Every request also avoids the road features (see avoidFeatures) a bike
// genuinely cannot or should not be routed onto — steps and unbridged
// fords. There is no lever beyond that to bias a cycling profile *toward*
// cycle paths specifically: ORS validates avoid_features per profile
// category, and "highways" (the obvious-looking choice) is rejected
// outright for every cycling-* profile — confirmed live against the real
// API, not assumed, after shipping it once already broke every suggestion
// request in production. cycling-regular's own bias toward cycleways and
// lanes is baked into the profile's routing cost function itself, not
// something this client can push further via the API.
package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
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

// Path is what a routing engine hands back for one request: the geometry
// itself, plus what it's made of — the route builder's own "type of
// ground" figure, alongside distance/ascent (gpx.ComputeStats, derived from
// Points) and the map line (also Points). Requested on the same call that
// already asks for elevation, not a second lookup.
type Path struct {
	Points []gpx.Point
	// Surface breaks the route down by ORS's own surface classification
	// (asphalt, gravel, dirt, ...), most-distance-first. Empty when ORS has
	// no surface data for a stretch of road (common outside well-mapped
	// areas) rather than an error — a route builder result is still useful
	// without it.
	Surface []SurfaceSummary
}

// SurfaceSummary is one surface type's share of a route's own distance —
// ORS's own extras.surface.summary entry, translated from its numeric code
// (see surfaceLabels) to a human label.
type SurfaceSummary struct {
	Type      string
	DistanceM float64
	// Fraction is 0-1, not 0-100 — ORS's own "amount" field is a percentage,
	// converted once here so nothing downstream has to remember which unit
	// this is in.
	Fraction float64
}

// Client turns waypoints or a starting point into a rideable path. The one
// real implementation is ORSClient; the interface exists so acceptance
// tests can substitute a fake, the same reason api.Server.TargetFactory
// exists.
type Client interface {
	// Route snaps a path through every waypoint in order, for the manual
	// route builder.
	Route(ctx context.Context, waypoints []LatLng, profile string) (Path, error)
	// RoundTrip returns one candidate loop of about distanceM, starting and
	// ending at start. seed varies the loop's shape so a caller generating
	// several candidates (the suggested and AI-native builders both call
	// this three times) gets genuinely different ones, not the same loop
	// three times over.
	RoundTrip(ctx context.Context, start LatLng, distanceM float64, seed int, profile string) (Path, error)
}

// ORSClient calls OpenRouteService's directions API.
type ORSClient struct {
	url        string
	apiKey     string
	httpClient *http.Client
	cache      *responseCache
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
		cache: newResponseCache(),
	}
}

// cacheTTL and cacheMaxEntries bound responseCache below. An hour is
// generous rather than exact: ORS routes the same coordinates against the
// same road graph, which does not change on human timescales, so nothing is
// lost by serving a same-input request out of cache well after the request
// that first computed it.
const (
	cacheTTL        = time.Hour
	cacheMaxEntries = 256
)

// responseCache is a small bounded, in-memory cache of ORS responses,
// keyed by the exact request that produced them. The API key this client
// carries is rate-limited (a free-tier quota, shared across every rider
// using this deployment's route builder), and the UI already produces
// plenty of legitimate exact repeats of a request already answered: a
// debounced preview firing again for waypoints that did not actually
// change, or an undo landing back on an earlier waypoint set. Caching
// those costs nothing and measurably reduces how fast the quota gets
// spent. (A repeated "Generate 3 options" click deliberately does *not*
// hit this cache — api/server.go's own suggestSeeds picks a fresh random
// base each call precisely so pressing Generate again shows different
// loops, not the same three answered from cache.) This lives here, on the
// concrete ORSClient, rather than as a decorator around the Client
// interface, so the fake Client acceptance tests substitute in place of a
// real one stays simple and does not need to know caching exists.
//
// resolve also coalesces concurrent identical requests — a double-click on
// Generate, or the same debounced preview firing twice in close succession
// — behind one real call: without that, two callers that both miss the
// cache in the same instant would each spend the quota independently,
// undermining the entire point of caching in the first place.
type responseCache struct {
	mu       sync.Mutex
	entries  map[string]cacheEntry
	inFlight map[string]*sync.WaitGroup
}

type cacheEntry struct {
	path    Path
	expires time.Time
}

func newResponseCache() *responseCache {
	return &responseCache{
		entries:  make(map[string]cacheEntry),
		inFlight: make(map[string]*sync.WaitGroup),
	}
}

// clone deep-copies a Path's slices — every cache read/write goes through
// this so two callers sharing a cached entry never alias, and mutating one
// caller's copy can never corrupt what the cache (or another caller) holds.
func (p Path) clone() Path {
	return Path{
		Points:  append([]gpx.Point(nil), p.Points...),
		Surface: append([]SurfaceSummary(nil), p.Surface...),
	}
}

func (c *responseCache) get(key string) (Path, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expires) {
		return Path{}, false
	}
	return entry.path.clone(), true
}

func (c *responseCache) put(key string, path Path) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= cacheMaxEntries {
		// Evict an arbitrary entry rather than tracking real LRU order —
		// this cache only exists to catch exact repeats of a recent
		// request, not to be a precise working set, so a random eviction
		// under pressure is enough to keep memory bounded.
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = cacheEntry{path: path.clone(), expires: time.Now().Add(cacheTTL)}
}

// resolve returns the cached value for key if there is one, otherwise calls
// fetch — but only once per key even under concurrent callers. A caller
// that arrives while another is already fetching the same key waits for
// that call to finish and then re-checks the cache, rather than starting a
// second, redundant outbound request of its own. If the winning call
// failed (nothing to cache), a waiter falls through and becomes the next
// attempt itself instead of propagating a stranger's error.
func (c *responseCache) resolve(key string, fetch func() (Path, error)) (Path, error) {
	if path, ok := c.get(key); ok {
		return path, nil
	}

	c.mu.Lock()
	if wg, waiting := c.inFlight[key]; waiting {
		c.mu.Unlock()
		wg.Wait()
		if path, ok := c.get(key); ok {
			return path, nil
		}
		return c.resolve(key, fetch)
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.inFlight[key] = wg
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.inFlight, key)
		c.mu.Unlock()
		wg.Done()
	}()

	path, err := fetch()
	if err != nil {
		return Path{}, err
	}
	c.put(key, path)
	return path, nil
}

// cacheKey identifies a directions request by everything that can change
// its answer: the profile (a different profile can route differently) and
// the coordinates/round-trip options already resolved onto the request
// body. Built from the request actually sent, after profile defaulting, so
// an explicit "" and an explicit DefaultProfile share one cache entry
// rather than two.
func cacheKey(profile string, body directionsRequest) string {
	var b strings.Builder
	b.WriteString(profile)
	for _, coord := range body.Coordinates {
		fmt.Fprintf(&b, "|%.6f,%.6f", coord[0], coord[1])
	}
	if opts := body.Options; opts != nil {
		if rt := opts.RoundTrip; rt != nil {
			fmt.Fprintf(&b, "|rt:%.0f:%d:%d", rt.Length, rt.Points, rt.Seed)
		}
		if len(opts.AvoidFeatures) > 0 {
			// avoidFeatures is the only value used anywhere today, so this
			// never actually distinguishes two requests in practice — but a
			// future caller that varies it must not silently collide with a
			// cached answer computed under a different constraint.
			b.WriteString("|avoid:")
			b.WriteString(strings.Join(opts.AvoidFeatures, ","))
		}
	}
	return b.String()
}

type directionsRequest struct {
	Coordinates [][2]float64    `json:"coordinates"`
	Options     *directionsOpts `json:"options,omitempty"`
	// Elevation asks ORS to append a third coordinate (metres) to every
	// point in the response geometry — the route builder's own height-gain
	// figure (gpx.ComputeStats' AscentM) is derived from this rather than a
	// second, separate elevation lookup: the routing engine already has to
	// walk this exact path, so asking it for elevation too is free, unlike
	// spending a second outbound call against a different service for data
	// this same response can carry.
	Elevation bool `json:"elevation,omitempty"`
	// ExtraInfo asks ORS for additional per-segment classification —
	// "surface" is the route builder's own "type of ground" figure (Path.
	// Surface below), same free-on-the-same-call reasoning as Elevation.
	ExtraInfo []string `json:"extra_info,omitempty"`
}

type directionsOpts struct {
	RoundTrip *roundTripOpts `json:"round_trip,omitempty"`
	// AvoidFeatures steers the routing engine away from features a bike
	// genuinely cannot or should not be routed onto — set on every request
	// (Route and RoundTrip both). ORS validates this list per profile
	// category, not universally: "highways"/"tollways" are driving-only and
	// rejected outright for every cycling-* profile (HTTP 400, error code
	// 2003, confirmed against the real API) — found live, after shipping
	// exactly that and breaking every suggestion request in production.
	// "steps"/"fords"/"ferries" are the ones cycling profiles actually
	// accept; avoidFeatures below sticks to steps and fords, the two that
	// are genuine obstacles rather than a legitimate route choice.
	AvoidFeatures []string `json:"avoid_features,omitempty"`
}

// avoidFeatures is applied to every directions request this client makes —
// see AvoidFeatures' own doc comment for why these two and not others.
var avoidFeatures = []string{"steps", "fords"}

// extraInfo is requested on every directions request — see Path.Surface's
// own doc comment. "waytype" (cycleway/track/street) was left out
// deliberately: the route builder's own ask was ground type, not road
// class, and every extra_info value ORS is asked for is one more thing a
// self-hosted instance might not have (see the warning handling below).
var extraInfo = []string{"surface"}

type roundTripOpts struct {
	Length float64 `json:"length"`
	Points int     `json:"points"`
	Seed   int     `json:"seed"`
}

// surfaceLabels translates ORS's own numeric surface codes to a human
// label — confirmed against the real API's response, not the fixed
// "highways" mistake this package shipped once already: this table is the
// documented ORS Surface ID list
// (https://giscience.github.io/openrouteservice/api-reference/endpoints/directions/extra-info/surface),
// not a guess. code 0 (Unknown) is included on purpose — ORS returns it for
// real, common stretches (unmapped surface tags), and silently dropping it
// would understate a route's own uncertain-surface distance rather than
// naming it.
var surfaceLabels = map[int]string{
	0:  "Unknown",
	1:  "Paved",
	2:  "Unpaved",
	3:  "Asphalt",
	4:  "Concrete",
	5:  "Cobblestone",
	6:  "Metal",
	7:  "Wood",
	8:  "Compacted Gravel",
	9:  "Fine Gravel",
	10: "Gravel",
	11: "Dirt",
	12: "Ground",
	13: "Ice",
	14: "Paving Stones",
	15: "Sand",
	16: "Woodchips",
	17: "Grass",
	18: "Grass Paver",
}

type directionsResponse struct {
	Features []struct {
		Geometry struct {
			Coordinates [][]float64 `json:"coordinates"`
		} `json:"geometry"`
		Properties struct {
			Extras struct {
				Surface struct {
					Summary []struct {
						Value    float64 `json:"value"`
						Distance float64 `json:"distance"`
						Amount   float64 `json:"amount"`
					} `json:"summary"`
				} `json:"surface"`
			} `json:"extras"`
		} `json:"properties"`
	} `json:"features"`
}

func (c *ORSClient) Route(ctx context.Context, waypoints []LatLng, profile string) (Path, error) {
	if len(waypoints) < 2 {
		return Path{}, fmt.Errorf("routing: need at least 2 waypoints, got %d", len(waypoints))
	}
	coords := make([][2]float64, len(waypoints))
	for i, w := range waypoints {
		coords[i] = [2]float64{w.Lon, w.Lat}
	}
	req := directionsRequest{
		Coordinates: coords,
		Options:     &directionsOpts{AvoidFeatures: avoidFeatures},
		Elevation:   true,
		ExtraInfo:   extraInfo,
	}
	return c.directions(ctx, profile, req)
}

func (c *ORSClient) RoundTrip(ctx context.Context, start LatLng, distanceM float64, seed int, profile string) (Path, error) {
	if distanceM <= 0 {
		return Path{}, fmt.Errorf("routing: distance must be positive, got %.0fm", distanceM)
	}
	req := directionsRequest{
		Coordinates: [][2]float64{{start.Lon, start.Lat}},
		Options: &directionsOpts{
			RoundTrip: &roundTripOpts{
				Length: distanceM,
				Points: roundTripPoints,
				Seed:   seed,
			},
			AvoidFeatures: avoidFeatures,
		},
		Elevation: true,
		ExtraInfo: extraInfo,
	}
	return c.directions(ctx, profile, req)
}

func (c *ORSClient) directions(ctx context.Context, profile string, body directionsRequest) (Path, error) {
	if profile == "" {
		profile = DefaultProfile
	}
	if !ValidProfiles[profile] {
		return Path{}, fmt.Errorf("routing: unsupported profile %q", profile)
	}

	key := cacheKey(profile, body)
	return c.cache.resolve(key, func() (Path, error) {
		return c.fetchDirections(ctx, profile, body)
	})
}

func (c *ORSClient) fetchDirections(ctx context.Context, profile string, body directionsRequest) (Path, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Path{}, err
	}

	url := fmt.Sprintf("%s/v2/directions/%s/geojson", c.url, profile)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Path{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		// ORS wants the raw key as the header value, not a Bearer-prefixed one.
		req.Header.Set("Authorization", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Path{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// The url is operator config, not attacker input — but a self-hosted
		// instance an operator points this at is still a third party from
		// this process's own point of view, so the error body is capped the
		// same way elevation.Client's own response reading is.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return Path{}, fmt.Errorf("routing service returned %s: %s", resp.Status, bytes.TrimSpace(msg))
	}

	var out directionsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return Path{}, fmt.Errorf("decode routing response: %w", err)
	}
	if len(out.Features) == 0 || len(out.Features[0].Geometry.Coordinates) < 2 {
		return Path{}, fmt.Errorf("routing service returned no usable route")
	}
	feature := out.Features[0]

	coords := feature.Geometry.Coordinates
	points := make([]gpx.Point, len(coords))
	for i, coord := range coords {
		if len(coord) < 2 {
			return Path{}, fmt.Errorf("routing service returned a malformed coordinate")
		}
		points[i] = gpx.Point{Lat: coord[1], Lon: coord[0]}
		// The elevation request above asks for a third value; a self-hosted
		// instance an operator forgot to enable elevation support on still
		// answers, just without it — degrade to "no elevation" rather than
		// treat a 2-element coordinate as an error.
		if len(coord) >= 3 {
			points[i].Ele, points[i].HasEle = coord[2], true
		}
	}

	// Same degrade-rather-than-error shape as elevation above: a
	// self-hosted instance without surface data for this stretch of road
	// (or without the extra_info feature enabled at all) just means an
	// empty breakdown, not a failed request.
	summary := feature.Properties.Extras.Surface.Summary
	surface := make([]SurfaceSummary, 0, len(summary))
	for _, s := range summary {
		label, known := surfaceLabels[int(s.Value)]
		if !known {
			// A future ORS release adding a code this table doesn't have
			// yet — show the raw distance under a label that says so,
			// rather than silently dropping real distance from the total.
			label = fmt.Sprintf("Unrecognised (%d)", int(s.Value))
		}
		surface = append(surface, SurfaceSummary{
			Type:      label,
			DistanceM: s.Distance,
			Fraction:  s.Amount / 100,
		})
	}
	sort.Slice(surface, func(i, j int) bool { return surface[i].DistanceM > surface[j].DistanceM })

	return Path{Points: points, Surface: surface}, nil
}
