// Acceptance tests for the Apple Health post: the second endpoint that bypasses
// authentication, with a hashed bearer token as its only credential. Checked
// over real HTTP, in every auth mode.
package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/auth"
)

const (
	applePath = "/api/wellness/apple"
	// One night of readings for 2026-10-10 (the harness clock's local day).
	appleNight = `{"hrv":[{"start":"2026-10-10T03:00:00+02:00","value":48}],` +
		`"restingHr":[],"sleep":[{"start":"2026-10-09T23:00:00+02:00","end":"2026-10-10T06:00:00+02:00","value":"Core"}]}`
)

// linkApple gives rider a shortcut token and returns it.
func (h *calHarness) linkApple(rider string) string {
	h.t.Helper()
	tok, err := h.apple.Regenerate(h.t.Context(), rider)
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// postApple posts body with the given Authorization header ("" sends none).
func (h *calHarness) postApple(authz, body string, mutate ...func(*http.Request)) calResp {
	h.t.Helper()
	return h.do(http.MethodPost, applePath, func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", "application/json")
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		for _, m := range mutate {
			m(r)
		}
	})
}

func eachAppleSetup(t *testing.T, run func(t *testing.T, h *calHarness)) { eachCalendarSetup(t, run) }

func TestAppleHealthPostWritesTheRidersDays(t *testing.T) {
	eachAppleSetup(t, func(t *testing.T, h *calHarness) {
		tok := h.linkApple("wilant")
		r := h.postApple("Bearer "+tok, appleNight)
		if r.code != http.StatusOK || r.body != `{"days":1}`+"\n" {
			t.Fatalf("post = %d %q", r.code, r.body)
		}
		rows, err := h.training.ListWellness(t.Context(), "wilant", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %+v", rows)
		}
		w := rows[0]
		if w.Source != "apple" || w.Date != "2026-10-10" || w.HRVLastNight != 48 || w.SleepSeconds != 7*3600 {
			t.Errorf("row = %+v", w)
		}
		if st, err := h.apple.Status(t.Context(), "wilant"); err != nil || st.LastReceivedAt == "" {
			t.Errorf("the post was not recorded on the link: %+v %v", st, err)
		}
	})
}

func TestAppleHealthResponseHoldsNoHealthValue(t *testing.T) {
	eachAppleSetup(t, func(t *testing.T, h *calHarness) {
		r := h.postApple("Bearer "+h.linkApple("wilant"), appleNight)
		if strings.TrimSpace(r.body) != `{"days":1}` {
			t.Fatalf("body = %q", r.body)
		}
	})
}

func TestAnEmptyPostIsZeroDays(t *testing.T) {
	eachAppleSetup(t, func(t *testing.T, h *calHarness) {
		r := h.postApple("Bearer "+h.linkApple("wilant"), `{"hrv":[],"restingHr":[],"sleep":[]}`)
		if r.code != http.StatusOK || strings.TrimSpace(r.body) != `{"days":0}` {
			t.Fatalf("post = %d %q", r.code, r.body)
		}
	})
}

func TestAppleHealthUnauthorizedIsOneAnswer(t *testing.T) {
	eachAppleSetup(t, func(t *testing.T, h *calHarness) {
		revoked := h.linkApple("bob")
		if err := h.apple.Revoke(t.Context(), "bob"); err != nil {
			t.Fatal(err)
		}
		unknown := strings.Repeat("a", 43)
		var want string
		for i, authz := range []string{"", "Bearer x", "Bearer " + unknown, "Bearer " + revoked, "bearer " + unknown, "Basic " + unknown} {
			r := h.postApple(authz, appleNight)
			if r.code != http.StatusUnauthorized {
				t.Fatalf("%q = %d, want 401", authz, r.code)
			}
			if i == 0 {
				want = r.body
			}
			if r.body != want || want != `{"error":"unauthorized"}`+"\n" {
				t.Errorf("%q body = %q, want one identical %q", authz, r.body, want)
			}
		}
	})
}

func TestAppleHealthIgnoresForgedIdentity(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		for _, mode := range []auth.Mode{auth.ModeProxy, auth.ModeOIDC} {
			t.Run(engine+"/"+string(mode), func(t *testing.T) {
				h := newCalHarness(t, mode, dsn, calendarPublicURL)
				h.linkApple("wilant")
				r := h.postApple("", appleNight, func(req *http.Request) { h.identify(req, "wilant", "cyclists") })
				if r.code != http.StatusUnauthorized || r.body != `{"error":"unauthorized"}`+"\n" {
					t.Fatalf("a signed-in request with no token = %d %q, want the handler's 401", r.code, r.body)
				}
			})
		}
	}
}

func TestAppleHealthTokenInTheQueryIsNotATokenAndIsNotLogged(t *testing.T) {
	eachAppleSetup(t, func(t *testing.T, h *calHarness) {
		tok := h.linkApple("wilant")
		r := h.do(http.MethodPost, applePath+"?token="+tok, func(req *http.Request) {
			req.Body = io.NopCloser(strings.NewReader(appleNight))
			req.ContentLength = int64(len(appleNight))
			req.Header.Set("Content-Type", "application/json")
		})
		if r.code != http.StatusUnauthorized {
			t.Fatalf("token in the query = %d, want 401", r.code)
		}
		// And a correct post leaves nothing of it in the log either.
		h.postApple("Bearer "+tok, appleNight)
		if strings.Contains(h.logs.String(), tok) {
			t.Error("the token reached the log")
		}
		for _, v := range []string{"48", "25200", "2026-10-10T03"} {
			if strings.Contains(h.logs.String(), "="+v) || strings.Contains(h.logs.String(), v+"+02") {
				t.Errorf("a health value or sample time (%s) reached the log", v)
			}
		}
	})
}

func TestOnlyPostBypassesTheGate(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		for _, mode := range []auth.Mode{auth.ModeProxy, auth.ModeOIDC} {
			t.Run(engine+"/"+string(mode), func(t *testing.T) {
				h := newCalHarness(t, mode, dsn, calendarPublicURL)
				tok := h.linkApple("wilant")
				for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPatch} {
					r := h.do(m, applePath, func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+tok) })
					// The gate's own 401 body is not the handler's.
					if r.body == `{"error":"unauthorized"}`+"\n" {
						t.Errorf("%s reached the handler: %d %q", m, r.code, r.body)
					}
					if r.code != http.StatusUnauthorized {
						t.Errorf("%s with a valid token and no session = %d, want the gate's 401", m, r.code)
					}
				}
				// Nothing else under the prefix is exempt.
				for _, p := range []string{applePath + "/", "/api/wellness", "/api/wellness/apple2"} {
					r := h.do(http.MethodPost, p, func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+tok) })
					if r.code != http.StatusUnauthorized {
						t.Errorf("POST %s = %d, want the gate's 401", p, r.code)
					}
				}
			})
		}
	}
}

func TestAppleHealthRateLimit(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newCalHarness(t, auth.ModeProxy, dsn, calendarPublicURL)
			tok := h.linkApple("wilant")
			for i := 1; i <= 12; i++ {
				if r := h.postApple("Bearer "+tok, appleNight); r.code != http.StatusOK {
					t.Fatalf("post %d = %d", i, r.code)
				}
			}
			r := h.postApple("Bearer "+tok, appleNight)
			if r.code != http.StatusTooManyRequests {
				t.Fatalf("13th post = %d, want 429", r.code)
			}
			// Another rider's token has its own budget.
			if r := h.postApple("Bearer "+h.linkApple("bob"), appleNight); r.code != http.StatusOK {
				t.Errorf("another token was limited: %d", r.code)
			}
		})
	}
}

func TestAppleHealthInvalidPostsCountAgainstTheTokenBudget(t *testing.T) {
	h := newCalHarness(t, auth.ModeProxy, "", calendarPublicURL)
	tok := h.linkApple("wilant")
	for i := 1; i <= 12; i++ {
		if r := h.postApple("Bearer "+tok, `not json`); r.code != http.StatusBadRequest {
			t.Fatalf("bad post %d = %d, want 400", i, r.code)
		}
	}
	if r := h.postApple("Bearer "+tok, appleNight); r.code != http.StatusTooManyRequests {
		t.Fatalf("a good post after 12 bad ones = %d, want 429", r.code)
	}
}

func TestAppleHealthMissesShareOneGlobalBucket(t *testing.T) {
	h := newCalHarness(t, auth.ModeProxy, "", calendarPublicURL)
	tok := h.linkApple("wilant")
	unknown := "Bearer " + strings.Repeat("b", 43)
	for i := 1; i <= 60; i++ {
		if r := h.postApple(unknown, appleNight); r.code != http.StatusUnauthorized {
			t.Fatalf("miss %d = %d, want 401", i, r.code)
		}
	}
	if r := h.postApple(unknown, appleNight); r.code != http.StatusTooManyRequests {
		t.Fatalf("61st miss = %d, want 429", r.code)
	}
	// A real token is not a miss, so the flood does not lock it out.
	if r := h.postApple("Bearer "+tok, appleNight); r.code != http.StatusOK {
		t.Fatalf("a real token after a flood = %d", r.code)
	}
}

func TestAppleHealthCaps(t *testing.T) {
	eachAppleSetup(t, func(t *testing.T, h *calHarness) {
		tok := h.linkApple("wilant")
		bearer := "Bearer " + tok

		big := `{"hrv":[]}` + strings.Repeat(" ", 300<<10)
		if r := h.postApple(bearer, big); r.code != http.StatusRequestEntityTooLarge {
			t.Errorf("300 KiB body = %d, want 413", r.code)
		}

		var b strings.Builder
		b.WriteString(`{"hrv":[`)
		for i := 0; i < 2001; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"start":"2026-10-10T03:00:00+02:00","value":48}`)
		}
		b.WriteString(`]}`)
		if b.Len() >= 256<<10 {
			t.Fatalf("test body is %d bytes: it must test the sample cap, not the byte cap", b.Len())
		}
		if r := h.postApple(bearer, b.String()); r.code != http.StatusRequestEntityTooLarge {
			t.Errorf("2001 samples = %d, want 413", r.code)
		}

		r := h.postApple(bearer, appleNight, func(req *http.Request) { req.Header.Set("Content-Type", "text/plain") })
		if r.code != http.StatusUnsupportedMediaType {
			t.Errorf("text/plain = %d, want 415 from csrfGuard", r.code)
		}
	})
}

func TestBlockingAPersonRemovesTheirAppleHealthLink(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		t.Run(engine, func(t *testing.T) {
			h := newAccessHarness(t, dsn)
			alice, bob := h.linkApple("alice"), h.linkApple("bob")
			if r := h.postApple("Bearer "+alice, appleNight); r.code != http.StatusOK {
				t.Fatalf("setup post = %d", r.code)
			}
			if code := h.admin(http.MethodPut, personPath("auth0|alice", "/blocked"), `{"blocked":true,"email":"alice@example.com"}`); code != http.StatusOK {
				t.Fatalf("block = %d", code)
			}
			if r := h.postApple("Bearer "+alice, appleNight); r.code != http.StatusUnauthorized {
				t.Errorf("a blocked rider's token = %d, want 401", r.code)
			}
			if r := h.postApple("Bearer "+bob, appleNight); r.code != http.StatusOK {
				t.Errorf("another rider lost their link: %d", r.code)
			}
		})
	}
}

// appleCard is the card's JSON, as the Fitness page reads it.
type appleCard struct {
	Linked       bool   `json:"linked"`
	LastReceived string `json:"lastReceived"`
	ShortcutURL  string `json:"shortcutUrl"`
	Token        string `json:"token"`
}

func (h *calHarness) appleCard(method string) (calResp, appleCard) {
	h.t.Helper()
	r := h.as("wilant", method, "/api/training/apple-health")
	var c appleCard
	if method != http.MethodDelete && r.code == http.StatusOK {
		if err := json.Unmarshal([]byte(r.body), &c); err != nil {
			h.t.Fatalf("card JSON %q: %v", r.body, err)
		}
	}
	return r, c
}

func TestAppleHealthCardLifecycle(t *testing.T) {
	eachAppleSetup(t, func(t *testing.T, h *calHarness) {
		h.srv.Config.AppleHealth.ShortcutURL = "https://www.icloud.com/shortcuts/abc"

		if _, c := h.appleCard(http.MethodGet); c.Linked || c.Token != "" || c.LastReceived != "" {
			t.Fatalf("a fresh rider: %+v", c)
		}

		r := h.as("wilant", http.MethodPost, "/api/training/apple-health")
		var created appleCard
		if err := json.Unmarshal([]byte(r.body), &created); err != nil || r.code != http.StatusOK {
			t.Fatalf("POST = %d %s", r.code, r.body)
		}
		if len(created.Token) != 43 {
			t.Fatalf("token %q is %d characters, want 43", created.Token, len(created.Token))
		}
		if created.ShortcutURL != "https://www.icloud.com/shortcuts/abc" {
			t.Errorf("shortcutUrl = %q", created.ShortcutURL)
		}

		_, c := h.appleCard(http.MethodGet)
		if !c.Linked || c.Token != "" || strings.Contains(h.as("wilant", http.MethodGet, "/api/training/apple-health").body, created.Token) {
			t.Fatalf("after generating, the card must show linked and never the token: %+v", c)
		}

		if p := h.postApple("Bearer "+created.Token, `{"hrv":[],"restingHr":[],"sleep":[]}`); p.code != http.StatusOK {
			t.Fatalf("post = %d %s", p.code, p.body)
		}
		if _, c := h.appleCard(http.MethodGet); c.LastReceived != "2026-10-10" {
			t.Errorf("lastReceived = %q, want the training-zone date 2026-10-10", c.LastReceived)
		}

		if d := h.as("wilant", http.MethodDelete, "/api/training/apple-health"); d.code != http.StatusNoContent {
			t.Fatalf("DELETE = %d", d.code)
		}
		if p := h.postApple("Bearer "+created.Token, `{"hrv":[],"restingHr":[],"sleep":[]}`); p.code != http.StatusUnauthorized {
			t.Errorf("a revoked token = %d, want 401", p.code)
		}
	})
}

// Mode none makes everyone a local admin, so the role and anonymous checks run
// only where a session or a header says who is asking.
func TestAViewerCannotLinkAppleHealth(t *testing.T) {
	for engine, dsn := range calendarEngines(t) {
		for _, mode := range []auth.Mode{auth.ModeProxy, auth.ModeOIDC} {
			t.Run(engine+"/"+string(mode), func(t *testing.T) {
				h := newCalHarness(t, mode, dsn, calendarPublicURL)
				for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
					r := h.do(m, "/api/training/apple-health", func(r *http.Request) { h.identify(r, "viewer", "guests") })
					if r.code != http.StatusForbidden {
						t.Errorf("viewer %s = %d, want 403", m, r.code)
					}
				}
				if r := h.do(http.MethodPost, "/api/training/apple-health", nil); r.code != http.StatusUnauthorized {
					t.Errorf("anonymous POST = %d, want 401", r.code)
				}
			})
		}
	}
}

func TestAppleHealthCardIs412WithoutAStore(t *testing.T) {
	eachAppleSetup(t, func(t *testing.T, h *calHarness) {
		h.srv.AppleHealth = nil
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			if r := h.as("wilant", m, "/api/training/apple-health"); r.code != http.StatusPreconditionFailed {
				t.Errorf("%s = %d, want 412", m, r.code)
			}
		}
		if !strings.Contains(h.logs.String(), "level=WARN") {
			t.Errorf("the 412 left no Warn:\n%s", h.logs.String())
		}
	})
}
