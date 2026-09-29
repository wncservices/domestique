package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixture is a trimmed Open-Meteo hourly response: two local days, times as
// the API sends them (local ISO strings without an offset).
const fixture = `{
  "utc_offset_seconds": 7200,
  "hourly": {
    "time": ["2026-10-01T08:00","2026-10-01T09:00","2026-10-02T00:00"],
    "temperature_2m": [11.5, 12.0, 9.0],
    "precipitation_probability": [10, null, 80],
    "precipitation": [0, 0.4, 2.5],
    "wind_speed_10m": [14.1, 20, 31],
    "wind_gusts_10m": [25, 33.5, 52],
    "weather_code": [1, 61, 95]
  }
}`

type fake struct {
	srv      *httptest.Server
	requests atomic.Int32
	mu       sync.Mutex
	queries  []url.Values
	paths    []string
	status   int
	body     string
	delay    time.Duration
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{status: http.StatusOK, body: fixture}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.Query())
		f.paths = append(f.paths, r.URL.Path)
		status, body, delay := f.status, f.body, f.delay
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) set(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func fixedClock(t time.Time) (func() time.Time, func(time.Duration)) {
	var mu sync.Mutex
	cur := t
	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return cur
		}, func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			cur = cur.Add(d)
		}
}

var t0 = time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)

func TestForecastSendsExactlyTheDocumentedQuery(t *testing.T) {
	f := newFake(t)
	clock, _ := fixedClock(t0)
	c := New(f.srv.URL, "", clock)

	// Six decimals in, two out: a rider's rounded town must be all that
	// ever leaves, even if a caller forgets to round.
	if _, err := c.Forecast(t.Context(), Location{Lat: 51.054321, Lon: 3.719876}); err != nil {
		t.Fatal(err)
	}
	if got := f.paths[0]; got != "/v1/forecast" {
		t.Fatalf("path = %q", got)
	}
	q := f.queries[0]
	want := map[string]string{
		"latitude":        "51.05",
		"longitude":       "3.72",
		"hourly":          "precipitation_probability,precipitation,temperature_2m,wind_speed_10m,wind_gusts_10m,weather_code",
		"forecast_days":   "4",
		"timezone":        "auto",
		"wind_speed_unit": "kmh",
	}
	var keys []string
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != len(want) {
		t.Fatalf("query keys = %v, want exactly %d (%v)", keys, len(want), want)
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if q.Has("apikey") {
		t.Error("apikey sent although none is configured")
	}
}

func TestForecastNegativeAndZeroCoordinatesAreWellFormed(t *testing.T) {
	f := newFake(t)
	clock, _ := fixedClock(t0)
	c := New(f.srv.URL, "", clock)
	if _, err := c.Forecast(t.Context(), Location{Lat: -0.001, Lon: -33.456}); err != nil {
		t.Fatal(err)
	}
	q := f.queries[0]
	if q.Get("latitude") != "0" || q.Get("longitude") != "-33.46" {
		t.Fatalf("lat=%q lon=%q", q.Get("latitude"), q.Get("longitude"))
	}
}

func TestForecastSendsAPIKeyOnlyWhenConfigured(t *testing.T) {
	f := newFake(t)
	clock, _ := fixedClock(t0)
	c := New(f.srv.URL, "s3cret-key", clock)
	if _, err := c.Forecast(t.Context(), Location{Lat: 51, Lon: 3}); err != nil {
		t.Fatal(err)
	}
	if got := f.queries[0].Get("apikey"); got != "s3cret-key" {
		t.Fatalf("apikey = %q", got)
	}
}

func TestForecastDecodesHoursKeepingLocalTimeStrings(t *testing.T) {
	f := newFake(t)
	clock, _ := fixedClock(t0)
	c := New(f.srv.URL, "", clock)
	got, err := c.Forecast(t.Context(), Location{Lat: 51, Lon: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.UTCOffsetSeconds != 7200 {
		t.Errorf("offset = %d", got.UTCOffsetSeconds)
	}
	if !got.FetchedAt.Equal(t0) {
		t.Errorf("FetchedAt = %v", got.FetchedAt)
	}
	want := []Hour{
		{Date: "2026-10-01", Hour: 8, Temp: 11.5, Rain: 0, RainProb: 10, Wind: 14.1, Gust: 25, Code: 1},
		// A null probability (Open-Meteo sends them for some models) reads as 0.
		{Date: "2026-10-01", Hour: 9, Temp: 12, Rain: 0.4, RainProb: 0, Wind: 20, Gust: 33.5, Code: 61},
		{Date: "2026-10-02", Hour: 0, Temp: 9, Rain: 2.5, RainProb: 80, Wind: 31, Gust: 52, Code: 95},
	}
	if len(got.Hours) != len(want) {
		t.Fatalf("hours = %d", len(got.Hours))
	}
	for i := range want {
		if got.Hours[i] != want[i] {
			t.Errorf("hour %d = %+v, want %+v", i, got.Hours[i], want[i])
		}
	}
}

func TestForecastErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", 500, `{"error":true}`},
		{"rate limited", 429, `{}`},
		{"malformed JSON", 200, `{not json`},
		{"ragged arrays", 200, `{"hourly":{"time":["2026-10-01T08:00"],"temperature_2m":[]}}`},
		{"bad time", 200, `{"hourly":{"time":["yesterday"],"temperature_2m":[1],"precipitation_probability":[1],"precipitation":[1],"wind_speed_10m":[1],"wind_gusts_10m":[1],"weather_code":[1]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.set(tc.status, tc.body)
			clock, _ := fixedClock(t0)
			c := New(f.srv.URL, "s3cret-key", clock)
			_, err := c.Forecast(t.Context(), Location{Lat: 51.05, Lon: 3.72})
			if err == nil {
				t.Fatal("want error")
			}
			// The error reaches logs: it must carry neither the key nor the
			// coordinates (net/http's own errors embed the whole URL).
			for _, secret := range []string{"s3cret-key", "51.05", "3.72", "latitude"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks %q: %v", secret, err)
				}
			}
		})
	}
}

func TestForecastTransportErrorDoesNotLeakURL(t *testing.T) {
	f := newFake(t)
	base := f.srv.URL
	f.srv.Close() // connection refused
	clock, _ := fixedClock(t0)
	c := New(base, "s3cret-key", clock)
	_, err := c.Forecast(t.Context(), Location{Lat: 51.05, Lon: 3.72})
	if err == nil {
		t.Fatal("want error")
	}
	for _, secret := range []string{"s3cret-key", "51.05", "3.72", "latitude"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

func TestForecastTimeoutAndCancellation(t *testing.T) {
	f := newFake(t)
	f.delay = 2 * time.Second
	clock, _ := fixedClock(t0)
	c := New(f.srv.URL, "", clock)
	c.http.Timeout = 50 * time.Millisecond
	start := time.Now()
	if _, err := c.Forecast(t.Context(), Location{Lat: 51, Lon: 3}); err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not honoured")
	}

	c2 := New(f.srv.URL, "", clock)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c2.Forecast(ctx, Location{Lat: 51, Lon: 3}); err == nil {
		t.Fatal("want cancellation error")
	}
}

func TestDefaultTimeoutIsTenSeconds(t *testing.T) {
	clock, _ := fixedClock(t0)
	if got := New("http://x", "", clock).http.Timeout; got != 10*time.Second {
		t.Fatalf("timeout = %v", got)
	}
}

func TestCacheServesWithinAnHourAndRefetchesAfter(t *testing.T) {
	f := newFake(t)
	clock, advance := fixedClock(t0)
	c := New(f.srv.URL, "", clock)
	loc := Location{Lat: 51.05, Lon: 3.72}

	if _, err := c.Forecast(t.Context(), loc); err != nil {
		t.Fatal(err)
	}
	advance(59 * time.Minute)
	// The same rounded town under different raw coordinates shares the entry.
	if _, err := c.Forecast(t.Context(), Location{Lat: 51.0499, Lon: 3.7201}); err != nil {
		t.Fatal(err)
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("requests within the hour = %d, want 1", n)
	}
	advance(2 * time.Minute)
	if _, err := c.Forecast(t.Context(), loc); err != nil {
		t.Fatal(err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Fatalf("requests after the hour = %d, want 2", n)
	}
	// A different town is its own entry.
	if _, err := c.Forecast(t.Context(), Location{Lat: 50.85, Lon: 4.35}); err != nil {
		t.Fatal(err)
	}
	if n := f.requests.Load(); n != 3 {
		t.Fatalf("requests for a new town = %d, want 3", n)
	}
}

func TestErrorIsNotCachedAndNoStaleDataAfterOne(t *testing.T) {
	f := newFake(t)
	clock, advance := fixedClock(t0)
	c := New(f.srv.URL, "", clock)
	loc := Location{Lat: 51.05, Lon: 3.72}

	if _, err := c.Forecast(t.Context(), loc); err != nil {
		t.Fatal(err)
	}
	advance(61 * time.Minute)
	f.set(500, `{}`)
	// The entry has expired and the refresh fails: an error, never the old
	// forecast served as if it were current.
	if got, err := c.Forecast(t.Context(), loc); err == nil {
		t.Fatalf("want error, got %d stale hours", len(got.Hours))
	}
	// And the failure was not remembered.
	f.set(200, fixture)
	if _, err := c.Forecast(t.Context(), loc); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if n := f.requests.Load(); n != 3 {
		t.Fatalf("requests = %d, want 3", n)
	}
}

func TestCacheIsBounded(t *testing.T) {
	f := newFake(t)
	clock, _ := fixedClock(t0)
	c := New(f.srv.URL, "", clock)
	for i := 0; i < maxCacheEntries+50; i++ {
		loc := Location{Lat: float64(i%80) + 0.5, Lon: float64(i/80) * 0.5}
		if _, err := c.Forecast(t.Context(), loc); err != nil {
			t.Fatal(err)
		}
	}
	if n := c.cacheLen(); n > maxCacheEntries {
		t.Fatalf("cache holds %d entries, cap is %d", n, maxCacheEntries)
	}
}

func TestResponseBodyIsCapped(t *testing.T) {
	f := newFake(t)
	f.set(200, `{"hourly":{"time":[`+strings.Repeat(`"2026-10-01T08:00",`, 200000)+`"2026-10-01T08:00"]}}`)
	clock, _ := fixedClock(t0)
	c := New(f.srv.URL, "", clock)
	if _, err := c.Forecast(t.Context(), Location{Lat: 51, Lon: 3}); err == nil {
		t.Fatal("want error for an oversized body")
	}
}

func TestRoundLocation(t *testing.T) {
	got := RoundLocation(Location{Lat: 51.054999, Lon: -3.7149})
	if got.Lat != 51.05 || got.Lon != -3.71 {
		t.Fatalf("got %+v", got)
	}
	if got := RoundLocation(Location{Lat: -0.001, Lon: 0}); got.Lat != 0 || 1/got.Lat < 0 {
		t.Fatalf("negative zero survived: %+v", got)
	}
}
