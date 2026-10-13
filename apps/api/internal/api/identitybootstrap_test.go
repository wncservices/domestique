package api_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
	"github.com/wncservices/domestique/apps/api/internal/identities"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"

	"github.com/wncservices/domestique/apps/api/internal/api"
)

const bootstrapIssuer = "https://tenant.example/"

// bootstrapEnv is a server with an identity store and a fake Management API,
// and nothing else: the bootstrap reads people and writes bindings.
type bootstrapEnv struct {
	srv    *api.Server
	ids    *identities.Store
	people *fakePeople
	log    *logs
	db     *source.DB
}

func newBootstrapEnv(t *testing.T, mode auth.Mode, people ...auth0mgmt.Person) *bootstrapEnv {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "bootstrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := identities.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	ids.Clock = func() time.Time { return ssoNow }

	cfg := auth.Config{
		Mode:        mode,
		Roles:       auth.RoleMapping{Admin: []string{"domestique-admins"}, Rider: []string{"cyclists"}},
		DefaultRole: "viewer",
	}
	if mode != auth.ModeNone {
		cfg.RequiredGroup = "domestique-users"
	}
	if mode == auth.ModeOIDC {
		cfg.OIDC = auth.OIDCConfig{Issuer: bootstrapIssuer, ClientID: "domestique-test", RedirectURL: "https://app.example/sso/callback"}
	}
	authenticator, err := auth.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakePeople{people: people}
	srv := &api.Server{Auth: authenticator, Identities: ids, People: fake, Clock: func() time.Time { return ssoNow }}
	l := capturedLogs(srv)
	return &bootstrapEnv{srv: srv, ids: ids, people: fake, log: l, db: db}
}

func person(sub, name, email string, verified bool) auth0mgmt.Person {
	return auth0mgmt.Person{UserID: sub, Name: name, Email: email, EmailVerified: verified, ProfileKnown: true, Roles: []string{"cyclists"}}
}

func (e *bootstrapEnv) bound(t *testing.T) map[string]string {
	t.Helper()
	all, err := e.ids.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, b := range all {
		if b.Issuer != bootstrapIssuer {
			t.Errorf("binding %+v is under issuer %q, want the configured %q", b, b.Issuer, bootstrapIssuer)
		}
		out[b.Sub] = b.Rider
	}
	return out
}

func TestBootstrapBindsAPersonReachedThroughGoogleAndAPasswordToOneRider(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC,
		person("google-oauth2|1", "Wilant", "Wilant@Example.com", true),
		person("auth0|2", "wilant", "wilant@example.com", true),
		person("google-oauth2|3", "Sam", "sam@example.com", true),
	)
	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := e.bound(t)
	want := map[string]string{"google-oauth2|1": "wilant", "auth0|2": "wilant", "google-oauth2|3": "sam"}
	if len(got) != len(want) {
		t.Fatalf("bindings = %v, want %v", got, want)
	}
	for sub, rider := range want {
		if got[sub] != rider {
			t.Errorf("%s is bound to %q, want %q", sub, got[sub], rider)
		}
	}
	if res.Bound != 3 || res.Conflicts != 0 {
		t.Errorf("result = %+v, want 3 bound and no conflicts", res)
	}
	// The address is stored as a MAC that the sign-in's own join will match.
	riders, _ := e.ids.RidersByEmail(context.Background(), bootstrapIssuer, e.ids.EmailMAC("wilant@example.com"))
	if len(riders) != 1 || riders[0] != "wilant" {
		t.Errorf("the verified email matches %v, want [wilant]", riders)
	}
}

func TestBootstrapBindsNobodyWhenIdentitiesClaimingARiderDisagree(t *testing.T) {
	// Someone renamed their Google account to "Wilant" before this deploy.
	e := newBootstrapEnv(t, auth.ModeOIDC,
		person("google-oauth2|real", "Wilant", "wilant@example.com", true),
		person("google-oauth2|impostor", "Wilant", "impostor-secret@example.com", true),
		person("google-oauth2|sam", "Sam", "sam@example.com", true),
	)
	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := e.bound(t)
	if _, bound := got["google-oauth2|real"]; bound {
		t.Error("the real rider was bound although another identity claims the name")
	}
	if _, bound := got["google-oauth2|impostor"]; bound {
		t.Error("the impostor was bound")
	}
	if got["google-oauth2|sam"] != "sam" {
		t.Errorf("an unrelated rider was not bound: %v", got)
	}
	if res.Conflicts != 1 {
		t.Errorf("conflicts = %d, want 1", res.Conflicts)
	}

	out := e.log.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "identity bootstrap conflict") {
		t.Errorf("no Error for the conflict in:\n%s", out)
	}
	if !strings.Contains(out, "rider=wilant") || !strings.Contains(out, "identities=2") {
		t.Errorf("the conflict line should name the rider and count, got:\n%s", out)
	}
	for _, leak := range []string{"@example.com", "impostor-secret", "google-oauth2|"} {
		if strings.Contains(out, leak) {
			t.Errorf("the log contains %q:\n%s", leak, out)
		}
	}
}

func TestBootstrapBindsNobodyForSeveralClaimantsWhenOneIsUnverified(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC,
		person("auth0|2", "Robin", "robin@example.com", true),
		person("google-oauth2|3", "Robin", "", true), // no address at all
	)
	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.bound(t); len(got) != 0 {
		t.Errorf("bindings = %v, want none: with two claimants an address nobody has verified cannot tell them apart", got)
	}
	if res.Unverified != 1 {
		t.Errorf("unverified riders = %d, want 1 (robin)", res.Unverified)
	}
	out := e.log.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "identity bootstrap skipped") {
		t.Errorf("no Warn for the skipped rider in:\n%s", out)
	}
	if strings.Contains(out, "@example.com") || strings.Contains(out, "auth0|") {
		t.Errorf("the log contains an address or a sub:\n%s", out)
	}
}

func TestBootstrapLeavesARiderThatAlreadyHasABindingAlone(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC,
		person("google-oauth2|real", "Wilant", "wilant@example.com", true),
		person("google-oauth2|other", "Wilant", "other@example.com", true),
	)
	// An admin resolved the conflict by hand on an earlier start.
	if _, err := e.ids.Bind(context.Background(), bootstrapIssuer, "google-oauth2|real", "wilant", ""); err != nil {
		t.Fatal(err)
	}
	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Bound != 0 || res.Conflicts != 0 {
		t.Errorf("result = %+v, want nothing to do for a rider that is bound", res)
	}
	if got := e.bound(t); len(got) != 1 {
		t.Errorf("bindings = %v, want only the admin's", got)
	}
	if strings.Contains(e.log.String(), "level=ERROR") {
		t.Errorf("a resolved conflict was reported again:\n%s", e.log.String())
	}
}

func TestBootstrapIsIdempotent(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC,
		person("google-oauth2|1", "Wilant", "wilant@example.com", true),
		person("auth0|2", "Wilant", "wilant@example.com", true),
	)
	first, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := e.bound(t)
	second, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Bound != 2 || second.Bound != 0 {
		t.Errorf("bound %d then %d, want 2 then 0", first.Bound, second.Bound)
	}
	after := e.bound(t)
	if len(before) != len(after) {
		t.Errorf("bindings went from %v to %v", before, after)
	}
}

// The ordering decision, end to end: until the bootstrap has run an unbound
// account is refused; after it, the real rider keeps their data and an
// impostor who renamed themselves earlier does not get it.
func TestSignInWaitsForTheBootstrapAndThenTheRealRiderIsBound(t *testing.T) {
	h := newGatedSSOHarness(t)
	fake := &fakePeople{people: []auth0mgmt.Person{
		person("google-oauth2|real", "Wilant", "wilant@example.com", true),
	}}
	h.srv.People = fake

	real := h.newActor()
	groups := []string{"cyclists", "domestique-users"}
	claims := google("google-oauth2|real", "Wilant", "wilant@example.com", true)
	claims["groups"] = groups
	if resp := real.signIn(claims); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("before the bootstrap the callback = %d, want 503", resp.StatusCode)
	}

	// A failing Management API does not open sign-in.
	fake.listErr = errors.New("auth0 is down")
	if _, err := h.srv.BootstrapIdentities(context.Background()); err == nil {
		t.Fatal("a failed listing reported success")
	}
	if resp := real.signIn(claims); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after a failed bootstrap the callback = %d, want 503", resp.StatusCode)
	}

	fake.listErr = nil
	if _, err := h.srv.BootstrapIdentities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resp := real.signIn(claims); resp.StatusCode != http.StatusFound {
		t.Fatalf("after the bootstrap the callback = %d, want 302", resp.StatusCode)
	}
	if got := real.rider(); got != "wilant" {
		t.Errorf("rider = %q, want wilant", got)
	}

	impostor := h.newActor()
	iclaims := google("google-oauth2|impostor", "Wilant", "impostor@example.com", true)
	iclaims["groups"] = groups
	if resp := impostor.signIn(iclaims); resp.StatusCode != http.StatusFound {
		t.Fatalf("impostor callback = %d, want 302", resp.StatusCode)
	}
	if got := impostor.rider(); got != "wilant-2" {
		t.Errorf("the impostor is %q, want wilant-2", got)
	}
}

// A deployment that can only start the bootstrap's first attempt late (Auth0
// slow or briefly down) must not stay refused for good.
func TestStartIdentityBootstrapRetriesUntilItSucceeds(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC, person("auth0|1", "Pat", "pat@example.com", true))
	flaky := &flakyPeople{fakePeople: e.people, failures: 2}
	e.srv.People = flaky

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.srv.StartIdentityBootstrap(ctx, func(int) time.Duration { return time.Millisecond })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.bound(t)) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := e.bound(t); got["auth0|1"] != "pat" {
		t.Fatalf("bindings = %v after the retries, want auth0|1 bound to pat", got)
	}
	if n := flaky.calls.Load(); n < 3 {
		t.Errorf("ListPeople was called %d times, want at least 3 (two failures, then a success)", n)
	}
	if !strings.Contains(e.log.String(), "level=ERROR") {
		t.Errorf("a failed bootstrap should log an Error:\n%s", e.log.String())
	}
}

type flakyPeople struct {
	*fakePeople
	failures int
	calls    atomic.Int32
}

func (f *flakyPeople) ListPeople(ctx context.Context, gate string, roles ...string) ([]auth0mgmt.Person, error) {
	if int(f.calls.Add(1)) <= f.failures {
		return nil, errors.New("auth0 is down")
	}
	return f.fakePeople.ListPeople(ctx, gate, roles...)
}

// Under mode proxy and mode none there are no bindings and nothing to bind.
func TestStartIdentityBootstrapDoesNothingOutsideModeOIDC(t *testing.T) {
	for _, mode := range []auth.Mode{auth.ModeProxy, auth.ModeNone} {
		e := newBootstrapEnv(t, mode, person("auth0|1", "Pat", "pat@example.com", true))
		flaky := &flakyPeople{fakePeople: e.people}
		e.srv.People = flaky
		e.srv.StartIdentityBootstrap(context.Background(), func(int) time.Duration { return time.Millisecond })
		time.Sleep(20 * time.Millisecond)
		if n := flaky.calls.Load(); n != 0 {
			t.Errorf("mode %s: ListPeople was called %d times, want 0", mode, n)
		}
		if got := e.bound(t); len(got) != 0 {
			t.Errorf("mode %s: bindings = %v", mode, got)
		}
	}
}
