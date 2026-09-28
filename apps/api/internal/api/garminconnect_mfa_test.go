package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/garminmfa"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
)

const (
	mfaGoodCode  = "918273"
	mfaBadCode   = "000000"
	mfaCookieVal = "cookie-secret-xyz"
	mfaCSRFVal   = "csrf-secret-xyz"
	mfaPassword  = "hunter2-mfa"
	mfaPath      = "/api/garmin/connection/mfa"
)

func mfaChallengeError() error {
	return &api.GarminMFAChallengeError{Challenge: garmin.MFAChallenge{
		Cookies: map[string][]*http.Cookie{
			"https://sso.garmin.com/sso": {{Name: "GARMIN-SSO", Value: mfaCookieVal}},
		},
		CSRF:   mfaCSRFVal,
		Email:  "rider@example.com",
		Method: "email",
	}}
}

// startMFA runs step one as rider and returns the challenge id.
func startMFA(t *testing.T, h *connectHarness, rider string) string {
	t.Helper()
	h.garmin.err = mfaChallengeError()
	h.garmin.wantCode = mfaGoodCode

	resp := h.as(rider, "cyclists", http.MethodPost, "/api/garmin/connection",
		fmt.Sprintf(`{"email":"rider@example.com","password":%q}`, mfaPassword))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("step one status = %d, want 409", resp.StatusCode)
	}
	body := decodeConnection(t, resp)
	id, _ := body["challenge"].(string)
	if id == "" {
		t.Fatalf("step one body = %v, want a challenge id", body)
	}
	return id
}

func codeBody(challenge, code string) string {
	return fmt.Sprintf(`{"challenge":%q,"code":%q}`, challenge, code)
}

func TestGarminMFAStepOneReturnsAChallenge(t *testing.T) {
	h := newConnectHarness(t, true)
	h.garmin.err = mfaChallengeError()

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
		fmt.Sprintf(`{"email":"rider@example.com","password":%q}`, mfaPassword))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	body := decodeConnection(t, resp)
	if body["mfa"] != true || body["method"] != "email" {
		t.Errorf("body = %v, want mfa: true and method: email", body)
	}
	if id, _ := body["challenge"].(string); id == "" {
		t.Errorf("body = %v, want a challenge id", body)
	}
	raw, _ := json.Marshal(body)
	for _, secret := range []string{mfaPassword, mfaCookieVal, mfaCSRFVal} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the response leaks %q", secret)
		}
	}
	if _, err := h.links.Get("garmin", "wilant"); err == nil {
		t.Error("a connection was stored before the code was given")
	}
}

// Without state to resume from there is nothing to offer a code field for, so
// the answer is today's bare 409.
func TestGarminMFAWithoutAChallengeIsTheBareConflict(t *testing.T) {
	for name, opt := range map[string]func(*api.Server){
		"connector returned no state": nil,
		"no challenge store":          func(s *api.Server) { s.GarminMFA = nil },
	} {
		t.Run(name, func(t *testing.T) {
			var opts []func(*api.Server)
			if opt != nil {
				opts = append(opts, opt)
			}
			h := newConnectHarness(t, true, opts...)
			h.garmin.err = garmin.ErrMFARequired
			if name == "no challenge store" {
				h.garmin.err = mfaChallengeError()
			}

			resp := h.as("wilant", "cyclists", http.MethodPost, "/api/garmin/connection",
				`{"email":"r@example.com","password":"pw"}`)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", resp.StatusCode)
			}
			body := decodeConnection(t, resp)
			if body["mfa"] != true {
				t.Errorf("body = %v, want mfa: true", body)
			}
			if _, has := body["challenge"]; has {
				t.Errorf("body = %v, want no challenge", body)
			}
		})
	}
}

func TestGarminMFACompletesTheSignIn(t *testing.T) {
	h := newConnectHarness(t, true)
	id := startMFA(t, h, "friend")

	resp := h.as("friend", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeConnection(t, resp)
	if body["connected"] != true || body["displayName"] != "Wilant N" || body["email"] != "rider@example.com" {
		t.Errorf("body = %v, want a connected account from the challenge's email", body)
	}

	// What Garmin was handed: the code and the state Login returned.
	if h.garmin.resumedCode != mfaGoodCode || h.garmin.resumedChallenge.CSRF != mfaCSRFVal {
		t.Errorf("connector saw code %q, challenge %+v", h.garmin.resumedCode, h.garmin.resumedChallenge)
	}
	cookies := h.garmin.resumedChallenge.Cookies["https://sso.garmin.com/sso"]
	if len(cookies) != 1 || cookies[0].Value != mfaCookieVal {
		t.Errorf("cookies did not survive the sealed round trip: %+v", h.garmin.resumedChallenge.Cookies)
	}

	// Stored exactly like step one's success path.
	_, secret, err := h.links.Secret("garmin", "friend")
	if err != nil {
		t.Fatal(err)
	}
	var session garmin.Session
	if err := json.Unmarshal([]byte(secret), &session); err != nil {
		t.Fatal(err)
	}
	if session.OAuth1Token != "garmin-token-mfa" {
		t.Errorf("stored %+v, want the token pair Garmin returned", session)
	}
	if _, err := h.accounts.Get(t.Context(), accounts.ID(model.ProviderGarmin, "friend")); err != nil {
		t.Errorf("no head unit was linked: %v", err)
	}

	// Single-use: the same challenge cannot be replayed.
	replay := h.as("friend", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode))
	if replay.StatusCode != http.StatusNotFound {
		t.Errorf("replay status = %d, want 404", replay.StatusCode)
	}
	if h.garmin.resumeCalls != 1 {
		t.Errorf("Garmin was asked %d times, want 1", h.garmin.resumeCalls)
	}
}

func TestGarminMFAWrongCodeKeepsTheChallengeOpen(t *testing.T) {
	h := newConnectHarness(t, true)
	id := startMFA(t, h, "wilant")

	resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaBadCode))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	body := decodeConnection(t, resp)
	if body["mfaInvalid"] != true || body["challenge"] != id {
		t.Errorf("body = %v, want mfaInvalid and the same challenge id", body)
	}
	if body["attemptsRemaining"] != float64(garminmfa.MaxAttempts-1) {
		t.Errorf("attemptsRemaining = %v, want %d", body["attemptsRemaining"], garminmfa.MaxAttempts-1)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "didn't work") {
		t.Errorf("error = %q", msg)
	}
	if _, err := h.links.Get("garmin", "wilant"); err == nil {
		t.Error("a wrong code stored a connection")
	}

	// The same challenge still works with the right code.
	ok := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode))
	if ok.StatusCode != http.StatusOK {
		t.Errorf("right code after a wrong one: status = %d, want 200", ok.StatusCode)
	}
}

func TestGarminMFAFifthWrongCodeEndsTheChallenge(t *testing.T) {
	h := newConnectHarness(t, true)
	id := startMFA(t, h, "wilant")

	for i := 1; i < garminmfa.MaxAttempts; i++ {
		resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaBadCode))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d: status = %d, want 422", i, resp.StatusCode)
		}
	}
	resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaBadCode))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("fifth wrong code: status = %d, want 409", resp.StatusCode)
	}
	if msg, _ := decodeConnection(t, resp)["error"].(string); !strings.Contains(msg, "too many wrong codes") {
		t.Errorf("error = %q, want it to say too many wrong codes", msg)
	}

	// Gone: even the right code is now a 404, and Garmin is not asked.
	calls := h.garmin.resumeCalls
	after := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode))
	if after.StatusCode != http.StatusNotFound {
		t.Errorf("after exhaustion: status = %d, want 404", after.StatusCode)
	}
	if h.garmin.resumeCalls != calls {
		t.Error("Garmin was asked about an exhausted challenge")
	}
}

// An unexpected Garmin page is our gap, not the rider's mistake.
func TestGarminMFAUnexpectedPageIsBadGatewayAndCostsNoAttempt(t *testing.T) {
	h := newConnectHarness(t, true)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
	id := startMFA(t, h, "wilant")

	h.garmin.resumeErr = errors.New(`garmin: the two-factor code was answered with an unrecognised page (status 200, title="Scheduled maintenance" bytes=41)`)
	for range garminmfa.MaxAttempts + 2 {
		resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaBadCode))
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", resp.StatusCode)
		}
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "Scheduled maintenance") {
		t.Errorf("log = %q, want a Warn carrying the fingerprint", logs.String())
	}

	// Every one of those would have exhausted the challenge had it counted.
	h.garmin.resumeErr = nil
	resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaBadCode))
	body := decodeConnection(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || body["attemptsRemaining"] != float64(garminmfa.MaxAttempts-1) {
		t.Errorf("status = %d body = %v: unexpected pages consumed attempts", resp.StatusCode, body)
	}
}

func TestGarminMFABlockedIsServiceUnavailableAndCostsNoAttempt(t *testing.T) {
	h := newConnectHarness(t, true)
	id := startMFA(t, h, "wilant")

	h.garmin.resumeErr = garmin.ErrBlocked
	resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if decodeConnection(t, resp)["blocked"] != true {
		t.Error("the blocked flag is missing")
	}

	h.garmin.resumeErr = nil
	if ok := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode)); ok.StatusCode != http.StatusOK {
		t.Errorf("after a block: status = %d, want 200", ok.StatusCode)
	}
}

func TestGarminMFAExpiredChallengeIsConflict(t *testing.T) {
	h := newConnectHarness(t, true)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	h.mfa.Now = func() time.Time { return now }
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
	id := startMFA(t, h, "wilant")

	now = now.Add(garminmfa.DefaultTTL + time.Second)
	resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if msg, _ := decodeConnection(t, resp)["error"].(string); !strings.Contains(msg, "expired") {
		t.Errorf("error = %q, want it to say expired", msg)
	}
	if h.garmin.resumeCalls != 0 {
		t.Error("Garmin was asked to verify against an expired challenge")
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("log = %q, want expiry at Warn", logs.String())
	}
}

func TestGarminMFAChallengeBelongsToItsRider(t *testing.T) {
	h := newConnectHarness(t, true)
	id := startMFA(t, h, "wilant")

	resp := h.as("friend", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another rider: status = %d, want 404", resp.StatusCode)
	}
	if h.garmin.resumeCalls != 0 {
		t.Error("Garmin was asked to verify another rider's challenge")
	}
	if _, err := h.links.Get("garmin", "friend"); err == nil {
		t.Error("a connection was stored for the stranger")
	}

	// And the stranger's attempt did not spend the owner's.
	if ok := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode)); ok.StatusCode != http.StatusOK {
		t.Errorf("owner: status = %d, want 200", ok.StatusCode)
	}

	// Unknown ids are the same 404.
	if r := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody("nope", mfaGoodCode)); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id: status = %d, want 404", r.StatusCode)
	}
}

// The rider is the session's, whatever the body says.
func TestGarminMFAIgnoresARiderInTheBody(t *testing.T) {
	h := newConnectHarness(t, true)
	id := startMFA(t, h, "wilant")

	body := fmt.Sprintf(`{"challenge":%q,"code":%q,"rider":"wilant","user":"wilant"}`, id, mfaGoodCode)
	if resp := h.as("friend", "cyclists", http.MethodPost, mfaPath, body); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404: the body named the owner", resp.StatusCode)
	}
}

func TestGarminMFARequestValidation(t *testing.T) {
	h := newConnectHarness(t, true)
	id := startMFA(t, h, "wilant")

	for name, body := range map[string]string{
		"not json":     `nope`,
		"no code":      codeBody(id, ""),
		"blank code":   codeBody(id, "   "),
		"no challenge": codeBody("", mfaGoodCode),
	} {
		if resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
	if h.garmin.resumeCalls != 0 {
		t.Error("Garmin was asked about an invalid request")
	}
}

func TestGarminMFANeedsAccountPermission(t *testing.T) {
	h := newConnectHarness(t, true)
	id := startMFA(t, h, "wilant")

	if resp := h.as("guest", "guests", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode)); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer status = %d, want 403", resp.StatusCode)
	}
}

func TestGarminMFAIsRefusedWithoutAStore(t *testing.T) {
	h := newConnectHarness(t, true, func(s *api.Server) { s.GarminMFA = nil })
	resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody("x", mfaGoodCode))
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", resp.StatusCode)
	}
	if h.garmin.resumeCalls != 0 {
		t.Error("Garmin was contacted without anywhere to keep the result")
	}
}

// One budget for the sign-in and the code, so the second step is not a way
// round the limit on the first.
func TestGarminMFASharesTheConnectRateLimit(t *testing.T) {
	h := newConnectHarness(t, true, func(s *api.Server) {
		s.ConnectLimiter = ratelimit.New(2, time.Hour)
	})
	id := startMFA(t, h, "wilant") // spends 1

	if resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaBadCode)); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second request status = %d, want 422", resp.StatusCode)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode)); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("third request status = %d, want 429", resp.StatusCode)
	}
}

// Nothing the rider typed, and nothing sealed, may reach a log line.
func TestGarminMFALogsNeverContainSecrets(t *testing.T) {
	h := newConnectHarness(t, true)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	id := startMFA(t, h, "wilant")
	h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaBadCode))
	h.garmin.resumeErr = errors.New("garmin: unrecognised page")
	h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaBadCode))
	h.garmin.resumeErr = nil
	h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode))
	h.as("wilant", "cyclists", http.MethodPost, mfaPath, codeBody(id, mfaGoodCode)) // replay

	out := logs.String()
	if out == "" {
		t.Fatal("nothing was logged at all; the assertion below would be vacuous")
	}
	for _, secret := range []string{mfaGoodCode, mfaBadCode, mfaCookieVal, mfaCSRFVal, mfaPassword, id} {
		if strings.Contains(out, secret) {
			t.Errorf("a log line contains %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "level=INFO") {
		t.Errorf("no Info line for the challenge being created:\n%s", out)
	}
}
