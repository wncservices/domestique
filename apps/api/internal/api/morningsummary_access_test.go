package api_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
	"github.com/wncservices/domestique/apps/api/internal/calendarfeed"
)

// Access that an admin takes away has to take the feed and the email with it.
// Both outlive a sign-in on purpose (a calendar app fetches with no session; the
// morning pass runs with none), so they are the two things "blocked" or "no
// longer a rider" would otherwise leave working.

type accessHarness struct {
	*msHarness
	people *fakePeople
}

func newAccessHarness(t *testing.T, dsn string) *accessHarness {
	t.Helper()
	h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{})
	fake := &fakePeople{people: []auth0mgmt.Person{
		{UserID: "auth0|alice", Email: "alice@example.com", Name: "alice", Roles: []string{"gate", "cyclists"}},
		{UserID: "auth0|bob", Email: "bob@example.com", Name: "bob", Roles: []string{"gate", "cyclists"}},
	}}
	feeds, err := calendarfeed.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.CalendarFeeds = feeds
	h.srv.CalendarLimiter = api.NewCalendarLimiter()
	h.srv.CalendarMissLimiter = api.NewCalendarMissLimiter()
	h.srv.People = fake
	h.srv.Blocklist = newTestBlocklist(t)
	return &accessHarness{msHarness: h, people: fake}
}

// admin sends one People-page request as an admin, with a body.
func (h *accessHarness) admin(method, path, body string) int {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	h.identifyAs(req, "boss", "boss@example.com", true, "admins")
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func personPath(id, tail string) string { return "/api/people/" + url.PathEscape(id) + tail }

// optedIn gives name an active feed and an opted-in summary, and returns the
// feed's path.
func (h *accessHarness) optedIn(name string) string {
	h.t.Helper()
	path := h.generate(name)
	h.optIn(name, name+"@example.com")
	if r := h.do(http.MethodGet, path, nil); r.code != http.StatusOK {
		h.t.Fatalf("setup: %s's feed = %d", name, r.code)
	}
	return path
}

func (h *accessHarness) feedCode(path string) int {
	h.t.Helper()
	return h.do(http.MethodGet, path, nil).code
}

func (h *accessHarness) summaryOn(name string) bool {
	h.t.Helper()
	p, ok, err := h.ms.Get(h.t.Context(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	return ok && p.Enabled
}

func (h *accessHarness) runPass() {
	h.t.Helper()
	h.at(7, 0)
	h.srv.RunMetricsSyncIfMissed(h.t.Context())
}

func TestBlockingARiderRevokesTheirFeedAndSummary(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newAccessHarness(t, dsn)
			alice, bob := h.optedIn("alice"), h.optedIn("bob")

			code := h.admin(http.MethodPut, personPath("auth0|alice", "/blocked"), `{"blocked":true,"email":"alice@example.com"}`)
			if code != http.StatusOK {
				t.Fatalf("block = %d", code)
			}
			if got := h.feedCode(alice); got != http.StatusNotFound {
				t.Errorf("a blocked rider's feed = %d, want 404", got)
			}
			if h.summaryOn("alice") {
				t.Error("a blocked rider is still opted in to the summary")
			}
			if p, _, _ := h.ms.Get(t.Context(), "alice"); p.Email != "" {
				t.Errorf("the blocked rider's address %q is still stored", p.Email)
			}
			// Nobody else is touched.
			if got := h.feedCode(bob); got != http.StatusOK || !h.summaryOn("bob") {
				t.Errorf("another rider lost access: feed %d, summary on %v", got, h.summaryOn("bob"))
			}

			h.runPass()
			for _, m := range h.notif.mails() {
				if m.to == "alice@example.com" {
					t.Errorf("a blocked rider was mailed: %q", m.subject)
				}
			}
			if h.notif.callsTo("bob@example.com") != 1 {
				t.Error("the rider who was not blocked should still be mailed")
			}
		})
	}
}

func TestUnblockingDoesNotSilentlyReEnableAnything(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newAccessHarness(t, dsn)
			alice := h.optedIn("alice")
			h.admin(http.MethodPut, personPath("auth0|alice", "/blocked"), `{"blocked":true,"email":"alice@example.com"}`)
			if code := h.admin(http.MethodPut, personPath("auth0|alice", "/blocked"), `{"blocked":false,"email":"alice@example.com"}`); code != http.StatusOK {
				t.Fatalf("unblock = %d", code)
			}
			if got := h.feedCode(alice); got != http.StatusNotFound {
				t.Errorf("the old feed URL came back to life after unblocking: %d", got)
			}
			if h.summaryOn("alice") {
				t.Error("unblocking re-enabled the summary: the rider has to opt in again")
			}
			h.runPass()
			if n := h.notif.callsTo("alice@example.com"); n != 0 {
				t.Errorf("an unblocked rider who never opted in again was mailed %d times", n)
			}
			// And opting in again works as it did the first time.
			h.optIn("alice", "alice@example.com")
			if !h.summaryOn("alice") {
				t.Error("could not opt in again")
			}
			if h.generate("alice") == alice {
				t.Error("a new feed should be a new URL")
			}
		})
	}
}

func TestDowngradingARiderRevokesTheirFeedAndSummary(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newAccessHarness(t, dsn)
			alice, bob := h.optedIn("alice"), h.optedIn("bob")

			// Staying a rider (or becoming an admin) keeps both.
			if code := h.admin(http.MethodPut, personPath("auth0|bob", "/role"), `{"role":"admin"}`); code != http.StatusOK {
				t.Fatalf("promote = %d", code)
			}
			if got := h.feedCode(bob); got != http.StatusOK || !h.summaryOn("bob") {
				t.Errorf("a role that still trains lost access: feed %d, summary %v", got, h.summaryOn("bob"))
			}

			// A viewer cannot train, so they hold neither.
			if code := h.admin(http.MethodPut, personPath("auth0|alice", "/role"), `{"role":"viewer"}`); code != http.StatusOK {
				t.Fatalf("demote = %d", code)
			}
			if got := h.feedCode(alice); got != http.StatusNotFound {
				t.Errorf("a viewer's old feed = %d, want 404", got)
			}
			if h.summaryOn("alice") {
				t.Error("a viewer is still opted in to the summary")
			}
			h.runPass()
			if n := h.notif.callsTo("alice@example.com"); n != 0 {
				t.Errorf("a viewer was mailed %d times", n)
			}
		})
	}
}

// If a hook cannot find a rider (a login long expired, a name nobody can
// derive), the morning pass is the backstop: a rider whose stored address is on
// the blocklist is skipped and switched off, never mailed.
func TestThePassSkipsAndDisablesABlockedAddress(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newAccessHarness(t, dsn)
			h.optedIn("alice")
			h.optedIn("bob")
			if err := h.srv.Blocklist.Block(t.Context(), "ALICE@example.com", "boss", ""); err != nil {
				t.Fatal(err)
			}

			h.runPass()
			if n := h.notif.callsTo("alice@example.com"); n != 0 {
				t.Errorf("a blocklisted address was mailed %d times", n)
			}
			if h.summaryOn("alice") {
				t.Error("a blocklisted rider stays opted in: unblocking would silently resume the mail")
			}
			if h.notif.callsTo("bob@example.com") != 1 {
				t.Error("the other rider was not mailed")
			}
			if strings.Contains(h.logs.String(), "alice@example.com") {
				t.Error("the address reached the logs")
			}
		})
	}
}

// The People page's own guess at a rider name can be wrong or empty. The rider
// is still found through what is certain: the identity's own sessions, or the
// address the summary was opted in under.
func TestBlockingFindsTheRiderBySessionWhenTheNameCannotBeGuessed(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newMSHarness(t, auth.ModeOIDC, dsn, msOpts{})
			feeds, err := calendarfeed.UseDB(h.db.Conn(), h.db.DSN())
			if err != nil {
				t.Fatal(err)
			}
			h.srv.CalendarFeeds = feeds
			h.srv.CalendarLimiter = api.NewCalendarLimiter()
			h.srv.CalendarMissLimiter = api.NewCalendarMissLimiter()
			h.srv.Sessions = h.sessions
			// A display name that is not a legal rider name, and an id that is not
			// one either: likelyRider has nothing to offer.
			h.srv.People = &fakePeople{people: []auth0mgmt.Person{
				{UserID: "auth0|alice", Email: "different@example.com", Name: "Alice Q. Rider", Roles: []string{"gate", "cyclists"}},
			}}
			h.srv.Blocklist = newTestBlocklist(t)
			path := h.generate("alice") // signs alice in: a session whose sub is auth0|alice
			h.optIn("alice", "alice@example.com")

			ah := &accessHarness{msHarness: h}
			if code := ah.admin(http.MethodPut, personPath("auth0|alice", "/blocked"), `{"blocked":true,"email":"unrelated@example.com"}`); code != http.StatusOK {
				t.Fatalf("block = %d", code)
			}
			if got := h.do(http.MethodGet, path, nil).code; got != http.StatusNotFound {
				t.Errorf("feed = %d, want 404", got)
			}
			if ah.summaryOn("alice") {
				t.Error("the summary is still on")
			}
		})
	}
}

func TestBlockingFindsTheRiderByTheAddressTheyOptedInUnder(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newAccessHarness(t, dsn)
			h.people.people = nil // the lookup finds nobody, and there are no sessions in proxy mode
			h.optedIn("alice")
			if code := h.admin(http.MethodPut, personPath("auth0|someone", "/blocked"), `{"blocked":true,"email":"Alice@Example.com"}`); code != http.StatusOK {
				t.Fatalf("block = %d", code)
			}
			if h.summaryOn("alice") {
				t.Error("the summary is still on")
			}
		})
	}
}
