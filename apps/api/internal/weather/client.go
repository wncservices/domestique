// Package weather fetches an hourly forecast from Open-Meteo and decides,
// purely, whether a ride window looks bad.
//
// Privacy is the reason this package is small and takes so little. A
// coordinate is personal data (a route starts at somebody's front door), so
// the only thing that ever reaches Open-Meteo is a rider's own chosen town,
// rounded to two decimals (about 1.1 km). This package imports neither gpx,
// source nor the route model: a Location comes from the rider's weather
// preferences and from nowhere else. Errors are rebuilt without the request
// URL, because net/http embeds the full URL (coordinates, API key) in its own.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// DefaultBaseURL is the free public Open-Meteo API: non-commercial use only,
// CC-BY attribution required (see docs/weather.md). An operator can point
// weather.base_url at a self-hosted instance or the commercial endpoint.
const DefaultBaseURL = "https://api.open-meteo.com"

// EnvAPIKey is where the optional Open-Meteo API key comes from: never the
// config file, never a log line.
// #nosec G101 -- this is the *name* of an environment variable, not a
// credential.
const EnvAPIKey = "OPEN_METEO_API_KEY"

const (
	requestTimeout = 10 * time.Second

	// cacheTTL matches how often Open-Meteo refreshes its models (roughly
	// hourly): a shorter TTL would only re-download the same numbers, and N
	// riders in one town cost one call an hour.
	cacheTTL = 60 * time.Minute

	// maxCacheEntries bounds memory. Entries are per rounded town, so this is
	// far above any real deployment; it only stops a runaway caller.
	maxCacheEntries = 256

	// maxBodyBytes: four days of six hourly series is a few tens of KB. A
	// self-hosted or compromised endpoint cannot hand this an unbounded body.
	maxBodyBytes = 1 << 20

	hourlyVars = "precipitation_probability,precipitation,temperature_2m,wind_speed_10m,wind_gusts_10m,weather_code"
)

// Location is a rounded town position. Callers pass already-rounded values
// (the preferences store rounds on write); the Client rounds again defensively.
type Location struct{ Lat, Lon float64 }

// Hour is one forecast hour. Date and Hour are the forecast's own local time,
// kept as parsed strings/ints: the server's zone is never involved.
type Hour struct {
	Date                             string // 2006-01-02, local to the location
	Hour                             int    // 0-23, local to the location
	Temp, Rain, RainProb, Wind, Gust float64
	Code                             int // WMO weather code
}

// Forecast is four local days of hours. The slice is shared with the cache and
// must be treated as read-only.
type Forecast struct {
	Hours            []Hour
	UTCOffsetSeconds int
	FetchedAt        time.Time
}

// RoundLocation rounds to two decimals, and normalises -0 so a query never
// reads "-0".
func RoundLocation(l Location) Location {
	return Location{Lat: round2(l.Lat), Lon: round2(l.Lon)}
}

func round2(v float64) float64 {
	r := math.Round(v*100) / 100
	if r == 0 {
		return 0
	}
	return r
}

type cacheEntry struct {
	forecast Forecast
	fetched  time.Time
}

// Client talks to Open-Meteo.
type Client struct {
	baseURL string
	apiKey  string // never logged, never in an error
	http    *http.Client
	clock   func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// New builds a Client. An empty baseURL falls back to DefaultBaseURL, and a nil
// clock to time.Now. apiKey may be empty (the free tier needs none).
func New(baseURL, apiKey string, clock func() time.Time) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if clock == nil {
		clock = time.Now
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		clock:   clock,
		cache:   map[string]cacheEntry{},
		http: &http.Client{
			Timeout:   requestTimeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

func cacheKey(l Location) string {
	return strconv.FormatFloat(l.Lat, 'f', 2, 64) + "," + strconv.FormatFloat(l.Lon, 'f', 2, 64)
}

func (c *Client) cacheLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cache)
}

// Forecast returns four days of hourly weather for the rounded location, from
// the cache when it is under an hour old. A failed fetch is an error and is not
// cached; an expired entry is never served after a failed refresh.
func (c *Client) Forecast(ctx context.Context, loc Location) (Forecast, error) {
	loc = RoundLocation(loc)
	key := cacheKey(loc)
	now := c.clock()

	c.mu.Lock()
	if e, ok := c.cache[key]; ok {
		if now.Sub(e.fetched) < cacheTTL {
			c.mu.Unlock()
			return e.forecast, nil
		}
		delete(c.cache, key) // expired: gone even if the refresh below fails
	}
	c.mu.Unlock()

	f, err := c.fetch(ctx, loc)
	if err != nil {
		return Forecast{}, err
	}
	f.FetchedAt = now

	c.mu.Lock()
	c.evictLocked(now)
	c.cache[key] = cacheEntry{forecast: f, fetched: now}
	c.mu.Unlock()
	return f, nil
}

// evictLocked makes room for one more entry: expired ones first, then the
// oldest.
func (c *Client) evictLocked(now time.Time) {
	if len(c.cache) < maxCacheEntries {
		return
	}
	for k, e := range c.cache {
		if now.Sub(e.fetched) >= cacheTTL {
			delete(c.cache, k)
		}
	}
	for len(c.cache) >= maxCacheEntries {
		var oldestKey string
		var oldest time.Time
		for k, e := range c.cache {
			if oldestKey == "" || e.fetched.Before(oldest) {
				oldestKey, oldest = k, e.fetched
			}
		}
		delete(c.cache, oldestKey)
	}
}

type apiResponse struct {
	UTCOffsetSeconds int `json:"utc_offset_seconds"`
	Hourly           struct {
		Time     []string   `json:"time"`
		Temp     []*float64 `json:"temperature_2m"`
		RainProb []*float64 `json:"precipitation_probability"`
		Rain     []*float64 `json:"precipitation"`
		Wind     []*float64 `json:"wind_speed_10m"`
		Gust     []*float64 `json:"wind_gusts_10m"`
		Code     []*float64 `json:"weather_code"`
	} `json:"hourly"`
}

func (c *Client) fetch(ctx context.Context, loc Location) (Forecast, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(loc.Lat, 'f', -1, 64))
	q.Set("longitude", strconv.FormatFloat(loc.Lon, 'f', -1, 64))
	q.Set("hourly", hourlyVars)
	q.Set("forecast_days", "4")
	q.Set("timezone", "auto")
	q.Set("wind_speed_unit", "kmh")
	if c.apiKey != "" {
		q.Set("apikey", c.apiKey)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/forecast?"+q.Encode(), nil)
	if err != nil {
		return Forecast{}, errors.New("weather: build request failed")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error's text embeds the URL: coordinates and API key. Keep only
		// the underlying cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Forecast{}, fmt.Errorf("weather: forecast request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Forecast{}, fmt.Errorf("weather: forecast service returned %d", resp.StatusCode)
	}

	var out apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&out); err != nil {
		return Forecast{}, errors.New("weather: decode forecast response failed")
	}
	return out.forecast()
}

func (r apiResponse) forecast() (Forecast, error) {
	h := r.Hourly
	n := len(h.Time)
	for _, s := range [][]*float64{h.Temp, h.RainProb, h.Rain, h.Wind, h.Gust, h.Code} {
		if len(s) != n {
			return Forecast{}, errors.New("weather: forecast series have different lengths")
		}
	}
	val := func(s []*float64, i int) float64 {
		if s[i] == nil {
			return 0
		}
		return *s[i]
	}
	hours := make([]Hour, 0, n)
	for i, ts := range h.Time {
		// "2026-10-01T09:00": the location's local time, no offset.
		t, err := time.Parse("2006-01-02T15:04", ts)
		if err != nil {
			return Forecast{}, errors.New("weather: unreadable forecast time")
		}
		hours = append(hours, Hour{
			Date:     t.Format("2006-01-02"),
			Hour:     t.Hour(),
			Temp:     val(h.Temp, i),
			Rain:     val(h.Rain, i),
			RainProb: val(h.RainProb, i),
			Wind:     val(h.Wind, i),
			Gust:     val(h.Gust, i),
			Code:     int(val(h.Code, i)),
		})
	}
	return Forecast{Hours: hours, UTCOffsetSeconds: r.UTCOffsetSeconds}, nil
}
