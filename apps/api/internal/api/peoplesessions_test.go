package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
)

// sessionHarness is an OIDC deployment with an admin signed in (wilant, through
// the harness's cookie jar) and the People connector wired, plus a way to mint
// other riders' sessions and check what a bare cookie resolves to.
type sessionHarness struct {
	*ssoHarness
	srv    *api.Server
	people *fakePeople
}

func newSessionHarness(t *testing.T) *sessionHarness {
	t.Helper()
	people := &fakePeople{}
	var srv *api.Server
	h := newSSOHarness(t, func(s *api.Server) { s.People = people; srv = s })
	// The People page grants roles relative to the gate group, so the
	// deployment needs one. Swapped in before anything has signed in.
	gated, err := auth.New(auth.Config{
		Mode: auth.ModeOIDC, RequiredGroup: "gate",
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}},
		OIDC: auth.OIDCConfig{
			Issuer: h.issuer.server.URL, ClientID: "domestique-test", RedirectURL: h.base + "/sso/callback",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gated.UseSessions(srv.Sessions)
	srv.Auth = gated
	h.login([]string{"gate", "admins"})
	return &sessionHarness{ssoHarness: h, srv: srv, people: people}
}

// cookieFor creates a live session for a rider, as if they had signed in on
// some device, and returns the cookie value.
func (h *sessionHarness) cookieFor(user, sub string, groups ...string) string {
	h.t.Helper()
	tok, _, err := h.srv.Sessions.Create(auth.Identity{User: user, Sub: sub, Groups: append([]string{"gate"}, groups...)}, time.Hour)
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// authenticated is whether a request carrying only this cookie is signed in.
func (h *sessionHarness) authenticated(cookie string) bool {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.base+"/api/me", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	return meBody(h.t, resp)["authenticated"] == true
}

func (h *sessionHarness) deletePerson(path string) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodDelete, h.base+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("DELETE %s = %d, want 200", path, resp.StatusCode)
	}
}

// The bug: removing a rider deleted their data and their Auth0 identity but
// left their server-side sessions, so a cookie they already held kept
// authenticating until it expired a month later.
func TestRemovingARiderEndsTheirSessionsEverywhere(t *testing.T) {
	h := newSessionHarness(t)
	phone := h.cookieFor("gone", "auth0|gone", "cyclists")
	laptop := h.cookieFor("gone", "auth0|gone", "cyclists")
	friend := h.cookieFor("friend", "auth0|friend", "cyclists")
	for name, c := range map[string]string{"phone": phone, "laptop": laptop, "friend": friend} {
		if !h.authenticated(c) {
			t.Fatalf("%s is not signed in before the removal: test setup is broken", name)
		}
	}

	h.deletePerson("/api/people/auth0%7Cgone?rider=gone")

	if h.authenticated(phone) || h.authenticated(laptop) {
		t.Error("a removed rider's cookie still authenticates")
	}
	if !h.authenticated(friend) {
		t.Error("removing one rider ended another rider's session")
	}
}

// The ?rider= is the UI's guess and may be absent; the identity being deleted
// is known for certain, so its sessions end regardless.
func TestRemovingAnIdentityEndsItsSessionsEvenWithoutARiderName(t *testing.T) {
	h := newSessionHarness(t)
	gone := h.cookieFor("gone", "auth0|gone", "cyclists")
	friend := h.cookieFor("friend", "auth0|friend", "cyclists")

	h.deletePerson("/api/people/auth0%7Cgone")

	if h.authenticated(gone) {
		t.Error("the deleted identity's cookie still authenticates")
	}
	if !h.authenticated(friend) {
		t.Error("another identity's session was ended")
	}
}

// Role is recomputed from the groups the session stored at sign-in, so without
// this a demoted admin stays an admin until the cookie expires.
func TestChangingARoleEndsThatPersonsSessions(t *testing.T) {
	h := newSessionHarness(t)
	grace := h.cookieFor("grace", "auth0|grace", "admins")
	friend := h.cookieFor("friend", "auth0|friend", "cyclists")

	req, err := http.NewRequest(http.MethodPut, h.base+"/api/people/auth0%7Cgrace/role", strings.NewReader(`{"role":"viewer"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set role = %d, want 200", resp.StatusCode)
	}

	if h.authenticated(grace) {
		t.Error("a person whose role changed kept the session that carries the old role")
	}
	if !h.authenticated(friend) {
		t.Error("changing one person's role ended someone else's session")
	}
}

// A failed Auth0 change must not log anyone out: nothing changed.
func TestAFailedRoleChangeKeepsTheSession(t *testing.T) {
	h := newSessionHarness(t)
	h.people.rolesErr = assertErr
	grace := h.cookieFor("grace", "auth0|grace", "admins")

	req, _ := http.NewRequest(http.MethodPut, h.base+"/api/people/auth0%7Cgrace/role", strings.NewReader(`{"role":"viewer"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("role change unexpectedly succeeded")
	}
	if !h.authenticated(grace) {
		t.Error("a failed role change ended the session")
	}
}
