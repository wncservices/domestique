package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"

	"github.com/wncservices/domestique/apps/api/internal/api"
)

// do sends a request with this browser's cookies and a JSON body.
func (a *actor) do(method, path, body string) *http.Response {
	a.h.t.Helper()
	req, err := http.NewRequest(method, a.h.base+path, strings.NewReader(body))
	if err != nil {
		a.h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.c.Do(req)
	if err != nil {
		a.h.t.Fatal(err)
	}
	a.h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func peoplePath(sub, tail string) string { return "/api/people/" + url.PathEscape(sub) + tail }

// inGroups is a sign-in with the given app roles, inside the gate.
func inGroups(claims map[string]any, groups ...string) map[string]any {
	claims["groups"] = append(groups, "domestique-users")
	return claims
}

// bindingHarness is a People-page server with an admin signed in.
type bindingHarness struct {
	*ssoHarness
	people *fakePeople
	admin  *actor
	logs   *logs
}

func newBindingHarness(t *testing.T, people ...auth0mgmt.Person) *bindingHarness {
	return newBindingHarnessAs(t, "Boss", people...)
}

// newBindingHarnessAs is newBindingHarness with the admin signing in under the
// given display name, which is what the rider is derived from.
func newBindingHarnessAs(t *testing.T, adminName string, people ...auth0mgmt.Person) *bindingHarness {
	t.Helper()
	fake := &fakePeople{people: people}
	h := newGatedSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.srv.MarkBindingsReady()
	l := capturedLogs(h.srv)
	admin := h.newActor()
	if resp := admin.signIn(inGroups(google("auth0|boss", adminName, "boss@example.com", true), "admins")); resp.StatusCode != http.StatusFound {
		t.Fatalf("admin sign-in = %d", resp.StatusCode)
	}
	return &bindingHarness{ssoHarness: h, people: fake, admin: admin, logs: l}
}

func (h *bindingHarness) binding(sub string) (string, bool) {
	h.t.Helper()
	b, ok, err := h.ids.Get(context.Background(), h.issuer.server.URL, sub)
	if err != nil {
		h.t.Fatal(err)
	}
	return b.Rider, ok
}

func TestPeopleListShowsWhichRiderEachAccountIs(t *testing.T) {
	h := newBindingHarness(t,
		person("auth0|wilant", "Wilant", "wilant@example.com", true),
		person("google-oauth2|sam", "Sam", "sam@example.com", true),
		person("google-oauth2|robin1", "Robin", "robin-one@example.com", true),
		person("google-oauth2|robin2", "Robin", "robin-two@example.com", true),
	)
	bindTo(t, h.ssoHarness, "auth0|wilant", "wilant-2", "wilant@example.com")

	var out []struct {
		ID            string `json:"id"`
		BoundRider    string `json:"boundRider"`
		BindingStatus string `json:"bindingStatus"`
	}
	if code := h.admin.json("/api/people", &out); code != http.StatusOK {
		t.Fatalf("GET /api/people = %d", code)
	}
	got := map[string][2]string{}
	for _, p := range out {
		got[p.ID] = [2]string{p.BindingStatus, p.BoundRider}
	}
	want := map[string][2]string{
		"auth0|wilant":         {"bound", "wilant-2"}, // what the account is bound to, not what its name suggests
		"google-oauth2|sam":    {"unbound", ""},
		"google-oauth2|robin1": {"conflict", ""},
		"google-oauth2|robin2": {"conflict", ""},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: status/bound = %v, want %v", id, got[id], w)
		}
	}
}

func TestAnAdminMovesABindingAndTheAccountsSessionsEnd(t *testing.T) {
	h := newBindingHarness(t, person("google-oauth2|moved", "Moved", "moved@example.com", true))

	moved := h.newActor()
	moved.signIn(inGroups(google("google-oauth2|moved", "Moved", "moved@example.com", true), "cyclists"))
	if got := moved.rider(); got != "moved" {
		t.Fatalf("first sign-in is %q, want moved", got)
	}

	resp := h.admin.do(http.MethodPut, peoplePath("google-oauth2|moved", "/binding"), `{"rider":"Wilant"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT binding = %d: %v", resp.StatusCode, jsonBody(t, resp))
	}
	if rider, ok := h.binding("google-oauth2|moved"); !ok || rider != "wilant" {
		t.Errorf("binding = %q, %v; want wilant", rider, ok)
	}
	// The session held the old rider; it is gone, not silently still "moved".
	if got := moved.rider(); got != "" {
		t.Errorf("the moved account's old session still answers as %q", got)
	}
	moved.signIn(inGroups(google("google-oauth2|moved", "Moved", "moved@example.com", true), "cyclists"))
	if got := moved.rider(); got != "wilant" {
		t.Errorf("after signing in again the rider is %q, want wilant", got)
	}

	out := h.logs.String()
	if !strings.Contains(out, "identity binding moved by admin") || !strings.Contains(out, "rider=wilant") || !strings.Contains(out, "from=moved") {
		t.Errorf("no log of the move naming the riders:\n%s", out)
	}
	for _, leak := range []string{"@example.com", "google-oauth2|moved"} {
		if strings.Contains(out, leak) {
			t.Errorf("the log contains %q:\n%s", leak, out)
		}
	}
}

func TestAnAdminCanBindAnAccountThatHasNoBindingYet(t *testing.T) {
	h := newBindingHarness(t, person("auth0|unbound", "Pat", "pat@example.com", false))
	resp := h.admin.do(http.MethodPut, peoplePath("auth0|unbound", "/binding"), `{"rider":"pat"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT binding = %d: %v", resp.StatusCode, jsonBody(t, resp))
	}
	if rider, ok := h.binding("auth0|unbound"); !ok || rider != "pat" {
		t.Errorf("binding = %q, %v; want pat", rider, ok)
	}
}

func TestOnlyAnAdminMovesABindingAndNeverTheirOwn(t *testing.T) {
	h := newBindingHarness(t, person("google-oauth2|target", "Target", "target@example.com", true))
	bindTo(t, h.ssoHarness, "google-oauth2|target", "target", "target@example.com")

	rider := h.newActor()
	rider.signIn(inGroups(google("google-oauth2|rider", "Rider", "rider@example.com", true), "cyclists"))
	if resp := rider.do(http.MethodPut, peoplePath("google-oauth2|target", "/binding"), `{"rider":"rider"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a rider moving a binding = %d, want 403", resp.StatusCode)
	}
	// A rider moving their own binding onto somebody else is the attack this prevents.
	if resp := rider.do(http.MethodPut, peoplePath("google-oauth2|rider", "/binding"), `{"rider":"target"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a rider moving their own binding = %d, want 403", resp.StatusCode)
	}
	// And an admin moves other people's, not their own.
	resp := h.admin.do(http.MethodPut, peoplePath("auth0|boss", "/binding"), `{"rider":"target"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("an admin moving their own binding = %d, want 403", resp.StatusCode)
	}
	if got := jsonBody(t, resp)["error"]; got == nil || !strings.Contains(got.(string), "bind-identity") {
		t.Errorf("the refusal should point to the CLI, got %v", got)
	}
	if rider, _ := h.binding("auth0|boss"); rider != "boss" {
		t.Errorf("the admin's own binding became %q", rider)
	}
	if rider, _ := h.binding("google-oauth2|target"); rider != "target" {
		t.Errorf("the target's binding became %q", rider)
	}
}

func TestMovingABindingRejectsAnIllegalRider(t *testing.T) {
	h := newBindingHarness(t, person("auth0|x", "X", "x@example.com", true))
	for _, body := range []string{`{"rider":"has spaces"}`, `{"rider":"   "}`, `{}`, `not json`, `{"rider":"a/b"}`} {
		if resp := h.admin.do(http.MethodPut, peoplePath("auth0|x", "/binding"), body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, resp.StatusCode)
		}
	}
	if _, ok := h.binding("auth0|x"); ok {
		t.Error("an illegal rider was bound")
	}
}

func TestDeletingAPersonRemovesTheirBinding(t *testing.T) {
	h := newBindingHarness(t, person("auth0|gone", "Gone", "gone@example.com", true))
	bindTo(t, h.ssoHarness, "auth0|gone", "gone", "gone@example.com")
	bindTo(t, h.ssoHarness, "google-oauth2|stays", "stays", "stays@example.com")

	// Auth0 failing leaves everything as it was.
	h.people.deleteUserErr = errors.New("auth0 is down")
	if resp := h.admin.do(http.MethodDelete, peoplePath("auth0|gone", ""), ""); resp.StatusCode == http.StatusOK {
		t.Fatal("a failed Auth0 delete reported success")
	}
	if _, ok := h.binding("auth0|gone"); !ok {
		t.Error("the binding was removed although the account was not deleted")
	}

	h.people.deleteUserErr = nil
	if resp := h.admin.do(http.MethodDelete, peoplePath("auth0|gone", ""), ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE = %d", resp.StatusCode)
	}
	if _, ok := h.binding("auth0|gone"); ok {
		t.Error("the deleted person is still bound")
	}
	if _, ok := h.binding("google-oauth2|stays"); !ok {
		t.Error("another person's binding was removed")
	}
}

// With no identity store no session can be checked against a binding, so none is
// honoured: the request is signed out before the handler (and its own 412 guard)
// is ever reached.
func TestMovingABindingWithoutAnIdentityStoreFailsClosed(t *testing.T) {
	h := newBindingHarness(t, person("auth0|x", "X", "x@example.com", true))
	h.srv.Identities = nil
	if resp := h.admin.do(http.MethodPut, peoplePath("auth0|x", "/binding"), `{"rider":"x"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
