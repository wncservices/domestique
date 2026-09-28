package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/garminmfa"
	"github.com/wncservices/domestique/apps/api/internal/komoot"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

// fakeConnector records what it was asked to sign in with, so a test can
// assert the password went to Komoot and nowhere else.
type fakeConnector struct {
	email    string
	password string
	fail     bool

	resumedUser  string
	resumedToken string

	// importer, when set, is what Resume hands back — for tests that care
	// what the import loop does rather than that it happened.
	importer api.KomootImporter
}

type fakeImporter struct{ tours []komoot.Tour }

func (f *fakeImporter) Tours(context.Context, bool) ([]komoot.Tour, error) { return f.tours, nil }
func (f *fakeImporter) GPX(context.Context, string) ([]byte, error)        { return []byte("<gpx/>"), nil }
func (f *fakeImporter) DeleteTour(context.Context, string) error           { return nil }

func (c *fakeConnector) Connect(_ context.Context, email, password string) (api.KomootImporter, api.KomootSession, error) {
	c.email, c.password = email, password
	if c.fail {
		return nil, api.KomootSession{}, http.ErrNotSupported
	}
	return &fakeImporter{}, api.KomootSession{
		UserID:      "komoot-user-1",
		Token:       "komoot-token-1",
		DisplayName: "Wilant N",
	}, nil
}

func (c *fakeConnector) Resume(userID, token string) api.KomootImporter {
	c.resumedUser, c.resumedToken = userID, token
	if c.importer != nil {
		return c.importer
	}
	return &fakeImporter{tours: []komoot.Tour{{ID: "42", Name: "A tour"}}}
}

type connectHarness struct {
	t         *testing.T
	client    *http.Client
	base      string
	links     *providerlink.Store
	mfa       *garminmfa.Store
	connector *fakeConnector
	garmin    *fakeGarmin
	settings  *settings.Store
	accounts  *accounts.Store
	db        *source.DB
	store     state.Store
	// srv is the server itself, not just its HTTP handler — for a test
	// that needs to call an exported method directly (AutoImportTick,
	// notably) rather than going through a request.
	srv *api.Server
}

// newConnectHarness builds a server with Komoot enabled and no environment
// account, which is the deployment this feature is for. opts can set
// additional Server fields (ConnectLimiter, for the rate-limit tests)
// before the server starts serving, the same shape newSSOHarness's opts
// already use.
func newConnectHarness(t *testing.T, withKey bool, opts ...func(*api.Server)) *connectHarness {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	var box *secrets.Box
	if withKey {
		key, err := secrets.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		if box, err = secrets.New(key); err != nil {
			t.Fatal(err)
		}
	}

	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	mfaStore, err := garminmfa.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := auth.New(auth.Config{
		Mode:  auth.ModeProxy,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	appSettings, err := settings.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}

	// A consumer pair in the environment, so the default harness is a
	// deployment where Garmin sign-in is available. Set explicitly rather than
	// inherited: whether these tests pass must not depend on whether the
	// machine running them has real Garmin credentials exported.
	t.Setenv(garmin.EnvConsumerKey, "test-consumer-key")
	t.Setenv(garmin.EnvConsumerSecret, "test-consumer-secret")

	connector := &fakeConnector{}
	garminConnector := &fakeGarmin{}
	accountStore := seedRoleAccounts(t, db)
	srv := &api.Server{
		Source:        db,
		Store:         store,
		Auth:          authenticator,
		Accounts:      accountStore,
		Links:         links,
		GarminMFA:     mfaStore,
		Connector:     connector,
		Garmin:        garminConnector,
		Settings:      appSettings,
		KomootEnabled: true,
		Config:        &config.Config{Komoot: config.KomootConfig{Enabled: true}},
	}
	for _, opt := range opts {
		opt(srv)
	}

	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	return &connectHarness{t: t, client: server.Client(), base: server.URL,
		links: links, mfa: mfaStore, connector: connector, garmin: garminConnector,
		settings: appSettings, accounts: accountStore, db: db, store: store, srv: srv}
}

func (h *connectHarness) as(user, groups, method, path, body string) *http.Response {
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

func decodeConnection(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConnectStoresTheSessionAndNotThePassword(t *testing.T) {
	h := newConnectHarness(t, true)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"rider@example.com","password":"hunter2"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body := decodeConnection(t, resp)
	if body["connected"] != true || body["displayName"] != "Wilant N" {
		t.Errorf("body = %v, want a connected account named Wilant N", body)
	}

	// The password reached Komoot...
	if h.connector.password != "hunter2" {
		t.Errorf("connector saw password %q", h.connector.password)
	}

	// ...and nowhere else. This is the reason the feature is built this way.
	var rows int
	if err := h.db.Conn().QueryRow(
		`SELECT COUNT(*) FROM provider_links WHERE CAST(secret AS TEXT) LIKE '%hunter2%'`).
		Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("the password appears in the stored token")
	}

	userID, token, err := h.links.Secret("komoot", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if userID != "komoot-user-1" || token != "komoot-token-1" {
		t.Errorf("stored %q/%q, want the session Komoot returned", userID, token)
	}
}

// The rider is whoever is signed in. Letting the body decide would let one
// rider attach an account to another — the same rule as linking a head unit.
func TestConnectIgnoresARiderInTheBody(t *testing.T) {
	h := newConnectHarness(t, true)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"r@example.com","password":"pw","rider":"someone-else"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if _, err := h.links.Get("komoot", "wilant"); err != nil {
		t.Errorf("the connection was not stored against the session rider: %v", err)
	}
	if _, err := h.links.Get("komoot", "someone-else"); err == nil {
		t.Error("the body's rider was honoured")
	}
}

func TestConnectRefusedWithoutAnEncryptionKey(t *testing.T) {
	h := newConnectHarness(t, false)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"r@example.com","password":"pw"}`)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
	// Refused before signing in: no point sending someone's password to
	// Komoot when the result cannot be kept.
	if h.connector.password != "" {
		t.Error("the password was sent to Komoot despite the refusal")
	}
}

func TestConnectionReportsWhetherConnectingIsPossible(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withKey    bool
		canConnect bool
	}{
		{"with a key", true, true},
		{"without a key", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newConnectHarness(t, tc.withKey)
			body := decodeConnection(t,
				h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/connection", ""))

			if body["canConnect"] != tc.canConnect {
				t.Errorf("canConnect = %v, want %v", body["canConnect"], tc.canConnect)
			}
			if body["connected"] != false {
				t.Errorf("connected = %v, want false before connecting", body["connected"])
			}
		})
	}
}

// A rider's own connection is used for their imports, not somebody else's and
// not the deployment-wide one.
func TestToursUseTheCallersOwnConnection(t *testing.T) {
	h := newConnectHarness(t, true)

	h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"r@example.com","password":"pw"}`)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/tours", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if h.connector.resumedToken != "komoot-token-1" {
		t.Errorf("resumed with %q, want the stored token", h.connector.resumedToken)
	}

	// A different rider has connected nothing, and there is no shared
	// account, so there is nothing to import from. 412 rather than 501: this
	// deployment can store a sign-in, so what is missing is theirs to supply.
	other := h.as("someone", "cyclists", http.MethodGet, "/api/komoot/tours", "")
	if other.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status for an unconnected rider = %d, want 412", other.StatusCode)
	}
}

func TestDisconnectRemovesOnlyTheCallersConnection(t *testing.T) {
	h := newConnectHarness(t, true)

	h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"a@example.com","password":"pw"}`)
	h.as("friend", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"b@example.com","password":"pw"}`)

	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/komoot/connection", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if _, err := h.links.Get("komoot", "wilant"); err == nil {
		t.Error("the caller's connection survived")
	}
	if _, err := h.links.Get("komoot", "friend"); err != nil {
		t.Errorf("another rider's connection was removed: %v", err)
	}
}

// Komoot rejecting the details must not leave a half-made connection, and the
// response must not echo Komoot's own error, which can contain the request.
func TestFailedLoginStoresNothing(t *testing.T) {
	h := newConnectHarness(t, true)
	h.connector.fail = true

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"r@example.com","password":"wrong"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}

	body := decodeConnection(t, resp)
	if msg, _ := body["error"].(string); strings.Contains(msg, "wrong") {
		t.Errorf("the error echoes the password: %q", msg)
	}
	if _, err := h.links.Get("komoot", "wilant"); err == nil {
		t.Error("a connection was stored despite the failed login")
	}
}

// handleKomootConnect proxies a password straight to Komoot — the same
// credential-stuffing-proxy risk Garmin connect carries, see
// TestGarminConnectIsRateLimitedPerRider.
func TestKomootConnectIsRateLimitedPerRider(t *testing.T) {
	h := newConnectHarness(t, true, func(s *api.Server) {
		s.ConnectLimiter = ratelimit.New(1, time.Hour)
	})

	first := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"r@example.com","password":"wrong"}`)
	if first.StatusCode == http.StatusTooManyRequests {
		t.Fatal("first attempt was already rate limited")
	}
	second := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"r@example.com","password":"wrong-again"}`)
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second attempt: status = %d, want 429", second.StatusCode)
	}
}

func TestConnectNeedsTheKomootPermission(t *testing.T) {
	h := newConnectHarness(t, true)

	// A viewer may read routes and nothing else.
	resp := h.as("guest", "", http.MethodPost, "/api/komoot/connection",
		`{"email":"r@example.com","password":"pw"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if h.connector.password != "" {
		t.Error("a viewer's details were sent to Komoot")
	}
}

func TestConnectRejectsMissingFields(t *testing.T) {
	h := newConnectHarness(t, true)

	for _, body := range []string{`{"email":"","password":"pw"}`, `{"email":"a@b.c","password":""}`, `{`} {
		resp := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q gave status %d, want 400", body, resp.StatusCode)
		}
	}
}

// slowImporter counts how many fetches overlap, so the test can tell
// concurrency from a sequential loop that merely finished.
type slowImporter struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	calls    int
	failFor  string
}

func (s *slowImporter) Tours(context.Context, bool) ([]komoot.Tour, error) {
	out := make([]komoot.Tour, 0, 12)
	for i := range 12 {
		out = append(out, komoot.Tour{ID: fmt.Sprintf("tour-%d", i), Name: fmt.Sprintf("Tour %d", i)})
	}
	return out, nil
}

func (s *slowImporter) DeleteTour(context.Context, string) error { return nil }

func (s *slowImporter) GPX(_ context.Context, id string) ([]byte, error) {
	s.mu.Lock()
	s.inFlight++
	s.calls++
	if s.inFlight > s.peak {
		s.peak = s.inFlight
	}
	s.mu.Unlock()

	time.Sleep(20 * time.Millisecond)

	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()

	if id == s.failFor {
		return nil, errors.New("komoot said no")
	}
	return []byte(`<?xml version="1.0"?><gpx version="1.1" creator="t"><trk><trkseg>` +
		`<trkpt lat="50.79" lon="2.81"><ele>50</ele></trkpt>` +
		`<trkpt lat="50.80" lon="2.82"><ele>60</ele></trkpt>` +
		`</trkseg></trk></gpx>`), nil
}

// The bug this fixes: thirty tours meant thirty sequential round trips with no
// response byte until the last, and the browser gave up first.
func TestImportFetchesToursConcurrently(t *testing.T) {
	h := newConnectHarness(t, true)
	slow := &slowImporter{}
	h.connector.importer = slow

	h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"r@example.com","password":"pw"}`)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/import",
		`{"tourIds":["tour-0","tour-1","tour-2","tour-3","tour-4","tour-5","tour-6","tour-7"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	slow.mu.Lock()
	peak := slow.peak
	slow.mu.Unlock()
	if peak < 2 {
		t.Errorf("peak concurrent fetches = %d; the import is still sequential", peak)
	}
}

// A tour Komoot refuses must not abandon the others, and must be reported.
func TestImportSurvivesOneBadTour(t *testing.T) {
	h := newConnectHarness(t, true)
	h.connector.importer = &slowImporter{failFor: "tour-1"}

	h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"r@example.com","password":"pw"}`)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/import",
		`{"tourIds":["tour-0","tour-1","tour-2"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Imported []string          `json:"imported"`
		Skipped  map[string]string `json:"skipped"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Imported) != 2 {
		t.Errorf("imported %v, want the two good tours", body.Imported)
	}
	if _, ok := body.Skipped["tour-1"]; !ok {
		t.Errorf("skipped = %v, want tour-1 reported", body.Skipped)
	}
}

// A rider who has not connected Komoot yet is not a broken deployment. The
// message they get has to send them to Settings, where they can fix it, and
// must not name KOMOOT_EMAIL — that advice predates per-rider sign-in and
// points at a Deployment nobody needs to edit. Per-rider credentials have no
// environment equivalent at all once there is more than one rider.
func TestKomootNotSignedInPointsAtSettings(t *testing.T) {
	h := newConnectHarness(t, true)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/tours", "")
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412 for a rider who has not signed in", resp.StatusCode)
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "Settings") {
		t.Errorf("error = %q, want it to send the rider to Settings", body.Error)
	}
	if strings.Contains(body.Error, "KOMOOT_EMAIL") {
		t.Errorf("error = %q, still names an environment variable the rider cannot set", body.Error)
	}
}

// Without an encryption key the store refuses to hold a sign-in, so the UI
// route really is closed and the environment is the only way in. This is the
// one case where naming the variables is still the right answer.
func TestKomootWithoutAKeyStillNamesTheEnvironment(t *testing.T) {
	h := newConnectHarness(t, false)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/tours", "")
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 when nothing can be stored", resp.StatusCode)
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "KOMOOT_EMAIL") {
		t.Errorf("error = %q, want the environment named when it is the only way in", body.Error)
	}
}
