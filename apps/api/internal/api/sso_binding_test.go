package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/workout"

	"github.com/wncservices/domestique/apps/api/internal/api"
)

const bindingGPX = `<?xml version="1.0"?><gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>` +
	`<trkpt lat="50.0" lon="3.0"/><trkpt lat="50.001" lon="3.001"/></trkseg></trk></gpx>`

// setRawClaims is a token with exactly the claims given (plus the ones every
// token has), for tests about which claims are trusted.
func (f *fakeIssuer) setRawClaims(nonce string, claims map[string]any) {
	now := time.Now()
	f.claims = map[string]any{
		"iss": f.server.URL, "aud": "domestique-test",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": nonce,
	}
	for k, v := range claims {
		f.claims[k] = v
	}
}

// actor is one person with their own browser: its own cookie jar against the
// same server and the same issuer.
type actor struct {
	h *ssoHarness
	c *http.Client
}

func (h *ssoHarness) newActor() *actor {
	jar, err := cookiejar.New(nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return &actor{h: h, c: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (a *actor) get(path string) *http.Response {
	a.h.t.Helper()
	resp, err := a.c.Get(a.h.base + path)
	if err != nil {
		a.h.t.Fatal(err)
	}
	a.h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// signIn is a whole /sso/login -> issuer -> /sso/callback round trip with the
// given claims, returning the callback's response.
func (a *actor) signIn(claims map[string]any) *http.Response {
	a.h.t.Helper()
	login := a.get("/sso/login")
	if login.StatusCode != http.StatusFound {
		a.h.t.Fatalf("GET /sso/login = %d, want 302", login.StatusCode)
	}
	loc, err := login.Location()
	if err != nil {
		a.h.t.Fatal(err)
	}
	a.h.issuer.setRawClaims(loc.Query().Get("nonce"), claims)
	return a.get("/sso/callback?code=any-code&state=" + loc.Query().Get("state"))
}

// rider is who the server says this browser is; empty when nobody.
func (a *actor) rider() string {
	a.h.t.Helper()
	me := meBody(a.h.t, a.get("/api/me"))
	user, _ := me["user"].(string)
	return user
}

func (a *actor) json(path string, into any) int {
	a.h.t.Helper()
	resp := a.get(path)
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			a.h.t.Fatalf("decoding %s: %v", path, err)
		}
	}
	return resp.StatusCode
}

// google is the claims of a Google sign-in: a sub, a display name the holder
// chose, and an address the issuer vouches for or not.
func google(sub, name, email string, verified bool) map[string]any {
	return map[string]any{
		"sub": sub, "name": name, "email": email, "email_verified": verified,
		"groups": []string{"cyclists"},
	}
}

// logs captures what the server logs, so a test can assert on what is, and is
// not, in it.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func capturedLogs(srv *api.Server) *logs {
	l := &logs{}
	srv.Log = slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l
}

// wireData opens the stores a rider's data lives in, on the harness database.
func wireData(t *testing.T, h *ssoHarness) (*accounts.Store, *workout.DB) {
	t.Helper()
	h.srv.Source = h.db
	acc, err := accounts.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Accounts = acc
	tr, err := workout.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Store = st
	h.srv.Training = tr
	return acc, tr
}

// seedWilant gives the rider "wilant" everything an impersonator would want:
// a route, a linked Garmin account and a training goal, and binds the real
// Google account to them.
func seedWilant(t *testing.T, h *ssoHarness) {
	t.Helper()
	ctx := context.Background()
	acc, tr := wireData(t, h)
	if _, err := h.db.Create(ctx, source.CreateRequest{Name: "Wilants private loop", GPX: []byte(bindingGPX), UploadedBy: "wilant"}); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.Link(ctx, model.ProviderGarmin, "wilant", "Wilants Edge"); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Wilants fondo", Sport: model.SportCycling}); err != nil {
		t.Fatal(err)
	}
	bindTo(t, h, "google-oauth2|real-wilant", "wilant", "wilant@example.com")
}

func bindTo(t *testing.T, h *ssoHarness, sub, rider, email string) {
	t.Helper()
	if _, err := h.ids.Bind(context.Background(), h.issuer.server.URL, sub, rider, h.ids.EmailMAC(email)); err != nil {
		t.Fatal(err)
	}
}

// exposed is how many routes, linked accounts and training goals a signed-in
// browser can see: the three kinds of thing H1 handed over.
func exposed(t *testing.T, a *actor) (routes, accountList, goals int) {
	t.Helper()
	var lib struct {
		Routes []json.RawMessage `json:"routes"`
	}
	if code := a.json("/api/routes", &lib); code != http.StatusOK {
		t.Fatalf("GET /api/routes = %d", code)
	}
	var accs []json.RawMessage
	if code := a.json("/api/accounts", &accs); code != http.StatusOK {
		t.Fatalf("GET /api/accounts = %d", code)
	}
	var gs []json.RawMessage
	if code := a.json("/api/training/goals", &gs); code != http.StatusOK {
		t.Fatalf("GET /api/training/goals = %d", code)
	}
	return len(lib.Routes), len(accs), len(gs)
}

// H1, as an attacker would run it. The attacker is invited, renames their own
// Google account to the name of a rider that exists, and signs in. Before
// bindings the name claim chose the rider, so they became that rider and
// inherited the routes, the Garmin account and the training data. Now an
// account has to be bound, and a name that belongs to somebody is taken.
func TestH1ARenamedGoogleAccountDoesNotBecomeAnotherRider(t *testing.T) {
	h := newSSOHarness(t)
	seedWilant(t, h)

	// The real rider can see everything; this is what is at stake.
	wilant := h.newActor()
	if resp := wilant.signIn(google("google-oauth2|real-wilant", "Wilant", "wilant@example.com", true)); resp.StatusCode != http.StatusFound {
		t.Fatalf("the real rider's callback = %d, want 302", resp.StatusCode)
	}
	if got := wilant.rider(); got != "wilant" {
		t.Fatalf("the real rider is %q, want wilant", got)
	}
	if r, a, g := exposed(t, wilant); r != 1 || a != 1 || g != 1 {
		t.Fatalf("the real rider sees %d routes, %d accounts, %d goals; want 1 of each", r, a, g)
	}

	// The attacker: different account, different address, the victim's name.
	attacker := h.newActor()
	if resp := attacker.signIn(google("google-oauth2|attacker", "Wilant", "attacker@example.com", true)); resp.StatusCode != http.StatusFound {
		t.Fatalf("the attacker's callback = %d, want 302", resp.StatusCode)
	}
	if got := attacker.rider(); got == "wilant" {
		t.Fatal("H1: an account renamed to Wilant signed in as wilant")
	}
	if got := attacker.rider(); got != "wilant-2" {
		t.Errorf("the attacker is %q, want the suffixed wilant-2", got)
	}
	if r, a, g := exposed(t, attacker); r != 0 || a != 0 || g != 0 {
		t.Errorf("the attacker sees %d routes, %d accounts, %d goals of the victim; want none", r, a, g)
	}
}

// The same attack when the real rider has no binding of their own (one the
// bootstrap could not bind). The name is still taken: the rider has data in
// tables other than the bindings, and training data alone makes somebody.
func TestH1AnUnboundRiderWithOnlyTrainingDataIsStillTaken(t *testing.T) {
	h := newSSOHarness(t)
	_, tr := wireData(t, h)
	if _, err := tr.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "sam", Name: "Sams goal", Sport: model.SportCycling}); err != nil {
		t.Fatal(err)
	}

	a := h.newActor()
	a.signIn(google("google-oauth2|someone", "Sam", "someone@example.com", true))
	if got := a.rider(); got != "sam-2" {
		t.Errorf("rider = %q, want sam-2: a rider with only training data is somebody", got)
	}
	if _, _, g := exposed(t, a); g != 0 {
		t.Errorf("%d of sam's goals are visible", g)
	}
}

func TestABoundAccountKeepsItsRiderWhenItsNameChanges(t *testing.T) {
	h := newSSOHarness(t)
	first := h.newActor()
	first.signIn(google("google-oauth2|1", "Robin", "robin@example.com", true))
	if got := first.rider(); got != "robin" {
		t.Fatalf("first sign-in is %q, want robin", got)
	}

	renamed := h.newActor()
	renamed.signIn(google("google-oauth2|1", "Totally Someone Else", "robin@example.com", true))
	if got := renamed.rider(); got != "robin" {
		t.Errorf("after the name claim changed the rider is %q, want robin", got)
	}
	// Even a rename to another bound rider's name changes nothing.
	bindTo(t, h, "google-oauth2|other", "wilant", "wilant@example.com")
	again := h.newActor()
	again.signIn(google("google-oauth2|1", "Wilant", "robin@example.com", true))
	if got := again.rider(); got != "robin" {
		t.Errorf("renamed to wilant, the rider is %q, want robin", got)
	}
}

func TestASecondSignInMethodWithTheSameVerifiedEmailJoinsTheRider(t *testing.T) {
	h := newSSOHarness(t)
	l := capturedLogs(h.srv)
	bindTo(t, h, "google-oauth2|1", "wilant", "Wilant@Example.com")

	a := h.newActor()
	// A password account for the same person: a different sub and a different name.
	a.signIn(google("auth0|2", "w. nackaerts", "wilant@example.com", true))
	if got := a.rider(); got != "wilant" {
		t.Fatalf("rider = %q, want the second account to join wilant", got)
	}
	if b, ok, _ := h.ids.Get(context.Background(), h.issuer.server.URL, "auth0|2"); !ok || b.Rider != "wilant" {
		t.Errorf("the joined account's binding = %+v, %v", b, ok)
	}
	out := l.String()
	if !strings.Contains(out, "identity bound by verified email") || !strings.Contains(out, "level=INFO") {
		t.Errorf("no Info line for the join in:\n%s", out)
	}
}

func TestAnUnverifiedEmailDoesNotJoin(t *testing.T) {
	h := newSSOHarness(t)
	bindTo(t, h, "google-oauth2|1", "wilant", "wilant@example.com")

	a := h.newActor()
	a.signIn(google("auth0|2", "Wilant", "wilant@example.com", false))
	if got := a.rider(); got != "wilant-2" {
		t.Errorf("rider = %q, want an unverified address not to join (wilant-2)", got)
	}
}

func TestAVerifiedEmailSharedByTwoRidersIsAmbiguousAndJoinsNothing(t *testing.T) {
	h := newSSOHarness(t)
	l := capturedLogs(h.srv)
	bindTo(t, h, "google-oauth2|1", "wilant", "shared@example.com")
	bindTo(t, h, "google-oauth2|2", "sam", "shared@example.com")

	a := h.newActor()
	a.signIn(google("auth0|3", "Newcomer", "shared@example.com", true))
	if got := a.rider(); got != "newcomer" {
		t.Errorf("rider = %q, want it to fall to its own name, joining neither", got)
	}
	out := l.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "ambiguous") {
		t.Errorf("no Warn for the ambiguous match in:\n%s", out)
	}
}

func TestAFirstSignInTakesAFreeNameAndSuffixesATakenOne(t *testing.T) {
	h := newSSOHarness(t)
	l := capturedLogs(h.srv)

	for i, want := range []string{"newcomer", "newcomer-2", "newcomer-3"} {
		sub := "google-oauth2|n" + string(rune('a'+i))
		a := h.newActor()
		a.signIn(google(sub, "Newcomer", string(rune('a'+i))+"@example.com", true))
		if got := a.rider(); got != want {
			t.Errorf("sign-in %d is %q, want %q", i+1, got, want)
		}
	}
	out := l.String()
	if !strings.Contains(out, "sign-in name already belongs to another rider") || !strings.Contains(out, "level=WARN") {
		t.Errorf("no Warn for the suffix in:\n%s", out)
	}
}

func TestAPreferredUsernameThatIsNotARiderNameFallsThrough(t *testing.T) {
	h := newSSOHarness(t)
	claims := google("auth0|abc", "Fallback Name", "pu@example.com", true)
	claims["nickname"] = "pu.nick"
	claims["preferred_username"] = "has spaces and <markup>"

	a := h.newActor()
	a.signIn(claims)
	if got := a.rider(); got != "pu.nick" {
		t.Errorf("rider = %q, want the next claim that is a legal rider name", got)
	}
}

func TestNoLogLineCarriesAnEmailOrASub(t *testing.T) {
	h := newSSOHarness(t)
	l := capturedLogs(h.srv)
	bindTo(t, h, "google-oauth2|1", "wilant", "wilant@example.com")

	h.newActor().signIn(google("auth0|2", "w", "wilant@example.com", true))                  // joins by email
	h.newActor().signIn(google("google-oauth2|zz", "Wilant", "zz-secret@example.com", true)) // suffixed
	h.newActor().signIn(google("google-oauth2|yy", "Wilant", "yy-secret@example.com", true)) // suffixed again

	out := l.String()
	for _, secret := range []string{"@example.com", "zz-secret", "yy-secret", "google-oauth2|zz", "google-oauth2|yy", "auth0|2"} {
		if strings.Contains(out, secret) {
			t.Errorf("the log contains %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "wilant") {
		t.Fatalf("expected the rider name in the log, got:\n%s", out)
	}
}

// Until the startup bootstrap has run, an account with no binding is refused,
// not resolved by name; one that is bound signs in as usual.
func TestSignInRefusesAnUnboundAccountUntilTheBootstrapHasRun(t *testing.T) {
	h := newGatedSSOHarness(t)
	bindTo(t, h, "google-oauth2|bound", "wilant", "wilant@example.com")

	unbound := h.newActor()
	resp := unbound.signIn(google("google-oauth2|new", "Wilant", "new@example.com", true))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unbound callback = %d, want 503", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After on the refusal")
	}
	if got := unbound.rider(); got != "" {
		t.Errorf("a refused sign-in left the browser as %q", got)
	}
	if _, ok, _ := h.ids.Get(context.Background(), h.issuer.server.URL, "google-oauth2|new"); ok {
		t.Error("a refused sign-in was bound")
	}

	bound := h.newActor()
	if resp := bound.signIn(google("google-oauth2|bound", "Anything", "wilant@example.com", true)); resp.StatusCode != http.StatusFound {
		t.Fatalf("bound callback = %d, want 302", resp.StatusCode)
	}
	if got := bound.rider(); got != "wilant" {
		t.Errorf("bound rider = %q", got)
	}

	h.srv.MarkBindingsReady()
	if resp := unbound.signIn(google("google-oauth2|new", "Wilant", "new@example.com", true)); resp.StatusCode != http.StatusFound {
		t.Errorf("after the bootstrap the callback = %d, want 302", resp.StatusCode)
	}
}

func TestSignInWithoutAnIdentityStoreIsRefused(t *testing.T) {
	h := newSSOHarness(t)
	h.srv.Identities = nil
	a := h.newActor()
	resp := a.signIn(google("google-oauth2|1", "Wilant", "w@example.com", true))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("callback = %d, want 500: with nothing to bind to, a name must not pick the rider", resp.StatusCode)
	}
	if got := a.rider(); got != "" {
		t.Errorf("browser is %q", got)
	}
}

func TestEachSignInIsStampedWithTheServerClock(t *testing.T) {
	h := newSSOHarness(t)
	h.newActor().signIn(google("google-oauth2|1", "Pat", "pat@example.com", true))
	b, ok, err := h.ids.Get(context.Background(), h.issuer.server.URL, "google-oauth2|1")
	if err != nil || !ok {
		t.Fatalf("binding: %v %v", ok, err)
	}
	if !b.CreatedAt.Equal(ssoNow) || !b.LastSeenAt.Equal(ssoNow) {
		t.Errorf("times = %v / %v, want the fixed clock %v", b.CreatedAt, b.LastSeenAt, ssoNow)
	}
}
