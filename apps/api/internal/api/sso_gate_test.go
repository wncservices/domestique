package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
)

func sessionRows(t *testing.T, h *ssoHarness) int {
	t.Helper()
	var n int
	if err := h.db.Conn().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// I1: an account the gate does not admit gets nothing: no binding (it would
// reserve a rider name and, via the email, could later join one), no session.
func TestAnUninvitedAccountIsNeitherBoundNorGivenASession(t *testing.T) {
	h := newGatedSSOHarness(t)
	requireGroup(t, h, "domestique-users")
	h.srv.MarkBindingsReady()
	a := h.newActor()
	claims := google("google-oauth2|stranger", "Stranger", "stranger@example.com", true)
	claims["groups"] = []string{} // not in domestique-users, no role group

	resp := a.signIn(claims)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback = %d, want a redirect with a notice", resp.StatusCode)
	}
	loc, _ := resp.Location()
	if loc == nil || !strings.Contains(loc.Query().Get("notice"), "not a member") {
		t.Errorf("redirect = %v, want the not-a-member notice", loc)
	}
	if _, ok, _ := h.ids.Get(context.Background(), h.issuer.server.URL, "google-oauth2|stranger"); ok {
		t.Error("an uninvited account was bound")
	}
	if all, _ := h.ids.List(context.Background()); len(all) != 0 {
		t.Errorf("%d bindings exist, want none", len(all))
	}
	if n := sessionRows(t, h); n != 0 {
		t.Errorf("%d session rows exist, want none", n)
	}
	if got := a.rider(); got != "" {
		t.Errorf("browser is %q", got)
	}
}

// requireGroup swaps the harness authenticator for one with a required group,
// the way a deployment that gates on domestique-users is configured.
func requireGroup(t *testing.T, h *ssoHarness, group string) {
	t.Helper()
	a, err := auth.New(auth.Config{
		Mode:          auth.ModeOIDC,
		RequiredGroup: group,
		Roles:         auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}},
		OIDC: auth.OIDCConfig{
			Issuer: h.issuer.server.URL, ClientID: "domestique-test", RedirectURL: h.base + "/sso/callback",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.UseSessions(h.srv.Sessions)
	h.srv.Auth = a
}

func (a *actor) withCookie(token string) *http.Response {
	a.h.t.Helper()
	req, err := http.NewRequest(http.MethodGet, a.h.base+"/api/me", nil)
	if err != nil {
		a.h.t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.h.t.Fatal(err)
	}
	a.h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// I2: a session is only as good as the binding behind it.
func TestASessionWithoutAMatchingBindingIsRejectedAndDeleted(t *testing.T) {
	h := newSSOHarness(t)
	bindTo(t, h, "google-oauth2|real", "wilant", "wilant@example.com")
	a := h.newActor()

	cases := map[string]auth.Identity{
		// An old-image session: the rider was chosen by a name, the sub is anyone's.
		"foreign sub": {User: "wilant", Sub: "google-oauth2|attacker", Groups: []string{"cyclists"}},
		"no sub":      {User: "wilant", Groups: []string{"cyclists"}},
		"wrong rider": {User: "someone-else", Sub: "google-oauth2|real", Groups: []string{"cyclists"}},
	}
	for name, id := range cases {
		tok, _, err := h.srv.Sessions.Create(id, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		me := meBody(t, a.withCookie(tok))
		if me["authenticated"] == true || me["user"] == "wilant" {
			t.Errorf("%s: the session was accepted: %v", name, me)
		}
		if _, ok := h.srv.Sessions.Lookup(tok); ok {
			t.Errorf("%s: the rejected session was not deleted", name)
		}
	}

	// And the right one still works.
	tok, _, _ := h.srv.Sessions.Create(auth.Identity{User: "wilant", Sub: "google-oauth2|real", Groups: []string{"cyclists"}}, time.Hour)
	if me := meBody(t, a.withCookie(tok)); me["user"] != "wilant" {
		t.Errorf("a session matching its binding was refused: %v", me)
	}
}

func TestASessionGoesStaleWhenItsBindingMoves(t *testing.T) {
	h := newSSOHarness(t)
	a := h.newActor()
	a.signIn(google("google-oauth2|1", "Robin", "robin@example.com", true))
	if got := a.rider(); got != "robin" {
		t.Fatalf("rider = %q", got)
	}
	if err := h.ids.Move(context.Background(), h.issuer.server.URL, "google-oauth2|1", "pat"); err != nil {
		t.Fatal(err)
	}
	if got := a.rider(); got != "" {
		t.Errorf("after the binding moved the old session answers as %q", got)
	}
	if n := sessionRows(t, h); n != 0 {
		t.Errorf("%d session rows left, want the stale one deleted", n)
	}
	a.signIn(google("google-oauth2|1", "Robin", "robin@example.com", true))
	if got := a.rider(); got != "pat" {
		t.Errorf("signing in again gives %q, want pat", got)
	}
}
