package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/weather"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const (
	weatherAPIKey = "k3y-secret-value"
	// The zone is explicit so nothing depends on the machine's TZ.
	weatherPlace = "Ghent"
)

// weatherNow is a Wednesday morning, 08:00 in Brussels (06:00 UTC).
var weatherNow = time.Date(2026, 3, 25, 8, 0, 0, 0, time.FixedZone("CET", 2*3600))

// weatherHarness runs the server against a fake Open-Meteo so a test can count
// and read every request that would have left the deployment.
type weatherHarness struct {
	t        *testing.T
	client   *http.Client
	base     string
	srv      *api.Server
	prefs    *weather.Store
	training *workout.DB
	db       *source.DB
	logs     *syncBuffer

	meteo    *httptest.Server
	requests atomic.Int32
	mu       sync.Mutex
	queries  []url.Values
	status   int
	body     string
}

// syncBuffer is a log sink safe for the server's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newWeatherHarness(t *testing.T, mutate ...func(*api.Server)) *weatherHarness {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	training, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := weather.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.New(auth.Config{
		Mode:  auth.ModeProxy,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}, Viewer: []string{"guests"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	h := &weatherHarness{t: t, prefs: prefs, training: training, db: db, logs: &syncBuffer{}, status: http.StatusOK, body: rainyForecast()}
	h.meteo = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.requests.Add(1)
		h.mu.Lock()
		h.queries = append(h.queries, r.URL.Query())
		status, body := h.status, h.body
		h.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(h.meteo.Close)

	srv := &api.Server{
		Source:       db,
		Auth:         authenticator,
		Links:        links,
		Training:     training,
		WeatherPrefs: prefs,
		Weather:      weather.New(h.meteo.URL, weatherAPIKey, func() time.Time { return weatherNow }),
		Config:       &config.Config{Weather: config.WeatherConfig{BaseURL: h.meteo.URL}},
		Clock:        func() time.Time { return weatherNow },
		Log:          slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, m := range mutate {
		m(srv)
	}
	h.srv = srv
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	h.client, h.base = server.Client(), server.URL
	return h
}

// rainyForecast is four flat days from 2026-03-25 (the harness's today): every
// hour wet, windy and cold, so any window is bad.
func rainyForecast() string { return forecastJSON(24*4, "2026-03-25", 2, 90, 2.0, 40, 70, 61) }

// forecastJSON builds an Open-Meteo body for n consecutive hours from date,
// every hour identical.
func forecastJSON(n int, date string, temp, prob, rain, wind, gust float64, code int) string {
	start, _ := time.Parse("2006-01-02", date)
	series := func(v float64) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
	times := make([]string, n)
	for i := range times {
		times[i] = start.Add(time.Duration(i) * time.Hour).Format("2006-01-02T15:04")
	}
	raw, _ := json.Marshal(map[string]any{
		"utc_offset_seconds": 7200,
		"hourly": map[string]any{
			"time":                      times,
			"temperature_2m":            series(temp),
			"precipitation_probability": series(prob),
			"precipitation":             series(rain),
			"wind_speed_10m":            series(wind),
			"wind_gusts_10m":            series(gust),
			"weather_code":              series(float64(code)),
		},
	})
	return string(raw)
}

func (h *weatherHarness) setForecast(status int, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status, h.body = status, body
}

func (h *weatherHarness) as(user, groups, method, path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Remote-User", user)
	req.Header.Set("Remote-Groups", groups)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

type weatherPrefsOut struct {
	Configured  bool   `json:"configured"`
	Place       string `json:"place"`
	Attribution string `json:"attribution"`
	Window      struct {
		Start int `json:"start"`
		End   int `json:"end"`
	} `json:"window"`
}

func (h *weatherHarness) decodePrefs(resp *http.Response) (weatherPrefsOut, string) {
	h.t.Helper()
	raw := readAll(h.t, resp)
	var out weatherPrefsOut
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatalf("decode %s: %v", raw, err)
	}
	return out, string(raw)
}

func (h *weatherHarness) getPrefs(user string) (weatherPrefsOut, string) {
	h.t.Helper()
	resp := h.as(user, "cyclists", http.MethodGet, "/api/training/weather", "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("GET weather: %d %s", resp.StatusCode, readAll(h.t, resp))
	}
	return h.decodePrefs(resp)
}

func (h *weatherHarness) setLocation(user, body string) *http.Response {
	h.t.Helper()
	return h.as(user, "cyclists", http.MethodPut, "/api/training/weather/location", body)
}

const ghentBody = `{"place":"Ghent","lat":51.054321,"lon":3.719876}`

func TestWeatherLocationIsStoredRoundedAndNeverReturned(t *testing.T) {
	h := newWeatherHarness(t)

	resp := h.setLocation("wilant", ghentBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}
	out, raw := h.decodePrefs(resp)
	if !out.Configured || out.Place != weatherPlace {
		t.Errorf("PUT body = %s", raw)
	}

	stored, ok, err := h.prefs.Get(context.Background(), "wilant")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if stored.Lat != 51.05 || stored.Lon != 3.72 {
		t.Errorf("stored %v, %v; want 2 decimals", stored.Lat, stored.Lon)
	}

	got, rawGet := h.getPrefs("wilant")
	if !got.Configured || got.Place != weatherPlace || got.Window.Start != 9 || got.Window.End != 12 {
		t.Errorf("GET = %s", rawGet)
	}
	if !strings.Contains(got.Attribution, "Open-Meteo") {
		t.Errorf("attribution = %q", got.Attribution)
	}
	// Neither the PUT nor the GET may carry a coordinate in any spelling.
	for _, body := range []string{raw, rawGet} {
		for _, leak := range []string{"lat", "lon", "51.0", "3.7"} {
			if strings.Contains(strings.ToLower(body), leak) {
				t.Errorf("response leaks %q: %s", leak, body)
			}
		}
	}
}

func TestWeatherLocationValidation(t *testing.T) {
	h := newWeatherHarness(t)
	for name, body := range map[string]string{
		"not json":       `{`,
		"no place":       `{"lat":51,"lon":3}`,
		"blank place":    `{"place":"   ","lat":51,"lon":3}`,
		"latitude high":  `{"place":"x","lat":91,"lon":3}`,
		"latitude low":   `{"place":"x","lat":-91,"lon":3}`,
		"longitude high": `{"place":"x","lat":51,"lon":181}`,
		"longitude low":  `{"place":"x","lat":51,"lon":-181}`,
	} {
		if got := h.setLocation("wilant", body).StatusCode; got != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, got)
		}
	}
	if _, ok, _ := h.prefs.Get(context.Background(), "wilant"); ok {
		t.Fatal("a rejected request stored a location")
	}
}

func TestWeatherPlaceIsTruncatedToEightyCharacters(t *testing.T) {
	h := newWeatherHarness(t)
	body := `{"place":"` + strings.Repeat("a", 200) + `","lat":51,"lon":3}`
	if got := h.setLocation("wilant", body).StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	out, _ := h.getPrefs("wilant")
	if len(out.Place) != 80 {
		t.Errorf("place length = %d, want 80", len(out.Place))
	}
}

func TestWeatherWindow(t *testing.T) {
	h := newWeatherHarness(t)

	// No town yet: nothing to attach the hours to.
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/weather/window", `{"start":7,"end":9}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("window without a location: status = %d, want 409", resp.StatusCode)
	}

	h.setLocation("wilant", ghentBody)
	resp = h.as("wilant", "cyclists", http.MethodPut, "/api/training/weather/window", `{"start":7,"end":9}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if out, _ := h.decodePrefs(resp); out.Window.Start != 7 || out.Window.End != 9 {
		t.Errorf("window = %+v", out.Window)
	}
	if out, _ := h.getPrefs("wilant"); out.Window.Start != 7 || out.Window.End != 9 {
		t.Errorf("GET window = %+v", out.Window)
	}

	for name, body := range map[string]string{
		"not json":     `{`,
		"start = end":  `{"start":9,"end":9}`,
		"start > end":  `{"start":12,"end":9}`,
		"negative":     `{"start":-1,"end":9}`,
		"end past 23":  `{"start":9,"end":24}`,
		"start past":   `{"start":24,"end":25}`,
		"missing both": `{}`,
	} {
		resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/weather/window", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
	// None of the rejected ones changed it.
	if out, _ := h.getPrefs("wilant"); out.Window.Start != 7 || out.Window.End != 9 {
		t.Errorf("window changed by a rejected request: %+v", out.Window)
	}
}

func TestWeatherDeleteIsTheOptOut(t *testing.T) {
	h := newWeatherHarness(t)
	h.setLocation("wilant", ghentBody)

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/weather/location", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if out, _ := h.getPrefs("wilant"); out.Configured || out.Place != "" {
		t.Errorf("still configured after delete: %+v", out)
	}
	if _, ok, _ := h.prefs.Get(context.Background(), "wilant"); ok {
		t.Error("row still there")
	}
	// Again, and for someone who never opted in: idempotent.
	if got := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/weather/location", "").StatusCode; got != http.StatusOK {
		t.Errorf("second delete status = %d", got)
	}
}

func TestWeatherIsOwnerOnlyAndTheRiderComesFromTheSession(t *testing.T) {
	h := newWeatherHarness(t)
	h.setLocation("wilant", ghentBody)

	// A body that names another rider is not obeyed.
	h.setLocation("sam", `{"rider":"wilant","place":"Antwerp","lat":51.22,"lon":4.4}`)
	if p, _, _ := h.prefs.Get(context.Background(), "wilant"); p.Place != weatherPlace {
		t.Errorf("sam's request rewrote wilant: %+v", p)
	}
	if p, ok, _ := h.prefs.Get(context.Background(), "sam"); !ok || p.Place != "Antwerp" {
		t.Errorf("sam's own row = %+v ok=%v", p, ok)
	}

	// Each sees only their own; deleting one leaves the other.
	if out, _ := h.getPrefs("wilant"); out.Place != weatherPlace {
		t.Errorf("wilant sees %q", out.Place)
	}
	h.as("sam", "cyclists", http.MethodDelete, "/api/training/weather/location", "")
	if out, _ := h.getPrefs("wilant"); !out.Configured {
		t.Error("sam's opt-out removed wilant's location")
	}
	if out, _ := h.getPrefs("sam"); out.Configured {
		t.Error("sam still configured")
	}
}

func TestWeatherNeedsARiderRole(t *testing.T) {
	h := newWeatherHarness(t)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/training/weather", ""},
		{http.MethodPut, "/api/training/weather/location", ghentBody},
		{http.MethodPut, "/api/training/weather/window", `{"start":7,"end":9}`},
		{http.MethodDelete, "/api/training/weather/location", ""},
	} {
		resp := h.as("guest", "guests", c.method, c.path, c.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a viewer: status = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
	if _, ok, _ := h.prefs.Get(context.Background(), "guest"); ok {
		t.Error("a viewer stored a location")
	}
}

func TestWeatherMakesNoRequestWithoutALocation(t *testing.T) {
	h := newWeatherHarness(t)

	h.getPrefs("wilant")
	h.as("wilant", "cyclists", http.MethodPut, "/api/training/weather/window", `{"start":7,"end":9}`)
	h.as("wilant", "cyclists", http.MethodDelete, "/api/training/weather/location", "")
	h.srv.SyncTrainingMetrics(context.Background())

	if n := h.requests.Load(); n != 0 {
		t.Fatalf("%d request(s) reached Open-Meteo for a rider with no location", n)
	}
}

func TestWeatherDisabledAnswers412AndSaysWhy(t *testing.T) {
	off := false
	h := newWeatherHarness(t, func(s *api.Server) {
		s.Config.Weather.Enabled = &off
	})
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/training/weather", ""},
		{http.MethodPut, "/api/training/weather/location", ghentBody},
		{http.MethodPut, "/api/training/weather/window", `{"start":7,"end":9}`},
		{http.MethodDelete, "/api/training/weather/location", ""},
	} {
		resp := h.as("wilant", "cyclists", c.method, c.path, c.body)
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Errorf("%s %s: status = %d, want 412", c.method, c.path, resp.StatusCode)
		}
	}
	if _, ok, _ := h.prefs.Get(context.Background(), "wilant"); ok {
		t.Error("a location was stored while weather is disabled")
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "weather is disabled") {
		t.Errorf("a guard that fires must log a Warn with the reason; logs:\n%s", logs)
	}
	if n := h.requests.Load(); n != 0 {
		t.Errorf("%d request(s) while disabled", n)
	}
}

// seedRiders opts riders in directly through the store.
func (h *weatherHarness) seed(rider, place string, lat, lon float64) {
	h.t.Helper()
	if err := h.prefs.SetLocation(context.Background(), rider, place, lat, lon); err != nil {
		h.t.Fatal(err)
	}
}

func TestSyncWarmsTheCacheOncePerDistinctLocation(t *testing.T) {
	h := newWeatherHarness(t)
	// Two riders in one town, a third elsewhere, a fourth who never opted in.
	h.seed("wilant", "Ghent", 51.0543, 3.7199)
	h.seed("sam", "Ghent", 51.0501, 3.7201) // rounds to the same 51.05, 3.72
	h.seed("pat", "Antwerp", 51.2194, 4.4025)

	h.srv.SyncTrainingMetrics(context.Background())

	if n := h.requests.Load(); n != 2 {
		t.Fatalf("requests = %d, want 2 (one per distinct rounded location)", n)
	}
	seen := map[string]bool{}
	for _, q := range h.queries {
		seen[q.Get("latitude")+","+q.Get("longitude")] = true
		if q.Get("apikey") != weatherAPIKey {
			t.Errorf("apikey missing from a warm-up request: %v", q)
		}
	}
	if !seen["51.05,3.72"] || !seen["51.22,4.4"] {
		t.Errorf("locations fetched = %v", seen)
	}

	// The cache is now warm: a second pass within the hour costs nothing.
	h.srv.SyncTrainingMetrics(context.Background())
	if n := h.requests.Load(); n != 2 {
		t.Errorf("second sync made %d more request(s)", n-2)
	}
}

func TestSyncWarmUpFailureLogsWarnAndTheSyncStillSucceeds(t *testing.T) {
	h := newWeatherHarness(t)
	h.seed("wilant", weatherPlace, 51.0543, 3.7199)
	h.setForecast(http.StatusInternalServerError, `{}`)

	done := make(chan struct{})
	go func() {
		h.srv.SyncTrainingMetrics(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the sync did not finish after a weather failure")
	}

	logs := h.logs.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "weather warm-up") {
		t.Errorf("want a Warn about the failed warm-up; logs:\n%s", logs)
	}
	// The sync itself was recorded as done (a wellness pass that ran fine must
	// not be re-run because the forecast was down).
	if n := h.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (a failure is not retried in the same pass)", n)
	}
}

func TestWeatherLogsCarryNoCoordinatesPlaceNamesKeysOrRiderNextToAValue(t *testing.T) {
	h := newWeatherHarness(t)
	const rider = "wilant"
	h.setLocation(rider, `{"place":"Ghent","lat":51.054321,"lon":3.719876}`)
	h.as(rider, "cyclists", http.MethodPut, "/api/training/weather/window", `{"start":7,"end":9}`)
	h.srv.SyncTrainingMetrics(context.Background())
	h.setForecast(http.StatusInternalServerError, `{}`)
	h.as(rider, "cyclists", http.MethodDelete, "/api/training/weather/location", "")
	h.setLocation(rider, `{"place":"Ghent","lat":51.054321,"lon":3.719876}`)
	// A failing warm-up, too, since that is where an error string could leak.
	h.srv.Weather = weather.New(h.meteo.URL, weatherAPIKey, func() time.Time { return weatherNow.Add(3 * time.Hour) })
	h.srv.SyncTrainingMetrics(context.Background())

	logs := h.logs.String()
	for _, leak := range []string{"Ghent", "51.05", "3.72", "51.054", "3.719", "latitude", "longitude", weatherAPIKey, "apikey"} {
		if strings.Contains(logs, leak) {
			t.Errorf("logs leak %q:\n%s", leak, logs)
		}
	}
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(strings.ToLower(line), "weather") && strings.Contains(line, rider) {
			t.Errorf("a weather log line names the rider: %s", line)
		}
	}
}
