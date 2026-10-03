// Acceptance tests for the private calendar feed: the one endpoint that
// bypasses authentication, so every property that keeps that safe is checked
// over real HTTP in every auth mode.
package api_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/calendarfeed"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const calendarPublicURL = "https://domestique.example.com"

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type calHarness struct {
	t        *testing.T
	mode     auth.Mode
	base     string
	client   *http.Client
	srv      *api.Server
	feeds    *calendarfeed.Store
	training *workout.DB
	sessions *sessions.Store
	logs     *lockedBuffer
	spans    *tracetest.SpanRecorder
}

var calendarNow = time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)

// calendarEngines yields the engines to run against: a marker for SQLite and
// the base DSN for PostgreSQL. Each harness gets its own database from
// freshDSN, so one test's rows never leak into the next.
func calendarEngines(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{"sqlite": ""}
	if dsn := os.Getenv("DOMESTIQUE_TEST_POSTGRES"); dsn != "" {
		out["postgres"] = dsn
	}
	return out
}

// freshDSN is a database nobody else has used: a new file for SQLite, a new
// schema (dropped afterwards) for PostgreSQL, so a parallel package's tables
// never see these rows either.
func freshDSN(t *testing.T, base string) string {
	t.Helper()
	if base == "" {
		return filepath.Join(t.TempDir(), "cal.db")
	}
	name := fmt.Sprintf("cal_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + name + ` CASCADE`)
		_ = admin.Close()
	})
	return base + "&search_path=" + name
}

func newCalHarness(t *testing.T, mode auth.Mode, dsn string, publicURL string) *calHarness {
	t.Helper()

	db, err := source.OpenDB(freshDSN(t, dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	feeds, err := calendarfeed.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	training, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	var a *auth.Authenticator
	var sess *sessions.Store
	switch mode {
	case auth.ModeNone:
		a, err = auth.New(auth.Config{Mode: auth.ModeNone})
	case auth.ModeProxy:
		a, err = auth.New(auth.Config{
			Mode: auth.ModeProxy, RequiredGroup: "gate",
			Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}, Viewer: []string{"guests"}},
		})
	case auth.ModeOIDC:
		a, err = auth.New(auth.Config{
			Mode: auth.ModeOIDC, RequiredGroup: "gate",
			Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}, Viewer: []string{"guests"}},
			OIDC:  auth.OIDCConfig{Issuer: "https://issuer.example.com", ClientID: "c", RedirectURL: "https://domestique.example.com/sso/callback"},
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == auth.ModeOIDC {
		key, _ := secrets.GenerateKey()
		box, err := secrets.New(key)
		if err != nil {
			t.Fatal(err)
		}
		if sess, err = sessions.UseDB(db.Conn(), db.DSN(), box); err != nil {
			t.Fatal(err)
		}
		a.UseSessions(sess)
	}

	logs := &lockedBuffer{}
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	srv := &api.Server{
		Auth: a, Training: training, CalendarFeeds: feeds,
		Config:              &config.Config{PublicURL: publicURL},
		Log:                 slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:               func() time.Time { return calendarNow },
		CalendarLimiter:     api.NewCalendarLimiter(),
		CalendarMissLimiter: api.NewCalendarMissLimiter(),
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	return &calHarness{t: t, mode: mode, base: server.URL, client: server.Client(), srv: srv,
		feeds: feeds, training: training, sessions: sess, logs: logs, spans: rec}
}

// rider is the name a signed-in user ends up with: mode none has only "local".
func (h *calHarness) rider(user string) string {
	if h.mode == auth.ModeNone {
		return "local"
	}
	return user
}

// identify makes req look like it came from user in that group, however this
// mode recognises someone.
func (h *calHarness) identify(req *http.Request, user string, groups ...string) {
	switch h.mode {
	case auth.ModeProxy:
		req.Header.Set(auth.HeaderUser, user)
		req.Header.Set(auth.HeaderGroups, strings.Join(append([]string{"gate"}, groups...), ","))
	case auth.ModeOIDC:
		tok, _, err := h.sessions.Create(auth.Identity{User: user, Sub: "auth0|" + user, Groups: append([]string{"gate"}, groups...)}, time.Hour)
		if err != nil {
			h.t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	}
}

type calResp struct {
	code   int
	header http.Header
	body   string
}

func (h *calHarness) do(method, path string, mutate func(*http.Request)) calResp {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return calResp{resp.StatusCode, resp.Header, string(b)}
}

func (h *calHarness) as(user, method, path string) calResp {
	return h.do(method, path, func(r *http.Request) { h.identify(r, user, "cyclists") })
}

// generate creates user's feed through the API and returns its path.
func (h *calHarness) generate(user string) string {
	h.t.Helper()
	resp := h.as(user, http.MethodPost, "/api/training/calendar")
	if resp.code != http.StatusOK {
		h.t.Fatalf("generating a feed = %d %s", resp.code, resp.body)
	}
	var out struct{ URL, WebcalURL string }
	if err := json.Unmarshal([]byte(resp.body), &out); err != nil {
		h.t.Fatal(err)
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		h.t.Fatal(err)
	}
	return u.Path
}

func (h *calHarness) seedWorkout(rider, name, date string) {
	h.t.Helper()
	_, err := h.training.CreateWorkout(h.t.Context(), workout.CreateWorkoutRequest{
		Rider: rider, Name: name, Date: date, Zone: workout.ZoneThreshold,
		Steps: []workout.WorkoutStep{{
			Repeat: 3, Steps: []workout.WorkoutStep{
				{Intensity: workout.IntensityInterval, Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetPower, TargetLow: 250, TargetHigh: 260},
				{Intensity: workout.IntensityRecovery, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
			},
		}},
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func eachCalendarSetup(t *testing.T, run func(t *testing.T, h *calHarness)) {
	t.Helper()
	for engine, dsn := range calendarEngines(t) {
		for _, mode := range []auth.Mode{auth.ModeNone, auth.ModeProxy, auth.ModeOIDC} {
			t.Run(engine+"/"+string(mode), func(t *testing.T) {
				run(t, newCalHarness(t, mode, dsn, calendarPublicURL))
			})
		}
	}
}

func TestCalendarFeedServesThePlanWithoutASession(t *testing.T) {
	eachCalendarSetup(t, func(t *testing.T, h *calHarness) {
		h.seedWorkout(h.rider("wilant"), "Threshold 3 x 10", "2026-10-12")
		h.seedWorkout("somebodyelse", "Their private plan", "2026-10-12")
		// Numbers that must never reach a calendar server.
		if _, err := h.training.SaveProfile(t.Context(), workout.RiderProfile{Rider: h.rider("wilant"), FTPWatts: 287}); err != nil {
			t.Fatal(err)
		}
		path := h.generate("wilant")

		resp := h.do(http.MethodGet, path, nil) // no cookie, no headers
		if resp.code != http.StatusOK {
			t.Fatalf("status = %d, body %q", resp.code, resp.body)
		}
		if ct := resp.header.Get("Content-Type"); ct != "text/calendar; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		for header, want := range map[string]string{
			"Cache-Control":       "private, max-age=300",
			"Referrer-Policy":     "no-referrer",
			"X-Robots-Tag":        "noindex",
			"Content-Disposition": `inline; filename="domestique.ics"`,
		} {
			if got := resp.header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		if !strings.Contains(resp.body, "SUMMARY:Threshold 3 x 10\r\n") || !strings.Contains(resp.body, "BEGIN:VCALENDAR\r\n") {
			t.Errorf("the rider's own session is missing:\n%s", resp.body)
		}
		if strings.Contains(resp.body, "Their private plan") {
			t.Error("another rider's plan is in the feed")
		}
		if strings.Contains(resp.body, "287") {
			t.Error("the rider's FTP is in the feed")
		}
	})
}

func TestCalendarFeedIgnoresWhoIsAskingAndWhatTheyClaim(t *testing.T) {
	eachCalendarSetup(t, func(t *testing.T, h *calHarness) {
		h.seedWorkout(h.rider("wilant"), "Threshold 3 x 10", "2026-10-12")
		h.seedWorkout("mallory", "Mallory's session", "2026-10-12")
		path := h.generate("wilant")
		plain := h.do(http.MethodGet, path, nil)

		// A forged Remote-User, and (under oidc) a live session of someone
		// else, change nothing: the owner is whoever the token belongs to.
		forged := h.do(http.MethodGet, path, func(r *http.Request) {
			r.Header.Set(auth.HeaderUser, "mallory")
			r.Header.Set(auth.HeaderGroups, "gate,admins")
			h.identify(r, "mallory", "admins")
		})
		if forged.code != http.StatusOK || forged.body != plain.body {
			t.Fatalf("an identity changed the feed: %d\n%s", forged.code, forged.body)
		}
		if strings.Contains(forged.body, "Mallory") {
			t.Error("the asker's own plan was served")
		}
	})
}

func TestCalendarFeedFailuresAreOneIdentical404(t *testing.T) {
	eachCalendarSetup(t, func(t *testing.T, h *calHarness) {
		revoked := h.generate("riderb")
		if r := h.as("riderb", http.MethodDelete, "/api/training/calendar"); r.code != http.StatusNoContent {
			t.Fatalf("revoke = %d", r.code)
		}
		replaced := h.generate("riderc")
		_ = h.generate("riderc") // the first link is dead now
		// Last: under mode none every caller is the same rider, and this
		// replaces whatever was generated above.
		good := h.generate("wilant")

		// A shape-valid token that is unknown, revoked or replaced: one
		// identical 404.
		paths := map[string]string{
			"unknown":  "/api/calendar/" + strings.Repeat("A", 43) + ".ics",
			"revoked":  revoked,
			"replaced": replaced,
		}
		var ref *calResp
		for name, p := range paths {
			resp := h.do(http.MethodGet, p, nil)
			if resp.code != http.StatusNotFound {
				t.Errorf("%s: status %d, want 404", name, resp.code)
				continue
			}
			resp.header.Del("Date")
			if ref == nil {
				ref = &resp
				continue
			}
			if resp.body != ref.body || fmt.Sprint(resp.header) != fmt.Sprint(ref.header) {
				t.Errorf("%s: response differs from the others:\n%v %q\nvs\n%v %q", name, resp.header, resp.body, ref.header, ref.body)
			}
		}

		// A malformed one is not the feed path at all, so it is not exempt from
		// the gate: the same 404 where there is no gate, the gate's 401 where
		// there is. It says nothing about any real token either way.
		malformed := []string{
			"/api/calendar/abc.ics", good[:len(good)-4] + "xx.ics",
			strings.TrimSuffix(good, ".ics"), "/api/calendar/" + strings.Repeat("+", 43) + ".ics",
		}
		for _, p := range malformed {
			resp := h.do(http.MethodGet, p, nil)
			want := http.StatusUnauthorized
			if h.mode == auth.ModeNone {
				want = http.StatusNotFound
			}
			if resp.code != want {
				t.Errorf("malformed %s: status %d, want %d", p, resp.code, want)
			}
			if h.mode == auth.ModeNone && ref != nil && resp.body != ref.body {
				t.Errorf("malformed %s: body %q differs from the unknown-token 404 %q", p, resp.body, ref.body)
			}
		}
	})
}

func TestCalendarFeedRevalidatesWithETag(t *testing.T) {
	eachCalendarSetup(t, func(t *testing.T, h *calHarness) {
		h.seedWorkout(h.rider("wilant"), "Threshold 3 x 10", "2026-10-12")
		path := h.generate("wilant")
		first := h.do(http.MethodGet, path, nil)
		etag := first.header.Get("ETag")
		if etag == "" {
			t.Fatal("no ETag")
		}
		again := h.do(http.MethodGet, path, func(r *http.Request) { r.Header.Set("If-None-Match", etag) })
		if again.code != http.StatusNotModified || again.body != "" {
			t.Fatalf("conditional GET = %d %q, want an empty 304", again.code, again.body)
		}
		if again.header.Get("ETag") != etag {
			t.Error("the 304 dropped the ETag")
		}
		// An edit changes the body, so the old ETag no longer matches.
		h.seedWorkout(h.rider("wilant"), "Another day", "2026-10-13")
		changed := h.do(http.MethodGet, path, func(r *http.Request) { r.Header.Set("If-None-Match", etag) })
		if changed.code != http.StatusOK || changed.header.Get("ETag") == etag {
			t.Fatalf("a changed feed still matched the old ETag: %d", changed.code)
		}
	})
}

func TestCalendarFeedFetchIsRecordedForTheProfile(t *testing.T) {
	eachCalendarSetup(t, func(t *testing.T, h *calHarness) {
		path := h.generate("wilant")
		before := h.as("wilant", http.MethodGet, "/api/training/calendar")
		if strings.Contains(before.body, "lastFetchedAt") {
			t.Fatalf("fetched by nobody yet: %s", before.body)
		}
		h.do(http.MethodGet, path, nil)
		after := h.as("wilant", http.MethodGet, "/api/training/calendar")
		if !strings.Contains(after.body, `"lastFetchedAt":"2026-`) && !strings.Contains(after.body, `"lastFetchedAt":"20`) {
			t.Fatalf("the fetch was not recorded: %s", after.body)
		}
	})
}

// Only GET <43 chars>.ics skips authorization. Anything else under
// /api/calendar/ is an ordinary gated path, and every verb but GET falls
// through to the gate.
func TestNothingElseUnderCalendarBypassesAuth(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		for _, mode := range []auth.Mode{auth.ModeProxy, auth.ModeOIDC} {
			t.Run(engine+"/"+string(mode), func(t *testing.T) {
				h := newCalHarness(t, mode, dsn, calendarPublicURL)
				good := h.generate("wilant")
				tok := strings.TrimSuffix(strings.TrimPrefix(good, "/api/calendar/"), ".ics")
				for _, p := range []string{
					"/api/calendar/", "/api/calendar/x/y.ics", good + "/", good + ".bak", "/api/calendar/" + tok,
				} {
					if r := h.do(http.MethodGet, p, nil); r.code != http.StatusUnauthorized {
						t.Errorf("anonymous GET %s = %d, want the gate's 401", p, r.code)
					}
				}
				for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
					if r := h.do(m, good, nil); r.code != http.StatusUnauthorized {
						t.Errorf("anonymous %s on the feed path = %d, want the gate's 401", m, r.code)
					}
					// And signed in, the feed path accepts no write at all.
					if r := h.as("wilant", m, good); r.code != http.StatusNotFound {
						t.Errorf("signed-in %s on the feed path = %d, want 404", m, r.code)
					}
				}
			})
		}
	}
}

func TestCalendarFeedIsRateLimitedPerToken(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newCalHarness(t, auth.ModeProxy, dsn, calendarPublicURL)
			path := h.generate("wilant")
			other := h.generate("riderb")
			for i := 1; i <= 30; i++ {
				if r := h.do(http.MethodGet, path, nil); r.code != http.StatusOK {
					t.Fatalf("fetch %d = %d, want 200", i, r.code)
				}
			}
			if r := h.do(http.MethodGet, path, nil); r.code != http.StatusTooManyRequests {
				t.Fatalf("the 31st fetch = %d, want 429", r.code)
			}
			if r := h.do(http.MethodGet, other, nil); r.code != http.StatusOK {
				t.Fatalf("another token is throttled by the first: %d", r.code)
			}
		})
	}
}

func TestCalendarMissesShareOneGlobalBucket(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newCalHarness(t, auth.ModeProxy, dsn, calendarPublicURL)
			good := h.generate("wilant")
			for i := 1; i <= 60; i++ {
				// Each a different, well-formed, unknown token.
				p := fmt.Sprintf("/api/calendar/%043d.ics", i)
				if r := h.do(http.MethodGet, p, nil); r.code != http.StatusNotFound {
					t.Fatalf("miss %d = %d, want 404", i, r.code)
				}
			}
			if r := h.do(http.MethodGet, "/api/calendar/"+strings.Repeat("Z", 43)+".ics", nil); r.code != http.StatusTooManyRequests {
				t.Fatalf("the 61st miss = %d, want 429", r.code)
			}
			if r := h.do(http.MethodGet, good, nil); r.code != http.StatusOK {
				t.Fatalf("a real token is blocked by other people's misses: %d", r.code)
			}
		})
	}
}

// The token is the only credential, so it must not leave through our own
// logs or traces. The same goes for a route-share link, which had the leak.
func TestTokensNeverReachLogsOrSpans(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newCalHarness(t, auth.ModeProxy, dsn, calendarPublicURL)
			path := h.generate("wilant")
			tok := strings.TrimSuffix(strings.TrimPrefix(path, "/api/calendar/"), ".ics")
			shareTok := strings.Repeat("S", 43)

			h.do(http.MethodGet, path, nil)
			h.do(http.MethodGet, "/api/calendar/"+strings.Repeat("M", 43)+".ics", nil) // a miss
			h.as("wilant", http.MethodGet, "/api/shares/"+shareTok)
			h.as("wilant", http.MethodGet, "/api/shares/"+shareTok+"/track")

			// Non-canonical spellings of the same URLs: the mux would clean or
			// redirect them, but the log and the spans see them first.
			for _, odd := range []string{
				"//api/calendar/" + tok + ".ics",
				"/./api/calendar/" + tok + ".ics",
				"/api/./calendar//" + tok + ".ics",
				"/API/Calendar/" + tok + ".ICS",
				"/api/calendar/../calendar/" + tok + ".ics",
				"/api/calendar/%2e%2e/" + tok + ".ics",
				"/x/" + tok + ".ics",
				"//api//shares//" + shareTok + "/track",
				"/API/SHARES/" + shareTok,
			} {
				h.do(http.MethodGet, odd, nil)
			}

			logs := h.logs.String()
			if !strings.Contains(logs, "/api/calendar/[redacted].ics") || !strings.Contains(logs, "/api/shares/[redacted]") {
				t.Errorf("the debug request log should show the redacted path:\n%s", logs)
			}
			secrets := []string{tok, shareTok, strings.Repeat("M", 43)}
			for _, s := range secrets {
				if strings.Contains(logs, s) {
					t.Errorf("a token reached the logs:\n%s", logs)
				}
			}

			names := map[string]bool{}
			for _, sp := range h.spans.Ended() {
				names[sp.Name()] = true
				dump := sp.Name()
				for _, kv := range sp.Attributes() {
					dump += " " + string(kv.Key) + "=" + kv.Value.String()
				}
				for _, ev := range sp.Events() {
					dump += " " + ev.Name
					for _, kv := range ev.Attributes {
						dump += " " + kv.Value.String()
					}
				}
				for _, s := range secrets {
					if strings.Contains(dump, s) {
						t.Errorf("a token reached a span: %s", dump)
					}
				}
			}
			for _, want := range []string{"GET /api/calendar/[redacted].ics", "GET /api/shares/[redacted]", "GET /api/shares/[redacted]/track"} {
				if !names[want] {
					t.Errorf("no span named %q; have %v", want, names)
				}
			}
		})
	}
}

func TestCalendarManagement(t *testing.T) {
	eachCalendarSetup(t, func(t *testing.T, h *calHarness) {
		status := func() map[string]any {
			var m map[string]any
			r := h.as("wilant", http.MethodGet, "/api/training/calendar")
			if r.code != http.StatusOK {
				t.Fatalf("GET status = %d %s", r.code, r.body)
			}
			if err := json.Unmarshal([]byte(r.body), &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
		if s := status(); s["available"] != true || s["active"] != false {
			t.Fatalf("a fresh rider: %v", s)
		}

		r := h.as("wilant", http.MethodPost, "/api/training/calendar")
		var link struct {
			URL       string `json:"url"`
			WebcalURL string `json:"webcalUrl"`
		}
		if err := json.Unmarshal([]byte(r.body), &link); err != nil || r.code != http.StatusOK {
			t.Fatalf("POST = %d %s", r.code, r.body)
		}
		if !strings.HasPrefix(link.URL, calendarPublicURL+"/api/calendar/") || !strings.HasSuffix(link.URL, ".ics") {
			t.Errorf("url = %q", link.URL)
		}
		if want := "webcal://domestique.example.com" + strings.TrimPrefix(link.URL, calendarPublicURL); link.WebcalURL != want {
			t.Errorf("webcalUrl = %q, want %q", link.WebcalURL, want)
		}
		if s := status(); s["active"] != true || s["createdAt"] == nil {
			t.Fatalf("after generating: %v", s)
		}
		if strings.Contains(h.as("wilant", http.MethodGet, "/api/training/calendar").body, strings.TrimSuffix(strings.TrimPrefix(link.URL, calendarPublicURL+"/api/calendar/"), ".ics")) {
			t.Error("the status endpoint gave the token back")
		}

		first := strings.TrimPrefix(link.URL, calendarPublicURL)
		if h.do(http.MethodGet, first, nil).code != http.StatusOK {
			t.Fatal("the new link does not work")
		}
		second := h.generate("wilant")
		if second == first {
			t.Fatal("regenerating returned the same link")
		}
		if h.do(http.MethodGet, first, nil).code != http.StatusNotFound {
			t.Error("the old link survived a regenerate")
		}
		if h.do(http.MethodGet, second, nil).code != http.StatusOK {
			t.Error("the regenerated link does not work")
		}

		if d := h.as("wilant", http.MethodDelete, "/api/training/calendar"); d.code != http.StatusNoContent {
			t.Fatalf("DELETE = %d", d.code)
		}
		if h.do(http.MethodGet, second, nil).code != http.StatusNotFound {
			t.Error("a revoked link still works")
		}
		if s := status(); s["active"] != false {
			t.Fatalf("after revoking: %v", s)
		}
	})
}

func TestCalendarManagementIsPerRiderAndNeedsTheTrainingRole(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		for _, mode := range []auth.Mode{auth.ModeProxy, auth.ModeOIDC} {
			t.Run(engine+"/"+string(mode), func(t *testing.T) {
				h := newCalHarness(t, mode, dsn, calendarPublicURL)
				mine := h.generate("wilant")
				// Another rider generating, revoking and regenerating touches
				// only their own row.
				h.generate("riderb")
				h.as("riderb", http.MethodDelete, "/api/training/calendar")
				if h.do(http.MethodGet, mine, nil).code != http.StatusOK {
					t.Error("one rider's revoke killed another rider's link")
				}
				// A viewer cannot manage a feed, and nobody anonymous can.
				viewer := h.do(http.MethodPost, "/api/training/calendar", func(r *http.Request) { h.identify(r, "viewer", "guests") })
				if viewer.code != http.StatusForbidden {
					t.Errorf("viewer POST = %d, want 403", viewer.code)
				}
				if h.do(http.MethodPost, "/api/training/calendar", nil).code != http.StatusUnauthorized {
					t.Error("anonymous POST was not refused")
				}
			})
		}
	}
}

func TestCalendarGenerateIs412WithoutAPublicURL(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newCalHarness(t, auth.ModeProxy, dsn, "")
			if r := h.as("wilant", http.MethodPost, "/api/training/calendar"); r.code != http.StatusPreconditionFailed {
				t.Fatalf("POST = %d, want 412", r.code)
			}
			if !strings.Contains(h.logs.String(), "level=WARN") || !strings.Contains(h.logs.String(), "public_url") {
				t.Errorf("the 412 left no Warn:\n%s", h.logs.String())
			}
			var m map[string]any
			_ = json.Unmarshal([]byte(h.as("wilant", http.MethodGet, "/api/training/calendar").body), &m)
			if m["available"] != false {
				t.Errorf("available = %v, want false", m["available"])
			}
			// Nothing was stored for a 412.
			if st, _ := h.feeds.Status(t.Context(), "wilant"); st.Active {
				t.Error("a feed was created despite the 412")
			}
		})
	}
}

// HEAD is the same handler as GET with no body, and exempt from the gate on
// the exact feed path only.
func TestCalendarFeedAnswersHEADWithoutASession(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		for _, mode := range []auth.Mode{auth.ModeProxy, auth.ModeOIDC} {
			t.Run(engine+"/"+string(mode), func(t *testing.T) {
				h := newCalHarness(t, mode, dsn, calendarPublicURL)
				path := h.generate("wilant")
				get := h.do(http.MethodGet, path, nil)
				head := h.do(http.MethodHead, path, nil)
				if head.code != http.StatusOK || head.body != "" {
					t.Fatalf("anonymous HEAD = %d with %d body bytes, want 200 and none", head.code, len(head.body))
				}
				if head.header.Get("ETag") == "" || head.header.Get("ETag") != get.header.Get("ETag") {
					t.Errorf("HEAD ETag %q, GET ETag %q", head.header.Get("ETag"), get.header.Get("ETag"))
				}
				if head.header.Get("Content-Type") != get.header.Get("Content-Type") {
					t.Errorf("HEAD Content-Type %q differs from GET's", head.header.Get("Content-Type"))
				}
				tok := strings.TrimSuffix(strings.TrimPrefix(path, "/api/calendar/"), ".ics")
				for _, p := range []string{"/api/calendar/", "/api/calendar/x/y.ics", path + "/", "/api/calendar/" + tok} {
					if r := h.do(http.MethodHead, p, nil); r.code != http.StatusUnauthorized {
						t.Errorf("anonymous HEAD %s = %d, want the gate's 401", p, r.code)
					}
				}
				// An unknown token over HEAD is the same 404 as over GET.
				if r := h.do(http.MethodHead, "/api/calendar/"+strings.Repeat("U", 43)+".ics", nil); r.code != http.StatusNotFound {
					t.Errorf("HEAD of an unknown token = %d, want 404", r.code)
				}
			})
		}
	}
}
