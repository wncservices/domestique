package api_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
)

// A calendar feed outlives a sign-in on purpose (a calendar app fetches with
// no session), so it is the one thing "blocked" or "no longer a rider" would
// otherwise leave working. These tests hold the admin hooks that reach it.

type accessHarness struct {
	*calHarness
	people *fakePeople
}

func newAccessHarness(t *testing.T, dsn string) *accessHarness {
	serialOnly(t) // built on newCalHarness, which swaps the global tracer provider
	t.Helper()
	h := newCalHarness(t, auth.ModeProxy, dsn, calendarPublicURL)
	fake := &fakePeople{people: []auth0mgmt.Person{
		{UserID: "auth0|alice", Email: "alice@example.com", Name: "alice", Roles: []string{"gate", "cyclists"}},
		{UserID: "auth0|bob", Email: "bob@example.com", Name: "bob", Roles: []string{"gate", "cyclists"}},
	}}
	h.srv.People = fake
	h.srv.Blocklist = newTestBlocklist(t)
	return &accessHarness{calHarness: h, people: fake}
}

// admin sends one People-page request as an admin, with a body.
func (h *accessHarness) admin(method, path, body string) int {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	h.identify(req, "boss", "admins")
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func personPath(id, tail string) string { return "/api/people/" + url.PathEscape(id) + tail }

// withFeed gives name an active feed and returns its path.
func (h *accessHarness) withFeed(name string) string {
	h.t.Helper()
	path := h.generate(name)
	if h.feedCode(path) != http.StatusOK {
		h.t.Fatalf("setup: %s's feed = %d", name, h.feedCode(path))
	}
	return path
}

func (h *accessHarness) feedCode(path string) int {
	h.t.Helper()
	return h.do(http.MethodGet, path, nil).code
}

func TestBlockingARiderRevokesTheirFeed(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newAccessHarness(t, dsn)
			alice, bob := h.withFeed("alice"), h.withFeed("bob")

			code := h.admin(http.MethodPut, personPath("auth0|alice", "/blocked"), `{"blocked":true,"email":"alice@example.com"}`)
			if code != http.StatusOK {
				t.Fatalf("block = %d", code)
			}
			if got := h.feedCode(alice); got != http.StatusNotFound {
				t.Errorf("a blocked rider's feed = %d, want 404", got)
			}
			// Nobody else is touched.
			if got := h.feedCode(bob); got != http.StatusOK {
				t.Errorf("another rider lost their feed: %d", got)
			}
		})
	}
}

func TestUnblockingDoesNotSilentlyReEnableTheFeed(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newAccessHarness(t, dsn)
			alice := h.withFeed("alice")
			h.admin(http.MethodPut, personPath("auth0|alice", "/blocked"), `{"blocked":true,"email":"alice@example.com"}`)
			if code := h.admin(http.MethodPut, personPath("auth0|alice", "/blocked"), `{"blocked":false,"email":"alice@example.com"}`); code != http.StatusOK {
				t.Fatalf("unblock = %d", code)
			}
			if got := h.feedCode(alice); got != http.StatusNotFound {
				t.Errorf("the old feed URL came back to life after unblocking: %d", got)
			}
			if h.generate("alice") == alice {
				t.Error("a new feed should be a new URL")
			}
		})
	}
}

func TestDowngradingARiderRevokesTheirFeed(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newAccessHarness(t, dsn)
			alice, bob := h.withFeed("alice"), h.withFeed("bob")

			// Becoming an admin keeps it.
			if code := h.admin(http.MethodPut, personPath("auth0|bob", "/role"), `{"role":"admin"}`); code != http.StatusOK {
				t.Fatalf("promote = %d", code)
			}
			if got := h.feedCode(bob); got != http.StatusOK {
				t.Errorf("a role that still trains lost its feed: %d", got)
			}

			// A viewer cannot train, so holds no feed.
			if code := h.admin(http.MethodPut, personPath("auth0|alice", "/role"), `{"role":"viewer"}`); code != http.StatusOK {
				t.Fatalf("demote = %d", code)
			}
			if got := h.feedCode(alice); got != http.StatusNotFound {
				t.Errorf("a viewer's old feed = %d, want 404", got)
			}
		})
	}
}
