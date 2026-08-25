package api_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/blocklist"
	"github.com/wncservices/domestique/apps/api/internal/oidcflow"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/source"

	"github.com/wncservices/domestique/apps/api/internal/api"
)

// --- a minimal, genuinely working fake issuer ---
//
// Same shape as internal/oidcflow's own fake, unexported there so it cannot
// be reused across packages: go-oidc verifies signatures for real against
// whatever JWKS its discovery document advertises, so a canned JSON blob
// would not exercise anything.

type fakeIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string
	claims map[string]any
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIssuer{key: key, kid: "test-key"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 f.server.URL,
			"authorization_endpoint": f.server.URL + "/authorize",
			"token_endpoint":         f.server.URL + "/token",
			"jwks_uri":               f.server.URL + "/jwks",
			"end_session_endpoint":   f.server.URL + "/end-session",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{
			map[string]any{
				"kty": "RSA", "kid": f.kid, "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.PublicKey.E)).Bytes()),
			},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		raw, err := signJWT(f.key, f.kid, f.claims)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": raw})
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeIssuer) setClaims(nonce string, groups []string) {
	now := time.Now()
	f.claims = map[string]any{
		"iss": f.server.URL, "sub": "auth0|wilant", "aud": "domestique-test",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": nonce,
		"preferred_username": "wilant", "groups": groups,
	}
}

// setClaimsWithUser is setClaims minus preferred_username, plus whatever the
// caller supplies — for exercising identityFromToken's fallback chain
// (nickname, then sub) the way an Auth0 database-connection token actually
// looks, rather than the happy path every other test uses.
func (f *fakeIssuer) setClaimsWithUser(nonce string, groups []string, userClaims map[string]any) {
	now := time.Now()
	f.claims = map[string]any{
		"iss": f.server.URL, "sub": "auth0|64f2a1b2c3d4e5f6", "aud": "domestique-test",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": nonce,
		"groups": groups,
	}
	for k, v := range userClaims {
		f.claims[k] = v
	}
}

// loginWithUser is h.login, but for setClaimsWithUser's fallback-chain cases
// instead of the standard preferred_username happy path.
func (h *ssoHarness) loginWithUser(groups []string, userClaims map[string]any) *http.Response {
	h.t.Helper()
	loginResp := h.get("/sso/login")
	if loginResp.StatusCode != http.StatusFound {
		h.t.Fatalf("GET /sso/login = %d, want 302", loginResp.StatusCode)
	}
	loc, err := loginResp.Location()
	if err != nil {
		h.t.Fatal(err)
	}
	nonce := loc.Query().Get("nonce")
	state := loc.Query().Get("state")

	h.issuer.setClaimsWithUser(nonce, groups, userClaims)
	return h.get("/sso/callback?code=any-code&state=" + state)
}

func signJWT(key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	h, err := b64JSON(map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid})
	if err != nil {
		return "", err
	}
	p, err := b64JSON(claims)
	if err != nil {
		return "", err
	}
	signingInput := h + "." + p
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64JSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// --- acceptance harness: the real HTTP surface, cookies and all ---

type ssoHarness struct {
	t      *testing.T
	client *http.Client
	base   string
	issuer *fakeIssuer
}

// newSSOHarness builds a working ModeOIDC server against the fake issuer.
// opts can set additional Server fields (People, for the self-service
// profile tests) before the server starts serving — safe to apply either
// side of httptest.NewServer, since Handler() reads srv's fields per
// request off the same pointer, not a snapshot taken at construction.
func newSSOHarness(t *testing.T, opts ...func(*api.Server)) *ssoHarness {
	t.Helper()
	issuer := newFakeIssuer(t)

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}

	db, err := source.OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sessionStore, err := sessions.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := auth.New(auth.Config{
		Mode:  auth.ModeOIDC,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}},
		OIDC: auth.OIDCConfig{
			Issuer: issuer.server.URL, ClientID: "domestique-test",
			RedirectURL: "will be overwritten below",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	authenticator.UseSessions(sessionStore)

	flow, err := oidcflow.New(context.Background(), oidcflow.Config{
		Issuer: issuer.server.URL, ClientID: "domestique-test", ClientSecret: "test-secret",
		Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{Auth: authenticator, OIDC: flow, Sessions: sessionStore, Box: box}
	for _, opt := range opts {
		opt(srv)
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	// The redirect_uri registered with the (fake) issuer has to be an
	// absolute URL on this harness's own address, decided only once the
	// server is up.
	authenticator2, err := auth.New(auth.Config{
		Mode:  auth.ModeOIDC,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}},
		OIDC: auth.OIDCConfig{
			Issuer: issuer.server.URL, ClientID: "domestique-test",
			RedirectURL: server.URL + "/sso/callback",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	authenticator2.UseSessions(sessionStore)
	srv.Auth = authenticator2

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
		// Inspect each redirect ourselves rather than following it — the
		// test needs the Location header (to reach the fake issuer, and
		// later to confirm ReturnTo) and the intermediate cookies.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	return &ssoHarness{t: t, client: client, base: server.URL, issuer: issuer}
}

func (h *ssoHarness) get(path string) *http.Response {
	h.t.Helper()
	resp, err := h.client.Get(h.base + path)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (h *ssoHarness) post(path string) *http.Response {
	h.t.Helper()
	resp, err := h.client.Post(h.base+path, "", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// login drives a real /sso/login -> (fake issuer) -> /sso/callback round
// trip using this harness's own cookie jar, exactly as a browser would.
// Returns the callback's response so the caller can assert on where it
// redirected.
func (h *ssoHarness) login(groups []string) *http.Response {
	h.t.Helper()
	loginResp := h.get("/sso/login")
	if loginResp.StatusCode != http.StatusFound {
		h.t.Fatalf("GET /sso/login = %d, want 302", loginResp.StatusCode)
	}
	loc, err := loginResp.Location()
	if err != nil {
		h.t.Fatal(err)
	}
	nonce := loc.Query().Get("nonce")
	state := loc.Query().Get("state")

	h.issuer.setClaims(nonce, groups)
	return h.get("/sso/callback?code=any-code&state=" + state)
}

func meBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSSOLoginRedirectsToTheIssuer(t *testing.T) {
	h := newSSOHarness(t)
	resp := h.get("/sso/login")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	loc, err := resp.Location()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc.String(), h.issuer.server.URL+"/authorize") {
		t.Errorf("redirected to %s, want the issuer's authorize endpoint", loc)
	}
	for _, param := range []string{"state", "nonce", "code_challenge", "client_id"} {
		if loc.Query().Get(param) == "" {
			t.Errorf("authorize URL missing %q: %s", param, loc)
		}
	}
}

// The whole path: login, land back authenticated, /api/me reflects who —
// including a role resolved from the groups the issuer sent, not trusted
// from the token directly.
func TestSSOCallbackSignsTheRiderIn(t *testing.T) {
	h := newSSOHarness(t)
	callback := h.login([]string{"cyclists"})

	if callback.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callback.StatusCode)
	}
	if loc, _ := callback.Location(); loc == nil || loc.Path != "/" {
		t.Errorf("callback redirected to %v, want the default \"/\"", loc)
	}

	me := meBody(t, h.get("/api/me"))
	if me["authenticated"] != true {
		t.Fatalf("me = %v, want authenticated", me)
	}
	if me["user"] != "wilant" {
		t.Errorf("user = %v", me["user"])
	}
	if me["role"] != "rider" {
		t.Errorf("role = %v, want it resolved from the cyclists group", me["role"])
	}
	if me["authMode"] != "oidc" {
		t.Errorf("authMode = %v", me["authMode"])
	}
}

// The common real-world case for Auth0's database connection: no
// preferred_username, but nickname is there and legal as a rider id.
func TestSSOCallbackFallsBackToNicknameWhenNoPreferredUsername(t *testing.T) {
	h := newSSOHarness(t)
	callback := h.loginWithUser([]string{"cyclists"}, map[string]any{"nickname": "wilant"})

	if callback.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callback.StatusCode)
	}
	me := meBody(t, h.get("/api/me"))
	if me["user"] != "wilant" {
		t.Errorf("user = %v, want the nickname claim", me["user"])
	}
}

// A nickname that cannot be an account id (spaces, here) must not become the
// rider — that would only fail later, one step after login looked like it
// worked, the first time this rider tried to link an account.
func TestSSOCallbackSkipsAnIllegalNicknameAndFallsBackToSub(t *testing.T) {
	h := newSSOHarness(t)
	callback := h.loginWithUser([]string{"cyclists"}, map[string]any{"nickname": "wil ant!"})

	if callback.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callback.StatusCode)
	}
	me := meBody(t, h.get("/api/me"))
	if me["user"] != "auth0|64f2a1b2c3d4e5f6" {
		t.Errorf("user = %v, want the sub", me["user"])
	}
}

// The real case this was found against: a nickname left at Auth0's
// auto-generated default doesn't match an already-established rider, while
// name — the field the Auth0 dashboard's own user page prompts an admin to
// edit — does, once lower-cased. name has to win, or the nickname keeps
// splitting the same person into two riders every time they sign in.
func TestSSOCallbackPrefersNameOverNickname(t *testing.T) {
	h := newSSOHarness(t)
	callback := h.loginWithUser([]string{"cyclists"}, map[string]any{
		"name": "Wilant", "nickname": "wilant.nackaerts",
	})

	if callback.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callback.StatusCode)
	}
	me := meBody(t, h.get("/api/me"))
	if me["user"] != "wilant" {
		t.Errorf("user = %v, want the lower-cased name claim", me["user"])
	}
}

// name can be exactly as illegal a rider id as nickname (a full "First
// Last" carries a space) — same skip-and-fall-through rule applies.
func TestSSOCallbackSkipsAnIllegalNameAndFallsBackToNickname(t *testing.T) {
	h := newSSOHarness(t)
	callback := h.loginWithUser([]string{"cyclists"}, map[string]any{
		"name": "Wilant Nackaerts", "nickname": "wilant",
	})

	if callback.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callback.StatusCode)
	}
	me := meBody(t, h.get("/api/me"))
	if me["user"] != "wilant" {
		t.Errorf("user = %v, want the nickname claim (name has a space)", me["user"])
	}
}

// preferred_username still wins when an issuer sends both — nickname is
// only ever the fallback, never preferred over the claim meant for this.
func TestSSOCallbackPrefersPreferredUsernameOverNickname(t *testing.T) {
	h := newSSOHarness(t)
	callback := h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "official-name", "nickname": "wilant",
	})

	if callback.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callback.StatusCode)
	}
	me := meBody(t, h.get("/api/me"))
	if me["user"] != "official-name" {
		t.Errorf("user = %v, want preferred_username", me["user"])
	}
}

// return_to carries the rider back to where they started, and only there —
// safeReturnTo is what stands between this and an open redirect.
func TestSSOLoginReturnToIsHonouredAndValidated(t *testing.T) {
	h := newSSOHarness(t)

	good := h.get("/sso/login?return_to=/settings")
	loc, err := good.Location()
	if err != nil {
		t.Fatal(err)
	}
	nonce := loc.Query().Get("nonce")
	h.issuer.setClaims(nonce, nil)
	callback := h.get("/sso/callback?code=x&state=" + loc.Query().Get("state"))
	if got, _ := callback.Location(); got == nil || got.Path != "/settings" {
		t.Errorf("redirected to %v, want /settings", got)
	}

	bad := h.get("/sso/login?return_to=https://evil.example/steal")
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("an absolute return_to: status = %d, want 400", bad.StatusCode)
	}
	bad2 := h.get("/sso/login?return_to=//evil.example/steal")
	if bad2.StatusCode != http.StatusBadRequest {
		t.Errorf("a protocol-relative return_to: status = %d, want 400", bad2.StatusCode)
	}
}

// The shape a rider actually reaches routinely: the tenant's own post-login
// Action denies a login on purpose mid-provisioning (linking an identity,
// granting a new signup its roles) and asks for a second attempt. This must
// land back in the app as a toast, not a raw JSON blob — /sso/callback is a
// top-level browser navigation, and nothing else ever renders its response.
func TestSSOCallbackAccessDeniedRedirectsWithANotice(t *testing.T) {
	h := newSSOHarness(t)
	loginResp := h.get("/sso/login?return_to=/settings")
	loc, err := loginResp.Location()
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")

	const message = "Welcome to Domestique! Your account is ready — please sign in again."
	resp := h.get("/sso/callback?error=access_denied&error_description=" +
		url.QueryEscape(message) + "&state=" + state)

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	redirect, err := resp.Location()
	if err != nil {
		t.Fatal(err)
	}
	if redirect.Path != "/settings" {
		t.Errorf("redirected to %v, want the validated return_to /settings", redirect)
	}
	if got := redirect.Query().Get("notice"); got != message {
		t.Errorf("notice = %q, want the Action's own error_description", got)
	}
}

// A blocked email must never reach a session, even on a brand-new Auth0
// identity the admin who blocked them never saw — the whole reason
// Blocklist exists as a local check separate from Auth0's own blocked flag.
func TestSSOCallbackRejectsABlockedEmail(t *testing.T) {
	db, err := source.OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bl, err := blocklist.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if err := bl.Block(context.Background(), "blocked@example.com", "admin", "kicked from a crew"); err != nil {
		t.Fatal(err)
	}

	h := newSSOHarness(t, func(s *api.Server) { s.Blocklist = bl })
	callback := h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "blocked@example.com",
	})

	if callback.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callback.StatusCode)
	}
	loc, err := callback.Location()
	if err != nil {
		t.Fatal(err)
	}
	if loc.Query().Get("notice") == "" {
		t.Errorf("redirect = %v, want a notice explaining the block", loc)
	}

	me := meBody(t, h.get("/api/me"))
	if me["authenticated"] == true {
		t.Fatal("a blocked rider ended up authenticated")
	}
}

// A rider not on the blocklist must sign in exactly as before — the check
// must not false-positive on an ordinary sign-in.
func TestSSOCallbackAllowsAnUnblockedEmail(t *testing.T) {
	db, err := source.OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bl, err := blocklist.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if err := bl.Block(context.Background(), "someone-else@example.com", "admin", ""); err != nil {
		t.Fatal(err)
	}

	h := newSSOHarness(t, func(s *api.Server) { s.Blocklist = bl })
	callback := h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	if callback.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callback.StatusCode)
	}
	me := meBody(t, h.get("/api/me"))
	if me["authenticated"] != true {
		t.Fatalf("me = %v, want authenticated — this email was never blocked", me)
	}
}

// Everything that is not access_denied is a genuinely broken flow (a stale
// state cookie, a misconfigured issuer) rather than something a rider
// reaches by an ordinary sign-in — the plain JSON body is still the right
// landing for those, unchanged from before access_denied got its own path.
func TestSSOCallbackOtherIssuerErrorsStayJSON(t *testing.T) {
	h := newSSOHarness(t)
	loginResp := h.get("/sso/login")
	loc, err := loginResp.Location()
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")

	resp := h.get("/sso/callback?error=server_error&error_description=broken&state=" + state)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body["error"], "server_error") {
		t.Errorf("error body = %v, want it to name server_error", body)
	}
}

func TestSSOCallbackRejectsAMissingStateCookie(t *testing.T) {
	h := newSSOHarness(t)
	// No prior /sso/login on this client: no state cookie exists at all.
	resp := h.get("/sso/callback?code=x&state=anything")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSSOCallbackRejectsAMismatchedState(t *testing.T) {
	h := newSSOHarness(t)
	loginResp := h.get("/sso/login")
	loc, _ := loginResp.Location()
	h.issuer.setClaims(loc.Query().Get("nonce"), nil)

	resp := h.get("/sso/callback?code=x&state=not-the-real-state")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// A token whose nonce does not match what this request actually asked for
// must be rejected even though its signature, issuer, audience and expiry
// are all otherwise fine — this is the one check go-oidc leaves to the
// caller, and it is what stops a token issued for a *different* login
// attempt being replayed into this one.
func TestSSOCallbackRejectsAWrongNonce(t *testing.T) {
	h := newSSOHarness(t)
	loginResp := h.get("/sso/login")
	loc, _ := loginResp.Location()
	state := loc.Query().Get("state")

	h.issuer.setClaims("a-nonce-nobody-asked-for", nil)
	resp := h.get("/sso/callback?code=x&state=" + state)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSSOLogoutEndsTheSessionForReal(t *testing.T) {
	h := newSSOHarness(t)
	h.login([]string{"cyclists"})

	if me := meBody(t, h.get("/api/me")); me["authenticated"] != true {
		t.Fatal("not signed in after login — test setup is broken")
	}

	logoutResp := h.post("/sso/logout")
	if logoutResp.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", logoutResp.StatusCode)
	}
	var body struct {
		RedirectTo string `json:"redirectTo"`
	}
	if err := json.NewDecoder(logoutResp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// No LandingHost configured on this harness — everyone gets the app, so
	// the fallback (this request's own origin) is the right landing place.
	// The end-session endpoint itself lives on the issuer, a separate
	// httptest server from h.base (the app).
	if !strings.HasPrefix(body.RedirectTo, h.issuer.server.URL+"/end-session?") {
		t.Errorf("redirectTo = %q, want the issuer's end-session endpoint", body.RedirectTo)
	}
	if !strings.Contains(body.RedirectTo, "post_logout_redirect_uri="+url.QueryEscape(h.base+"/")) {
		t.Errorf("redirectTo = %q, want it to carry this harness's own origin as the post-logout uri", body.RedirectTo)
	}

	me := meBody(t, h.get("/api/me"))
	if me["authenticated"] != false {
		t.Errorf("me = %v after logout, want anonymous", me)
	}
}

// The app host (app.domestique.dev) is where a logout request is made from,
// but it is not where a now-anonymous visitor belongs — mode: oidc redirects
// anonymous visitors to LandingHost everywhere else (spaHandler), and
// logout must land there too rather than on a host that just bounces them
// straight back. Found live: this used to redirect back to the app host.
func TestSSOLogoutRedirectsToTheLandingHostNotTheAppHost(t *testing.T) {
	issuer := newFakeIssuer(t)

	flow, err := oidcflow.New(context.Background(), oidcflow.Config{
		Issuer: issuer.server.URL, ClientID: "domestique-test", ClientSecret: "test-secret",
		Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := auth.New(auth.Config{
		Mode: auth.ModeOIDC,
		OIDC: auth.OIDCConfig{
			Issuer: issuer.server.URL, ClientID: "domestique-test",
			RedirectURL: "https://app.domestique.test/sso/callback",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{
		Auth:        authenticator,
		OIDC:        flow,
		LandingHost: "domestique.test",
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	// The request itself carries the app host, not the landing one — the
	// bug this test exists for is redirectTo copying r.Host instead of
	// looking at LandingHost.
	req, err := http.NewRequest(http.MethodPost, server.URL+"/sso/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "app.domestique.test"
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body struct {
		RedirectTo string `json:"redirectTo"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	wantURI := "post_logout_redirect_uri=" + url.QueryEscape("https://domestique.test/")
	if !strings.Contains(body.RedirectTo, wantURI) {
		t.Errorf("redirectTo = %q, want it to carry the landing host (%s), not app.domestique.test",
			body.RedirectTo, wantURI)
	}
	if strings.Contains(body.RedirectTo, "app.domestique.test") {
		t.Errorf("redirectTo = %q, still carries the app host it should not", body.RedirectTo)
	}
}

func TestSSOEndpointsAreNotFoundOutsideOIDCMode(t *testing.T) {
	authenticator, err := auth.New(auth.Config{Mode: auth.ModeNone})
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Auth: authenticator}
	server := httptest.NewServer(srv.Handler())
	defer server.Close()

	for _, path := range []string{"/sso/login", "/sso/callback", "/sso/logout"} {
		method := http.MethodGet
		if path == "/sso/logout" {
			method = http.MethodPost
		}
		req, _ := http.NewRequest(method, server.URL+path, nil)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s in mode none: status = %d, want 404", path, resp.StatusCode)
		}
	}
}

// /api/me being reachable while anonymous is new behavior this PR
// introduces (see the comment on authenticate in server.go) — it was
// entirely unexercised before, since mode: proxy never let an anonymous
// request reach this Go server at all. Direct coverage now, across every
// mode, so a future change cannot quietly re-gate it without a test noticing.
func TestMeIsReachableAnonymouslyInEveryMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  auth.Config
	}{
		{"none", auth.Config{Mode: auth.ModeNone}},
		{"proxy", auth.Config{Mode: auth.ModeProxy}},
		{"oidc", validOIDCAuthConfig(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authenticator, err := auth.New(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			srv := &api.Server{Auth: authenticator}
			server := httptest.NewServer(srv.Handler())
			defer server.Close()

			resp, err := http.Get(server.URL + "/api/me")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 even anonymously", resp.StatusCode)
			}
			me := meBody(t, resp)

			// None of these three requests ever presents a session cookie,
			// so "authenticated" must be false in every one of them —
			// including mode none, where the local-admin identity is not a
			// real login and the UI's "no login required" badge depends on
			// this staying false.
			if me["authenticated"] != false {
				t.Errorf("authenticated = %v, want false (nobody signed in)", me["authenticated"])
			}
		})
	}
}

// /api/config being reachable while anonymous is the same fix as /api/me,
// caught later: handleConfig carries no require() of its own and nothing in
// it is secret, but the blanket Authorize check gated it anyway. Under
// mode: proxy this was invisible for the same reason /api/me was. Under
// mode: oidc it broke the anonymous bootstrap for real — useLibrary's
// initial Promise.all fetches config() alongside me(), so config's 401
// failed the whole batch and me never got set: no "Sign in" button, no
// visible explanation, just an empty error state.
func TestConfigIsReachableAnonymouslyInEveryMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  auth.Config
	}{
		{"none", auth.Config{Mode: auth.ModeNone}},
		{"proxy", auth.Config{Mode: auth.ModeProxy}},
		{"oidc", validOIDCAuthConfig(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authenticator, err := auth.New(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			// handleConfig calls Source.Describe(), unlike handleMe — needs a
			// real source, not just an authenticator.
			src, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			srv := &api.Server{Auth: authenticator, Source: src}
			server := httptest.NewServer(srv.Handler())
			defer server.Close()

			resp, err := http.Get(server.URL + "/api/config")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 even anonymously", resp.StatusCode)
			}
		})
	}
}

// The fix for /api/me and /api/config must not have widened to every
// route — an anonymous visitor to anything else under /api/ in mode oidc is
// still refused.
func TestOtherRoutesStayGatedWhenMeDoesNot(t *testing.T) {
	authenticator, err := auth.New(validOIDCAuthConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Auth: authenticator}
	server := httptest.NewServer(srv.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/routes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/routes anonymously: status = %d, want 401", resp.StatusCode)
	}
}

// The whole point of preview_redirect_url: it must apply *only* to a
// request whose own Host exactly matches it — never to the app's normal
// host, and never to some third, unrelated Host a client might send.
// Anything other than an exact match on the one configured preview host
// has to fall back to the ordinary redirect_url, the same as if
// preview_redirect_url were never set at all.
func TestSSOLoginPreviewRedirectOnlyAppliesOnThePreviewHost(t *testing.T) {
	issuer := newFakeIssuer(t)

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}

	flow, err := oidcflow.New(context.Background(), oidcflow.Config{
		Issuer: issuer.server.URL, ClientID: "domestique-test", ClientSecret: "test-secret",
		Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}

	const (
		primaryRedirect = "https://app.domestique.test/sso/callback"
		previewRedirect = "https://preview.domestique.test/sso/callback"
	)
	authenticator, err := auth.New(auth.Config{
		Mode: auth.ModeOIDC,
		OIDC: auth.OIDCConfig{
			Issuer: issuer.server.URL, ClientID: "domestique-test",
			RedirectURL:        primaryRedirect,
			PreviewRedirectURL: previewRedirect,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{Auth: authenticator, OIDC: flow, Box: box}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	redirectURIFor := func(t *testing.T, host string) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/sso/login", nil)
		if err != nil {
			t.Fatal(err)
		}
		// http.Request.Host, not a "Host" header set via req.Header — the
		// latter is stripped and ignored by net/http in favor of this
		// field, which is what the server actually sees as r.Host.
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("Host %q: status = %d, want 302", host, resp.StatusCode)
		}
		loc, err := resp.Location()
		if err != nil {
			t.Fatal(err)
		}
		return loc.Query().Get("redirect_uri")
	}

	cases := []struct {
		name string
		host string
		want string
	}{
		{"the preview host gets the preview redirect", "preview.domestique.test", previewRedirect},
		{"the app's normal host still gets the normal redirect", "app.domestique.test", primaryRedirect},
		{"an unrelated host falls back to the normal redirect, not the preview one", "evil.example.test", primaryRedirect},
		{"a host that merely contains the preview host as a substring is not a match", "notpreview.domestique.test", primaryRedirect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redirectURIFor(t, tc.host); got != tc.want {
				t.Errorf("Host %q: redirect_uri = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

func validOIDCAuthConfig(t *testing.T) auth.Config {
	t.Helper()
	return auth.Config{
		Mode: auth.ModeOIDC,
		OIDC: auth.OIDCConfig{
			Issuer: "https://idp.example.test/", ClientID: "x", RedirectURL: "https://app.example.test/sso/callback",
		},
	}
}
