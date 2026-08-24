// Package elevation backfills a route's elevation from a third-party
// terrain API, for routes gpx.NeedsElevation flags as carrying no usable
// elevation of their own — a route drawn on a planning site that stamps
// every point with a 0.00000 placeholder rather than ever querying real
// terrain, most concretely (see gpx.NeedsElevation's own doc comment for
// the exact shape that looks like, and why 0.00000 everywhere is treated
// as absent rather than as "flat, verified").
//
// Off by default (config.ElevationConfig.Enabled) — this is the one
// feature in this codebase that sends a route's own coordinates to a
// service outside the deployment on its own initiative, not because a
// rider asked to connect an account. A regular running route can reveal
// where someone lives; that is an operator's call to make on purpose, not
// a default this package should assume.
package elevation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// DefaultURL is the public Open-Elevation instance — free, no API key, no
// signup, and open source, so an operator who would rather not depend on
// a public instance can self-host the same server instead and point
// elevation.url at that; this package only ever needs a URL that speaks
// its lookup API.
const DefaultURL = "https://api.open-elevation.com/api/v1/lookup"

// batchSize is how many locations one request asks for. The public
// instance has been observed to struggle well before any documented
// limit; this stays comfortably clear of that.
const batchSize = 100

const requestTimeout = 20 * time.Second

// Point is one location to look up.
type Point struct {
	Lat, Lon float64
}

// Client looks up elevation from the configured terrain API.
type Client struct {
	url        string
	httpClient *http.Client
}

// New builds a Client. An empty url falls back to DefaultURL — config.Load
// already fills this in at startup, but a caller constructing one
// directly (tests, the CLI) gets the same default rather than an empty,
// unusable one.
func New(url string) *Client {
	if url == "" {
		url = DefaultURL
	}
	return &Client{url: url, httpClient: &http.Client{
		Timeout:   requestTimeout,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}}
}

// Lookup returns the elevation, in metres, for each point, in the same
// order — batched transparently, so a caller never needs to think about
// the API's own per-request limit. Fails the whole call on the first
// error rather than returning a partial profile: a caller backfilling a
// route's elevation needs either a complete, trustworthy profile or none
// at all, never a silently-partial one that would read as real terrain
// for the points that happened to succeed.
func (c *Client) Lookup(ctx context.Context, points []Point) ([]float64, error) {
	out := make([]float64, 0, len(points))
	for start := 0; start < len(points); start += batchSize {
		end := start + batchSize
		if end > len(points) {
			end = len(points)
		}
		batch, err := c.lookupBatch(ctx, points[start:end])
		if err != nil {
			return nil, fmt.Errorf("elevation lookup for points %d-%d of %d: %w", start, end, len(points), err)
		}
		out = append(out, batch...)
	}
	return out, nil
}

type lookupRequest struct {
	Locations []location `json:"locations"`
}

type location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type lookupResponse struct {
	Results []struct {
		Elevation float64 `json:"elevation"`
	} `json:"results"`
}

func (c *Client) lookupBatch(ctx context.Context, points []Point) ([]float64, error) {
	locations := make([]location, len(points))
	for i, p := range points {
		locations[i] = location{Latitude: p.Lat, Longitude: p.Lon}
	}
	body, err := json.Marshal(lookupRequest{Locations: locations})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("elevation service returned %s", resp.Status)
	}

	// The url is operator config, not attacker input — but a self-hosted
	// instance an operator points this at is still a third party from this
	// process's own point of view, and a batch here is never more than
	// batchSize results, each a couple of small numbers. Capped well past
	// anything a real response could need, so a misbehaving or compromised
	// endpoint can't hand this an unbounded body to decode into memory.
	// io.LimitReader, not http.MaxBytesReader — that one is documented for
	// an incoming *request* body on the server side (it signals back
	// through an http.ResponseWriter on overflow); this is a client
	// reading a response, where a plain byte cap is all that applies.
	var out lookupResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode elevation response: %w", err)
	}
	if len(out.Results) != len(points) {
		return nil, fmt.Errorf("elevation service returned %d results for %d points", len(out.Results), len(points))
	}

	elevations := make([]float64, len(points))
	for i, r := range out.Results {
		elevations[i] = r.Elevation
	}
	return elevations, nil
}
