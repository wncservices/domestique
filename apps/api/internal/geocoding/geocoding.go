// Package geocoding turns a place name into a location, for the route
// builder's own location search — the fallback offered whenever a rider's
// browser has no geolocation, or they simply want to start looking somewhere
// other than wherever they are. Deliberately not the routing engine's own
// geocoder: internal/routing's ORS API key is rate-limited and shared across
// every rider using the route builder, and a place search is a different
// kind of request than a route computation — no reason to spend that same
// budget on this too. Nominatim (OpenStreetMap's own, free, keyless
// geocoder) is the one dependency this package needs instead.
//
// Unlike elevation and routing, this needs no enabled/disabled config
// toggle: it never runs on its own initiative — only in direct response to
// a rider typing a search and pressing the button — and needs no
// credential, so there is nothing an operator has to opt into before it's
// safe to expose.
package geocoding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// DefaultURL is the public Nominatim instance.
const DefaultURL = "https://nominatim.openstreetmap.org"

// userAgent identifies this app to Nominatim, per its usage policy
// (https://operations.osmfoundation.org/policies/nominatim/), which
// requires a valid HTTP Referer or User-Agent identifying the application —
// an anonymous client is liable to be blocked outright.
const userAgent = "Domestique (https://github.com/wncservices/domestique)"

const requestTimeout = 10 * time.Second

// maxResults caps how many candidates a single search returns — the route
// builder only ever shows the top match today, but a small handful (rather
// than one) leaves room for a future disambiguation UI without another
// round trip.
const maxResults = 5

// cacheTTL and cacheMaxEntries bound the response cache below — generous
// rather than exact, the same reasoning as internal/routing's own cache:
// the same place name resolves to the same coordinates on human timescales,
// and Nominatim's own usage policy caps this app to roughly one request a
// second across every rider, so serving a repeated search out of cache
// (a rider re-running a search, or two riders searching the same city)
// costs nothing and keeps this app comfortably under that limit.
const (
	cacheTTL        = time.Hour
	cacheMaxEntries = 256
)

// Result is one candidate location.
type Result struct {
	Name string
	Lat  float64
	Lon  float64
}

// Client searches for a place name. The one real implementation is
// NominatimClient; the interface exists so acceptance tests can substitute
// a fake, the same reason routing.Client and api.Server.TargetFactory exist.
type Client interface {
	Search(ctx context.Context, query string) ([]Result, error)
}

// NominatimClient calls Nominatim's search API.
type NominatimClient struct {
	url        string
	httpClient *http.Client
	cache      *responseCache
}

// New builds a NominatimClient. An empty url falls back to DefaultURL.
func New(rawURL string) *NominatimClient {
	if rawURL == "" {
		rawURL = DefaultURL
	}
	return &NominatimClient{
		url: strings.TrimSuffix(rawURL, "/"),
		httpClient: &http.Client{
			Timeout:   requestTimeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
		cache: newResponseCache(),
	}
}

type searchResult struct {
	DisplayName string `json:"display_name"`
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
}

// Search returns candidate locations matching query, best match first.
// Empty/whitespace-only queries are rejected before ever reaching Nominatim
// — the same "refuse before calling out" shape as a missing routing API key.
func (c *NominatimClient) Search(ctx context.Context, query string) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("geocoding: query must not be empty")
	}

	key := strings.ToLower(query)
	if results, ok := c.cache.get(key); ok {
		return results, nil
	}

	reqURL := fmt.Sprintf("%s/search?q=%s&format=jsonv2&limit=%d", c.url, url.QueryEscape(query), maxResults)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("geocoding service returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	var raw []searchResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode geocoding response: %w", err)
	}

	results := make([]Result, 0, len(raw))
	for _, r := range raw {
		var lat, lon float64
		if _, err := fmt.Sscanf(r.Lat, "%f", &lat); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(r.Lon, "%f", &lon); err != nil {
			continue
		}
		results = append(results, Result{Name: r.DisplayName, Lat: lat, Lon: lon})
	}

	c.cache.put(key, results)
	return results, nil
}

type responseCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	results []Result
	expires time.Time
}

func newResponseCache() *responseCache {
	return &responseCache{entries: make(map[string]cacheEntry)}
}

func (c *responseCache) get(key string) ([]Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return append([]Result(nil), entry.results...), true
}

func (c *responseCache) put(key string, results []Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= cacheMaxEntries {
		// Evict an arbitrary entry — same reasoning as internal/routing's
		// own cache: this only exists to catch exact repeats, not to be a
		// precise working set.
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = cacheEntry{results: append([]Result(nil), results...), expires: time.Now().Add(cacheTTL)}
}
