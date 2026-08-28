package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/wahoo"
)

// fakeWahooUpstream stands in for api.wahooligan.com: token, profile and
// routes endpoints, just enough to exercise Exchange/Me/CreateRoute/
// UpdateRoute/DeleteRoute for real rather than mocking wahoo.Client itself.
type fakeWahooUpstream struct {
	server *httptest.Server
	// failToken, when set, makes /oauth/token answer with this error code
	// instead of a token — for the "wahoo did not accept the code" path.
	failToken string
	// tokenCalls counts /oauth/token hits, so a refresh can be told apart
	// from the original exchange by the access token it hands back.
	tokenCalls int
	// nextRouteID is what the next POST /v1/routes returns as "id".
	nextRouteID int

	// createdRoutes/updatedRoutes/deletedRoutes/routeAuth record what
	// reached the routes endpoints, for tests that care what a push sent.
	createdRoutes []url.Values
	updatedRoutes map[string]url.Values
	deletedRoutes []string
	routeAuth     []string

	// listedRoutes is what GET /v1/routes answers with — set directly by
	// sync-back tests, JSON-encoded verbatim so a test can shape exactly
	// the fields it cares about (deleted, external_id, ...) without this
	// fake needing its own copy of wahoo.Route.
	listedRoutes []map[string]any
	// files serves the FIT bytes addRoute pointed a listed route's
	// file.url at — this fake's stand-in for Wahoo's CDN, same host as the
	// API itself so DownloadRoute's own-host check attaches the bearer
	// token the same way a real cross-host CDN link would not (that case
	// is covered at the wahoo package's own level, in wahoo_test.go).
	files    map[string][]byte
	fileAuth []string
}

// addRoute appends a route to what GET /v1/routes returns, pointing its
// file at fitBytes served from this same fake server. route's "id" key
// decides the file's name; callers set every other field GET /v1/routes
// hands back that the test cares about.
func (f *fakeWahooUpstream) addRoute(route map[string]any, fitBytes []byte) {
	name := fmt.Sprintf("route-%v.fit", route["id"])
	route["file"] = map[string]string{"url": f.server.URL + "/files/" + name}
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[name] = fitBytes
	f.listedRoutes = append(f.listedRoutes, route)
}

func newFakeWahooUpstream(t *testing.T) *fakeWahooUpstream {
	t.Helper()
	f := &fakeWahooUpstream{nextRouteID: 1, updatedRoutes: map[string]url.Values{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if f.failToken != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": f.failToken})
			return
		}
		f.tokenCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("at-%d", f.tokenCalls), "refresh_token": "rt-1",
			"expires_in": 3600, "scope": "user_read routes_read routes_write",
		})
	})
	mux.HandleFunc("/v1/user", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(wahoo.Profile{ID: 7, Email: "rider@example.test", First: "Rider", Last: "One"})
	})
	mux.HandleFunc("/v1/routes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			out := f.listedRoutes
			if out == nil {
				out = []map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		f.routeAuth = append(f.routeAuth, r.Header.Get("Authorization"))
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		f.createdRoutes = append(f.createdRoutes, r.PostForm)
		id := f.nextRouteID
		f.nextRouteID++
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
	})
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		f.fileAuth = append(f.fileAuth, r.Header.Get("Authorization"))
		name := strings.TrimPrefix(r.URL.Path, "/files/")
		data, ok := f.files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/v1/routes/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/routes/")
		f.routeAuth = append(f.routeAuth, r.Header.Get("Authorization"))
		switch r.Method {
		case http.MethodPut:
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			f.updatedRoutes[id] = r.PostForm
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
		case http.MethodDelete:
			f.deletedRoutes = append(f.deletedRoutes, id)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

type wahooHarness struct {
	t        *testing.T
	client   *http.Client
	base     string
	box      *secrets.Box
	links    *providerlink.Store
	accounts *accounts.Store
	store    state.Store
	upstream *fakeWahooUpstream
	db       *source.DB
}

// newWahooHarness builds a server with Wahoo configured against a fake
// upstream. withKey controls whether an encryption key is set — the same
// "reads work, writes refuse" case every provider connection has to handle.
func newWahooHarness(t *testing.T, withKey bool) *wahooHarness {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

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
	accountStore, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	stateStore, err := state.UseDB(db.Conn(), db.DSN())
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

	upstream := newFakeWahooUpstream(t)

	srv := &api.Server{
		Source:   db,
		Store:    stateStore,
		Auth:     authenticator,
		Links:    links,
		Accounts: accountStore,
		Box:      box,
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	// The redirect_uri has to be this harness's own address, decided only
	// once the server is up — the same two-stage construction
	// newSSOHarness uses for the same reason.
	wahooClient := wahoo.New(wahoo.Config{
		ClientID: "test-client", ClientSecret: "test-secret",
		RedirectURL: server.URL + "/wahoo/callback",
	})
	wahooClient.APIBase = upstream.server.URL
	srv.Wahoo = wahooClient

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	return &wahooHarness{
		t: t, client: client, base: server.URL, box: box,
		links: links, accounts: accountStore, store: stateStore, upstream: upstream, db: db,
	}
}

// as issues a request as a user in the given groups. body is optional and
// variadic so every existing no-body call site stays untouched — passing
// one string sends it as a JSON request body.
func (h *wahooHarness) as(user, groups, method, path string, body ...string) *http.Response {
	h.t.Helper()
	var reader io.Reader
	if len(body) > 0 && body[0] != "" {
		reader = strings.NewReader(body[0])
	}
	req, err := http.NewRequest(method, h.base+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Remote-User", user)
	req.Header.Set("Remote-Groups", groups)
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// connect drives a real /wahoo/connect -> (fake wahoo) -> /wahoo/callback
// round trip using this harness's own cookie jar, exactly as a browser
// would. Returns the callback's response.
func (h *wahooHarness) connect(user, groups string) *http.Response {
	h.t.Helper()
	connectResp := h.as(user, groups, http.MethodGet, "/wahoo/connect")
	if connectResp.StatusCode != http.StatusFound {
		h.t.Fatalf("GET /wahoo/connect = %d, want 302", connectResp.StatusCode)
	}
	loc, err := connectResp.Location()
	if err != nil {
		h.t.Fatal(err)
	}
	state := loc.Query().Get("state")
	return h.as(user, groups, http.MethodGet, "/wahoo/callback?code=a-code&state="+state)
}

func TestWahooConnectRedirectsToAuthorizeWithSealedCookie(t *testing.T) {
	h := newWahooHarness(t, true)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/wahoo/connect")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	loc, err := resp.Location()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc.String(), h.upstream.server.URL+"/oauth/authorize?") {
		t.Fatalf("redirected to %s, want the fake wahoo authorize endpoint", loc)
	}
	q := loc.Query()
	if q.Get("client_id") != "test-client" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("state") == "" {
		t.Fatal("no state in the authorize redirect")
	}

	var sawStateCookie bool
	for _, c := range resp.Cookies() {
		if c.Name == "domestique_wahoo_state" {
			sawStateCookie = true
		}
	}
	if !sawStateCookie {
		t.Fatal("no state cookie was set")
	}
}

func TestWahooCallbackStoresConnectionAndLinksAccount(t *testing.T) {
	h := newWahooHarness(t, true)

	resp := h.connect("wilant", "cyclists")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", resp.StatusCode)
	}
	if loc, _ := resp.Location(); loc == nil || loc.Path != "/" {
		t.Fatalf("callback redirected to %v, want /", loc)
	}

	link, err := h.links.Get("wahoo", "wilant")
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	if link.Email != "rider@example.test" {
		t.Fatalf("email = %q", link.Email)
	}
	if link.DisplayName != "Rider One" {
		t.Fatalf("display name = %q", link.DisplayName)
	}

	acc, err := h.accounts.Get(t.Context(), accounts.ID(model.ProviderWahoo, "wilant"))
	if err != nil {
		t.Fatalf("account was not linked: %v", err)
	}
	if acc.Rider != "wilant" {
		t.Fatalf("account rider = %q", acc.Rider)
	}

	dto := decodeConnection(t, h.as("wilant", "cyclists", http.MethodGet, "/api/wahoo/connection"))
	if dto["connected"] != true {
		t.Fatalf("connection dto = %v, want connected: true", dto)
	}
}

func TestWahooCallbackFailsClosedWithoutTheStateCookie(t *testing.T) {
	h := newWahooHarness(t, true)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/wahoo/callback?code=a-code&state=whatever")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if _, err := h.links.Get("wahoo", "wilant"); err == nil {
		t.Fatal("a connection was stored despite no state cookie")
	}
}

func TestWahooCallbackFailsClosedOnStateMismatch(t *testing.T) {
	h := newWahooHarness(t, true)

	connectResp := h.as("wilant", "cyclists", http.MethodGet, "/wahoo/connect")
	if connectResp.StatusCode != http.StatusFound {
		t.Fatalf("connect status = %d, want 302", connectResp.StatusCode)
	}

	// The cookie from /wahoo/connect is in the jar; the state in the query
	// string does not match what was sealed into it.
	resp := h.as("wilant", "cyclists", http.MethodGet, "/wahoo/callback?code=a-code&state=not-the-real-state")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if _, err := h.links.Get("wahoo", "wilant"); err == nil {
		t.Fatal("a connection was stored despite a state mismatch")
	}
}

func TestWahooCallbackSurfacesAnUpstreamExchangeFailure(t *testing.T) {
	h := newWahooHarness(t, true)
	h.upstream.failToken = "invalid_grant"

	resp := h.connect("wilant", "cyclists")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if _, err := h.links.Get("wahoo", "wilant"); err == nil {
		t.Fatal("a connection was stored despite a failed exchange")
	}
}

func TestWahooDisconnectRemovesConnectionAndAccount(t *testing.T) {
	h := newWahooHarness(t, true)
	h.connect("wilant", "cyclists")

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/wahoo/connection")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disconnect status = %d, want 200", resp.StatusCode)
	}

	if _, err := h.links.Get("wahoo", "wilant"); err == nil {
		t.Fatal("connection survived disconnect")
	}
	if _, err := h.accounts.Get(t.Context(), accounts.ID(model.ProviderWahoo, "wilant")); err == nil {
		t.Fatal("account survived disconnect")
	}
}

// A viewer may not link head units, and connecting Wahoo is linking one.
func TestWahooEndpointsNeedAccountPermission(t *testing.T) {
	h := newWahooHarness(t, true)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/wahoo/connect"},
		{http.MethodGet, "/api/wahoo/connection"},
		{http.MethodDelete, "/api/wahoo/connection"},
	} {
		resp := h.as("guest", "guests", tc.method, tc.path)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s status = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestWahooConnectionReportsWhyItCannotConnect(t *testing.T) {
	for _, tc := range []struct {
		name          string
		withKey       bool
		noWahoo       bool
		wantAvailable bool
	}{
		{name: "no encryption key", withKey: false, wantAvailable: false},
		{name: "configured", withKey: true, wantAvailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWahooHarness(t, tc.withKey)
			dto := decodeConnection(t, h.as("wilant", "cyclists", http.MethodGet, "/api/wahoo/connection"))
			canConnect, _ := dto["canConnect"].(bool)
			if canConnect != tc.wantAvailable {
				t.Fatalf("canConnect = %v, want %v (dto: %v)", canConnect, tc.wantAvailable, dto)
			}
			if !tc.wantAvailable && dto["unavailable"] == "" {
				t.Fatal("canConnect is false but unavailable says nothing")
			}
		})
	}
}

func TestWahooHandlersSurviveNoWahooClient(t *testing.T) {
	srv := &api.Server{Auth: noAuth(t)}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/wahoo/connection", http.StatusOK},
		{http.MethodGet, "/wahoo/connect", http.StatusNotImplemented},
		{http.MethodGet, "/wahoo/callback", http.StatusNotImplemented},
		{http.MethodDelete, "/api/wahoo/connection", http.StatusOK},
	} {
		req, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
}
