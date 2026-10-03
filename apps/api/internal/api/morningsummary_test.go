// Acceptance tests for the morning summary: opting in, the verified-address
// rule, the test send, and the pass that sends after the morning sync.
package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/mailer"
	"github.com/wncservices/domestique/apps/api/internal/morningsummary"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type sentMail struct{ to, subject, body string }

// notifier is the fake relay: it records what would have been sent and never
// touches a network.
type notifier struct {
	mu    sync.Mutex
	sent  []sentMail
	calls map[string]int
	// fail, when set, decides per recipient whether Send errors.
	fail func(to string) error
}

func (n *notifier) Send(_ context.Context, to, subject, body string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.calls == nil {
		n.calls = map[string]int{}
	}
	n.calls[to]++
	if n.fail != nil {
		if err := n.fail(to); err != nil {
			return err
		}
	}
	n.sent = append(n.sent, sentMail{to, subject, body})
	return nil
}

func (n *notifier) mails() []sentMail {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]sentMail(nil), n.sent...)
}

func (n *notifier) callsTo(to string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls[to]
}

type msOpts struct {
	noSMTP   bool
	noMailer bool
}

type msHarness struct {
	*calHarness
	ms    *morningsummary.Store
	notif *notifier
	clock time.Time
	loc   *time.Location
	db    *source.DB
}

func newMSHarness(t *testing.T, mode auth.Mode, dsn string, opts msOpts) *msHarness {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	db, err := source.OpenDB(freshDSN(t, dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	training, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	ms, err := morningsummary.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.GenerateKey()
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}

	var a *auth.Authenticator
	var sess *sessions.Store
	roles := auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}, Viewer: []string{"guests"}}
	switch mode {
	case auth.ModeProxy:
		a, err = auth.New(auth.Config{Mode: auth.ModeProxy, RequiredGroup: "gate", Roles: roles})
	case auth.ModeOIDC:
		a, err = auth.New(auth.Config{Mode: auth.ModeOIDC, RequiredGroup: "gate", Roles: roles,
			OIDC: auth.OIDCConfig{Issuer: "https://issuer.example.com", ClientID: "c", RedirectURL: "https://domestique.example.com/sso/callback"}})
	default:
		a, err = auth.New(auth.Config{Mode: auth.ModeNone})
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == auth.ModeOIDC {
		if sess, err = sessions.UseDB(db.Conn(), db.DSN(), box); err != nil {
			t.Fatal(err)
		}
		a.UseSessions(sess)
	}

	logs := &lockedBuffer{}
	notif := &notifier{}
	h := &msHarness{ms: ms, notif: notif, loc: loc, db: db,
		clock: time.Date(2026, 6, 10, 7, 0, 0, 0, loc)}
	cfg := &config.Config{PublicURL: calendarPublicURL}
	if !opts.noSMTP {
		cfg.Notifications.SMTP = config.SMTPConfig{Host: "smtp.example.com", Port: 587, Security: "starttls", From: "Domestique <d@example.com>"}
	}
	srv := &api.Server{
		Auth: a, Training: training, MorningSummaries: ms, Links: links, Config: cfg,
		Log:             slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:           func() time.Time { return h.clock },
		TestMailLimiter: api.NewTestMailLimiter(),
	}
	if !opts.noMailer {
		srv.Mailer = notif
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	h.calHarness = &calHarness{t: t, mode: mode, base: server.URL, client: server.Client(), srv: srv,
		training: training, sessions: sess, logs: logs}
	return h
}

// identifyAs signs req in as user with this address, vouched for or not.
func (h *msHarness) identifyAs(req *http.Request, user, email string, verified bool, groups ...string) {
	switch h.mode {
	case auth.ModeProxy:
		req.Header.Set(auth.HeaderUser, user)
		req.Header.Set(auth.HeaderGroups, strings.Join(append([]string{"gate"}, groups...), ","))
		if email != "" {
			req.Header.Set(auth.HeaderEmail, email)
		}
	case auth.ModeOIDC:
		tok, _, err := h.sessions.Create(auth.Identity{User: user, Sub: "auth0|" + user, Email: email, EmailVerified: verified,
			Groups: append([]string{"gate"}, groups...)}, time.Hour)
		if err != nil {
			h.t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	}
}

func (h *msHarness) call(method, path, body, user, email string, verified bool) calResp {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	h.identifyAs(req, user, email, verified, "cyclists")
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return calResp{resp.StatusCode, resp.Header, string(b)}
}

type msStatus struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Enabled   bool   `json:"enabled"`
	Email     string `json:"email"`
}

func (h *msHarness) status(user, email string, verified bool) msStatus {
	h.t.Helper()
	r := h.call(http.MethodGet, "/api/training/morning-summary", "", user, email, verified)
	if r.code != http.StatusOK {
		h.t.Fatalf("GET status = %d %s", r.code, r.body)
	}
	var s msStatus
	if err := json.Unmarshal([]byte(r.body), &s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *msHarness) optIn(user, email string) {
	h.t.Helper()
	r := h.call(http.MethodPut, "/api/training/morning-summary", `{"enabled":true}`, user, email, true)
	if r.code != http.StatusOK {
		h.t.Fatalf("opting %s in = %d %s", user, r.code, r.body)
	}
}

func eachMSMode(t *testing.T, modes []auth.Mode, run func(t *testing.T, h *msHarness)) {
	t.Helper()
	for engine, dsn := range calendarEngines(t) {
		for _, mode := range modes {
			t.Run(engine+"/"+string(mode), func(t *testing.T) { run(t, newMSHarness(t, mode, dsn, msOpts{})) })
		}
	}
}

var gatedModes = []auth.Mode{auth.ModeProxy, auth.ModeOIDC}

func TestMorningSummaryStatus(t *testing.T) {
	eachMSMode(t, gatedModes, func(t *testing.T, h *msHarness) {
		s := h.status("wilant", "wilant@example.com", true)
		if !s.Available || s.Enabled || s.Email != "wilant@example.com" || s.Reason != "" {
			t.Fatalf("status = %+v", s)
		}
		h.optIn("wilant", "wilant@example.com")
		if s := h.status("wilant", "wilant@example.com", true); !s.Enabled {
			t.Fatalf("after opting in: %+v", s)
		}
	})
}

func TestMorningSummaryUnavailableReasons(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine+"/mode none has no email", func(t *testing.T) {
			h := newMSHarness(t, auth.ModeNone, dsn, msOpts{})
			s := h.status("local", "", false)
			if s.Available || s.Reason != "no_email" {
				t.Fatalf("status = %+v, want unavailable: no_email", s)
			}
			r := h.call(http.MethodPut, "/api/training/morning-summary", `{"enabled":true}`, "local", "", false)
			if r.code != http.StatusPreconditionFailed {
				t.Fatalf("PUT = %d, want 412", r.code)
			}
			if !strings.Contains(h.logs.String(), "level=WARN") || !strings.Contains(h.logs.String(), "no_email") {
				t.Errorf("the 412 left no Warn:\n%s", h.logs.String())
			}
		})
		t.Run(engine+"/no SMTP configured", func(t *testing.T) {
			h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{noSMTP: true})
			s := h.status("wilant", "wilant@example.com", true)
			if s.Available || s.Reason != "not_configured" {
				t.Fatalf("status = %+v, want unavailable: not_configured", s)
			}
			if r := h.call(http.MethodPut, "/api/training/morning-summary", `{"enabled":true}`, "wilant", "wilant@example.com", true); r.code != http.StatusPreconditionFailed {
				t.Fatalf("PUT = %d, want 412", r.code)
			}
			if r := h.call(http.MethodPost, "/api/training/morning-summary/test", "", "wilant", "wilant@example.com", true); r.code != http.StatusPreconditionFailed {
				t.Fatalf("test send = %d, want 412", r.code)
			}
			if !strings.Contains(h.logs.String(), "level=WARN") {
				t.Errorf("the 412 left no Warn:\n%s", h.logs.String())
			}
			assertNoAddress(t, h.logs.String())
		})
		t.Run(engine+"/oidc: an unverified address cannot opt in", func(t *testing.T) {
			h := newMSHarness(t, auth.ModeOIDC, dsn, msOpts{})
			s := h.status("wilant", "wilant@example.com", false)
			if s.Available || s.Reason != "email_unverified" {
				t.Fatalf("status = %+v, want unavailable: email_unverified", s)
			}
			r := h.call(http.MethodPut, "/api/training/morning-summary", `{"enabled":true}`, "wilant", "wilant@example.com", false)
			if r.code != http.StatusPreconditionFailed {
				t.Fatalf("PUT = %d, want 412", r.code)
			}
			if p, ok, _ := h.ms.Get(t.Context(), "wilant"); ok && p.Enabled {
				t.Fatal("an unverified address was opted in")
			}
			if r := h.call(http.MethodPost, "/api/training/morning-summary/test", "", "wilant", "wilant@example.com", false); r.code != http.StatusPreconditionFailed {
				t.Fatalf("test send to an unverified address = %d, want 412", r.code)
			}
			if len(h.notif.mails()) != 0 {
				t.Fatal("mail went to an unverified address")
			}
			assertNoAddress(t, h.logs.String())
		})
	}
}

func assertNoAddress(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "@example.com") && !strings.Contains(text, "smtp.example.com") {
		t.Errorf("an address reached the logs:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(strings.ReplaceAll(line, "smtp.example.com", ""), "@example.com") {
			t.Errorf("an address reached a log line: %s", line)
		}
	}
}

// The address comes from the authenticated identity, never the body: a typed
// address would make this a way to send mail to somebody else.
func TestOptInTakesTheAddressFromTheIdentityOnly(t *testing.T) {
	eachMSMode(t, gatedModes, func(t *testing.T, h *msHarness) {
		r := h.call(http.MethodPut, "/api/training/morning-summary",
			`{"enabled":true,"email":"victim@example.org","to":"victim@example.org"}`, "wilant", "wilant@example.com", true)
		if r.code != http.StatusOK {
			t.Fatalf("PUT = %d %s", r.code, r.body)
		}
		p, ok, err := h.ms.Get(t.Context(), "wilant")
		if err != nil || !ok || !p.Enabled || p.Email != "wilant@example.com" {
			t.Fatalf("stored %+v %v %v; the body's address must be ignored", p, ok, err)
		}
		if strings.Contains(r.body, "victim") {
			t.Error("the body's address was echoed")
		}
	})
}

func TestOptOutForgetsTheAddress(t *testing.T) {
	eachMSMode(t, gatedModes, func(t *testing.T, h *msHarness) {
		h.optIn("wilant", "wilant@example.com")
		r := h.call(http.MethodPut, "/api/training/morning-summary", `{"enabled":false}`, "wilant", "wilant@example.com", true)
		if r.code != http.StatusOK {
			t.Fatalf("PUT = %d %s", r.code, r.body)
		}
		if p, _, _ := h.ms.Get(t.Context(), "wilant"); p.Enabled || p.Email != "" {
			t.Fatalf("stored %+v after opting out", p)
		}
		// Turning it off never needs the feature to be available.
		hn := h.call(http.MethodPut, "/api/training/morning-summary", `{"enabled":false}`, "wilant", "", false)
		if hn.code != http.StatusOK {
			t.Fatalf("opting out without an address = %d", hn.code)
		}
	})
}

func TestPutNeedsAnEnabledFlag(t *testing.T) {
	eachMSMode(t, gatedModes, func(t *testing.T, h *msHarness) {
		for _, body := range []string{`{}`, `{"email":"a@b.example"}`, `nope`} {
			if r := h.call(http.MethodPut, "/api/training/morning-summary", body, "wilant", "wilant@example.com", true); r.code != http.StatusBadRequest {
				t.Errorf("PUT %q = %d, want 400", body, r.code)
			}
		}
	})
}

// Reading the status refreshes a changed address, and fails closed on an
// address nobody vouches for any more.
func TestStatusRefreshesTheAddress(t *testing.T) {
	eachMSMode(t, gatedModes, func(t *testing.T, h *msHarness) {
		h.optIn("wilant", "old@example.com")
		s := h.status("wilant", "new@example.com", true)
		if s.Email != "new@example.com" || !s.Enabled {
			t.Fatalf("status = %+v", s)
		}
		if p, _, _ := h.ms.Get(t.Context(), "wilant"); p.Email != "new@example.com" {
			t.Fatalf("stored %q, want the refreshed address", p.Email)
		}
	})
}

func TestAnAddressThatStopsBeingVerifiedSwitchesTheSummaryOff(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newMSHarness(t, auth.ModeOIDC, dsn, msOpts{})
			h.optIn("wilant", "wilant@example.com")
			s := h.status("wilant", "wilant@example.com", false)
			if s.Available || s.Enabled {
				t.Fatalf("status = %+v", s)
			}
			if p, _, _ := h.ms.Get(t.Context(), "wilant"); p.Enabled || p.Email != "" {
				t.Fatalf("stored %+v: an address the issuer no longer vouches for must not stay opted in", p)
			}
		})
	}
}

func TestTestSendGoesToTheRidersOwnAddressOnly(t *testing.T) {
	eachMSMode(t, gatedModes, func(t *testing.T, h *msHarness) {
		r := h.call(http.MethodPost, "/api/training/morning-summary/test", `{"to":"victim@example.org","email":"victim@example.org"}`,
			"wilant", "wilant@example.com", true)
		if r.code != http.StatusNoContent {
			t.Fatalf("POST test = %d %s", r.code, r.body)
		}
		mails := h.notif.mails()
		if len(mails) != 1 || mails[0].to != "wilant@example.com" {
			t.Fatalf("sent %+v, want one mail to the rider's own address", mails)
		}
		if !strings.Contains(strings.ToLower(mails[0].subject), "test") {
			t.Errorf("subject %q does not say it is a test", mails[0].subject)
		}
		assertNoAddress(t, h.logs.String())
	})
}

func TestTestSendIsLimitedToFiveEveryFifteenMinutes(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{})
			for i := 1; i <= 5; i++ {
				if r := h.call(http.MethodPost, "/api/training/morning-summary/test", "", "wilant", "wilant@example.com", true); r.code != http.StatusNoContent {
					t.Fatalf("send %d = %d", i, r.code)
				}
			}
			if r := h.call(http.MethodPost, "/api/training/morning-summary/test", "", "wilant", "wilant@example.com", true); r.code != http.StatusTooManyRequests {
				t.Fatalf("the sixth send = %d, want 429", r.code)
			}
			// Per rider: another rider is not throttled by it.
			if r := h.call(http.MethodPost, "/api/training/morning-summary/test", "", "riderb", "b@example.com", true); r.code != http.StatusNoContent {
				t.Fatalf("another rider = %d", r.code)
			}
		})
	}
}

func TestTestSendFailureSaysStageAndCodeOnly(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{})
			h.notif.fail = func(string) error { return &mailer.SendError{Stage: "rcpt", Code: 550} }
			r := h.call(http.MethodPost, "/api/training/morning-summary/test", "", "wilant", "wilant@example.com", true)
			if r.code != http.StatusBadGateway {
				t.Fatalf("POST test = %d, want 502", r.code)
			}
			assertNoAddress(t, r.body)
			assertNoAddress(t, h.logs.String())
			if !strings.Contains(h.logs.String(), "rcpt") || !strings.Contains(h.logs.String(), "550") {
				t.Errorf("the Warn should carry stage and code:\n%s", h.logs.String())
			}
		})
	}
}

func TestMorningSummaryIsOwnerOnly(t *testing.T) {
	eachMSMode(t, gatedModes, func(t *testing.T, h *msHarness) {
		h.optIn("wilant", "wilant@example.com")
		// Another rider sees and changes only their own.
		if s := h.status("riderb", "b@example.com", true); s.Enabled {
			t.Fatal("another rider sees someone else's opt-in")
		}
		h.call(http.MethodPut, "/api/training/morning-summary", `{"enabled":false}`, "riderb", "b@example.com", true)
		if p, _, _ := h.ms.Get(t.Context(), "wilant"); !p.Enabled {
			t.Fatal("another rider switched wilant's summary off")
		}
		// A viewer has no training role; nobody anonymous gets in.
		req, _ := http.NewRequest(http.MethodPut, h.base+"/api/training/morning-summary", strings.NewReader(`{"enabled":true}`))
		h.identifyAs(req, "viewer", "v@example.com", true, "guests")
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("viewer PUT = %d, want 403", resp.StatusCode)
		}
		if r := h.do(http.MethodGet, "/api/training/morning-summary", nil); r.code != http.StatusUnauthorized {
			t.Errorf("anonymous GET = %d, want 401", r.code)
		}
	})
}

// ----- the sign-in side: the claim has to be real, via a real OIDC round trip

func TestOIDCEmailVerifiedClaimDecidesWhetherARiderCanOptIn(t *testing.T) {
	cases := map[string]struct {
		claims map[string]any
		want   bool
		reason string
	}{
		"verified":           {map[string]any{"email": "wilant@example.com", "email_verified": true}, true, ""},
		"verified string":    {map[string]any{"email": "wilant@example.com", "email_verified": "true"}, true, ""},
		"unverified":         {map[string]any{"email": "wilant@example.com", "email_verified": false}, false, "email_unverified"},
		"claim missing":      {map[string]any{"email": "wilant@example.com"}, false, "email_unverified"},
		"junk claim":         {map[string]any{"email": "wilant@example.com", "email_verified": "yes please"}, false, "email_unverified"},
		"verified, no email": {map[string]any{"email_verified": true}, false, "no_email"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			db, err := source.OpenDB(filepath.Join(t.TempDir(), "ms.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			ms, err := morningsummary.UseDB(db.Conn(), db.DSN())
			if err != nil {
				t.Fatal(err)
			}
			notif := &notifier{}
			h := newSSOHarness(t, func(s *api.Server) {
				s.MorningSummaries, s.Mailer = ms, notif
				s.Config = &config.Config{PublicURL: calendarPublicURL, Notifications: config.NotificationsConfig{
					SMTP: config.SMTPConfig{Host: "smtp.example.com", From: "d@example.com"}}}
			})
			claims := map[string]any{"preferred_username": "wilant"}
			for k, v := range c.claims {
				claims[k] = v
			}
			if resp := h.loginWithUser([]string{"cyclists"}, claims); resp.StatusCode != http.StatusFound {
				t.Fatalf("login = %d", resp.StatusCode)
			}

			resp := h.get("/api/training/morning-summary")
			var s msStatus
			if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
				t.Fatal(err)
			}
			if s.Available != c.want || s.Reason != c.reason {
				t.Fatalf("status = %+v, want available=%v reason=%q", s, c.want, c.reason)
			}

			put, err := http.NewRequest(http.MethodPut, h.base+"/api/training/morning-summary", strings.NewReader(`{"enabled":true}`))
			if err != nil {
				t.Fatal(err)
			}
			pr, err := h.client.Do(put)
			if err != nil {
				t.Fatal(err)
			}
			_ = pr.Body.Close()
			wantCode := http.StatusOK
			if !c.want {
				wantCode = http.StatusPreconditionFailed
			}
			if pr.StatusCode != wantCode {
				t.Fatalf("PUT = %d, want %d", pr.StatusCode, wantCode)
			}
			rider := "wilant"
			p, ok, _ := ms.Get(t.Context(), rider)
			if enabled := ok && p.Enabled; enabled != c.want {
				t.Fatalf("stored enabled = %v, want %v", enabled, c.want)
			}
		})
	}
}

// ----- the pass after the morning sync

const mailDay = "2026-06-10"

func (h *msHarness) at(hour, min int) {
	h.clock = time.Date(2026, 6, 10, hour, min, 0, 0, h.loc)
}

func (h *msHarness) seedToday(rider, name string) {
	h.t.Helper()
	h.seedWorkout(rider, name, mailDay)
}

func (h *msHarness) lastSent(rider string) string {
	h.t.Helper()
	p, _, err := h.ms.Get(h.t.Context(), rider)
	if err != nil {
		h.t.Fatal(err)
	}
	return p.LastSentDate
}

func TestPassAtTheFirstSlotMailsEachOptedInRiderOnce(t *testing.T) {
	eachMSMode(t, gatedModes, func(t *testing.T, h *msHarness) {
		h.seedToday("wilant", "Threshold 3 x 10")
		h.optIn("wilant", "wilant@example.com")
		h.optIn("riderb", "b@example.com")
		h.call(http.MethodPut, "/api/training/morning-summary", `{"enabled":false}`, "riderc", "c@example.com", true) // never opted in

		h.at(7, 0)
		if !h.srv.RunMetricsSyncIfMissed(t.Context()) {
			t.Fatal("the 06:30 pass did not run")
		}
		mails := h.notif.mails()
		if len(mails) != 2 {
			t.Fatalf("sent %d mails, want 2: %+v", len(mails), mails)
		}
		byTo := map[string]sentMail{}
		for _, m := range mails {
			byTo[m.to] = m
		}
		if m := byTo["wilant@example.com"]; !strings.Contains(m.subject, "Threshold 3 x 10") || !strings.Contains(m.body, "Readiness: Ready") {
			t.Errorf("wilant's mail: %q\n%s", m.subject, m.body)
		}
		if m := byTo["b@example.com"]; !strings.Contains(m.subject, "rest day") {
			t.Errorf("riderb has nothing planned: %q", m.subject)
		}
		if h.lastSent("wilant") != mailDay {
			t.Errorf("last sent = %q, want %s (the zone's date)", h.lastSent("wilant"), mailDay)
		}

		// A second pass the same day sends none, however it is triggered.
		h.at(7, 30)
		h.srv.SyncTrainingMetrics(t.Context())
		// And the 21:00 pass never sends.
		h.at(21, 5)
		h.srv.RunMetricsSyncIfMissed(t.Context())
		h.srv.SyncTrainingMetrics(t.Context())
		if n := len(h.notif.mails()); n != 2 {
			t.Fatalf("%d mails after the extra passes, want still 2", n)
		}
	})
}

// "Today" is the schedule's zone, not the server's: at 23:30 UTC on the 9th it
// is already the 10th in Brussels.
func TestPassUsesTheDeploymentsDate(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{})
			h.seedToday("wilant", "Threshold 3 x 10")
			h.optIn("wilant", "wilant@example.com")
			h.clock = time.Date(2026, 6, 10, 4, 45, 0, 0, time.UTC) // 06:45 Brussels, 04:45 UTC
			h.srv.RunMetricsSyncIfMissed(t.Context())
			mails := h.notif.mails()
			if len(mails) != 1 || !strings.Contains(mails[0].subject, "Threshold 3 x 10") {
				t.Fatalf("mails = %+v", mails)
			}
		})
	}
}

func TestPassCutOffIsNoon(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		for _, c := range []struct {
			name       string
			hour, min  int
			wantMailed bool
		}{{"11:59 still sends", 11, 59, true}, {"12:00 is too late", 12, 0, false}, {"12:01 is too late", 12, 1, false}, {"15:00 is too late", 15, 0, false}} {
			t.Run(engine+"/"+c.name, func(t *testing.T) {
				h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{})
				h.optIn("wilant", "wilant@example.com")
				h.at(c.hour, c.min)
				if !h.srv.RunMetricsSyncIfMissed(t.Context()) {
					t.Fatal("the missed 06:30 pass did not run")
				}
				if got := len(h.notif.mails()) == 1; got != c.wantMailed {
					t.Fatalf("mailed = %v, want %v", got, c.wantMailed)
				}
				if !c.wantMailed && h.lastSent("wilant") != "" {
					t.Error("a skipped day was still claimed")
				}
			})
		}
	}
}

func TestOneFailureNeitherStopsTheOthersNorIsRetried(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{})
			h.optIn("alice", "alice.private@example.com")
			h.optIn("bob", "bob.private@example.com")
			h.notif.fail = func(to string) error {
				if to == "alice.private@example.com" {
					return &mailer.SendError{Stage: "rcpt", Code: 550}
				}
				return nil
			}
			before := metricCount(t, h.base, "failed")
			sentBefore := metricCount(t, h.base, "sent")

			h.at(7, 0)
			if !h.srv.RunMetricsSyncIfMissed(t.Context()) {
				t.Fatal("the pass did not run")
			}
			mails := h.notif.mails()
			if len(mails) != 1 || mails[0].to != "bob.private@example.com" {
				t.Fatalf("mails = %+v, want bob's only", mails)
			}
			if got := metricCount(t, h.base, "failed") - before; got != 1 {
				t.Errorf("failed counter rose by %d, want 1", got)
			}
			if got := metricCount(t, h.base, "sent") - sentBefore; got != 1 {
				t.Errorf("sent counter rose by %d, want 1", got)
			}

			logs := h.logs.String()
			if !strings.Contains(logs, "morning summary: send failed") || !strings.Contains(logs, "rcpt") || !strings.Contains(logs, "550") {
				t.Errorf("a failure should be one Warn with stage and code:\n%s", logs)
			}
			for _, banned := range []string{"private@example.com", "Readiness", "Take it easy", "Rest today", "Rest day"} {
				if strings.Contains(logs, banned) {
					t.Errorf("%q reached the logs:\n%s", banned, logs)
				}
			}

			// No retry: another pass the same day leaves alice alone, and the
			// sync was recorded regardless of her failure.
			h.at(7, 30)
			h.srv.SyncTrainingMetrics(t.Context())
			if n := h.notif.callsTo("alice.private@example.com"); n != 1 {
				t.Errorf("alice was attempted %d times, want exactly 1", n)
			}
			h.at(8, 0)
			if h.srv.RunMetricsSyncIfMissed(t.Context()) {
				t.Error("the sync was not recorded as done")
			}
		})
	}
}

func TestOptedInRidersWithNoMailerGetOneWarnPerPass(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{})
			h.optIn("alice", "alice.private@example.com")
			h.optIn("bob", "bob.private@example.com")
			h.srv.Mailer = nil

			h.at(7, 0)
			h.srv.RunMetricsSyncIfMissed(t.Context())
			logs := h.logs.String()
			if n := strings.Count(logs, "morning summary: not configured"); n != 1 {
				t.Fatalf("%d warnings, want exactly one per pass:\n%s", n, logs)
			}
			if !strings.Contains(logs, "riders=2") {
				t.Errorf("the Warn should count the riders it could not serve:\n%s", logs)
			}
			assertNoAddress(t, logs)
			if h.lastSent("alice") != "" {
				t.Error("a day was claimed with nothing able to send")
			}
		})
	}
}

// Nothing the rider's health data says may land next to their name in a log.
func TestPassLogsNoVerdictOrReasonNextToARider(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{})
			h.optIn("alice", "alice.private@example.com")
			if err := h.training.SaveWellness(t.Context(), workout.DailyWellness{
				Rider: "alice", Date: mailDay, SleepSeconds: 6*3600 + 10*60, SleepScore: 31, HRVStatus: "low", RestingHR: 71,
			}); err != nil {
				t.Fatal(err)
			}
			h.at(7, 0)
			h.srv.RunMetricsSyncIfMissed(t.Context())
			mails := h.notif.mails()
			if len(mails) != 1 || !strings.Contains(mails[0].body, "Readiness: Rest today") {
				t.Fatalf("mails = %+v; the verdict should be in the mail", mails)
			}
			for _, banned := range []string{"sleep", "6h10", "score", "HRV", "resting"} {
				if strings.Contains(strings.ToLower(mails[0].body), strings.ToLower(banned)) {
					t.Errorf("%q is in the email body:\n%s", banned, mails[0].body)
				}
			}
			logs := h.logs.String()
			for _, banned := range []string{"Rest today", "sleep score", "6h10", "private@example.com"} {
				if strings.Contains(logs, banned) {
					t.Errorf("%q reached the logs:\n%s", banned, logs)
				}
			}
		})
	}
}

func TestPassMentionsAnAlreadyRiddenSessionAndAnEasedOne(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newMSHarness(t, auth.ModeProxy, dsn, msOpts{})
			h.seedToday("alice", "Threshold 3 x 10")
			h.optIn("alice", "alice@example.com")
			if _, err := h.training.UpsertSession(t.Context(), workout.UpsertSessionRequest{
				Rider: "alice", Provider: "garmin", ExternalID: "x1", Sport: "cycling", Date: mailDay, DurationSeconds: 3000,
			}); err != nil {
				t.Fatal(err)
			}
			h.at(7, 0)
			h.srv.RunMetricsSyncIfMissed(t.Context())
			mails := h.notif.mails()
			if len(mails) != 1 || !strings.Contains(mails[0].body, "Already ridden") {
				t.Fatalf("mails = %+v", mails)
			}
		})
	}
}

// The metrics counter is process-wide, so tests compare before and after.
var metricLine = regexp.MustCompile(`(?m)^domestique_morning_summary_total\{[^}]*result="(\w+)"[^}]*\} (\d+(?:\.\d+)?)$`)

func metricCount(t *testing.T, base, result string) int {
	t.Helper()
	resp, err := http.Get(base + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	for _, m := range metricLine.FindAllStringSubmatch(string(b), -1) {
		if m[1] == result {
			f, _ := strconv.ParseFloat(m[2], 64)
			return int(f)
		}
	}
	return 0
}

var _ = fmt.Sprint
