package garmin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// mfaLogin runs the first half against the fake and returns the challenge.
func mfaLogin(t *testing.T, c *Client) MFAChallenge {
	t.Helper()
	ch, err := c.Login(t.Context(), testEmail, testPassword)
	if !errors.Is(err, ErrMFARequired) {
		t.Fatalf("Login error = %v, want ErrMFARequired", err)
	}
	return ch
}

// freshClient is a second Client aimed at the same fake, standing in for the
// other process (or pod) that resumes the login.
func freshClient(c *Client) *Client {
	other := New()
	other.SSOBase, other.APIBase, other.WebBase = c.SSOBase, c.APIBase, c.WebBase
	other.SetConsumer(testKey, testSecret)
	return other
}

// through marshals and unmarshals, as the sealed store does between requests.
func through(t *testing.T, ch MFAChallenge) MFAChallenge {
	t.Helper()
	raw, err := json.Marshal(ch)
	if err != nil {
		t.Fatal(err)
	}
	var out MFAChallenge
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLoginReturnsAResumableChallenge(t *testing.T) {
	c, fake := newFakeConnect(t)
	fake.mfa, fake.mfaMethod = true, "email"

	ch := mfaLogin(t, c)

	if ch.CSRF != testMFACSRF {
		t.Errorf("challenge CSRF = %q, want the challenge page's own %q, not the sign-in page's", ch.CSRF, testMFACSRF)
	}
	if ch.Email != testEmail {
		t.Errorf("challenge email = %q, want %q", ch.Email, testEmail)
	}
	if ch.Method != "email" {
		t.Errorf("challenge method = %q, want email", ch.Method)
	}
	if len(ch.Cookies) == 0 {
		t.Error("challenge carries no cookies; a resumed login would have no session")
	}
}

func TestChallengeMethods(t *testing.T) {
	for raw, want := range map[string]string{
		"email": "email", "EMAIL": "email", "sms": "sms", "phone": "sms",
		"totp": "totp", "authenticator": "totp", "something-new": "",
	} {
		c, fake := newFakeConnect(t)
		fake.mfa, fake.mfaMethod = true, raw
		if got := mfaLogin(t, c).Method; got != want {
			t.Errorf("mfaMethod %q -> method %q, want %q", raw, got, want)
		}
	}
}

// The password is used once and gone. Nothing serialised for storage may
// contain it.
func TestChallengeNeverContainsThePassword(t *testing.T) {
	c, fake := newFakeConnect(t)
	fake.mfa = true
	raw, err := json.Marshal(mfaLogin(t, c))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testPassword) {
		t.Errorf("the sealed state would contain the password: %s", raw)
	}
	for _, field := range []string{"password"} {
		if strings.Contains(strings.ToLower(string(raw)), field) {
			t.Errorf("challenge JSON mentions %q: %s", field, raw)
		}
	}
}

func TestLoginWithoutMFAReturnsAZeroChallenge(t *testing.T) {
	c, _ := newFakeConnect(t)
	ch, err := c.Login(t.Context(), testEmail, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if ch.CSRF != "" || ch.Cookies != nil || ch.Email != "" {
		t.Errorf("challenge = %+v, want zero on the non-MFA path", ch)
	}
}

func TestResumeMFACompletesTheSignInFromAnotherClient(t *testing.T) {
	c, fake := newFakeConnect(t)
	fake.mfa = true
	ch := through(t, mfaLogin(t, c))

	other := freshClient(c)
	session, err := other.ResumeMFA(t.Context(), ch, testMFACode)
	if err != nil {
		t.Fatalf("ResumeMFA: %v", err)
	}
	if session.OAuth1Token != "tok-1" || session.OAuth1Secret != "sec-1" {
		t.Errorf("session = %+v, want the OAuth1 pair", session)
	}
	if session.ObtainedAt.IsZero() {
		t.Error("ObtainedAt is zero")
	}
	if fake.oauth1Calls != 1 {
		t.Errorf("oauth1 exchanges = %d, want 1", fake.oauth1Calls)
	}
	if other.Session() != session {
		t.Error("the client does not hold the session it returned")
	}
}

func TestResumeMFASendsWhatGarminExpects(t *testing.T) {
	c, fake := newFakeConnect(t)
	fake.mfa = true
	ch := mfaLogin(t, c)

	if _, err := freshClient(c).ResumeMFA(t.Context(), ch, testMFACode); err != nil {
		t.Fatal(err)
	}

	form := fake.verifyForm
	// Both names, deliberately: which one Garmin reads is unconfirmed.
	if form.Get("mfa-code") != testMFACode || form.Get("mfa-verification-code") != testMFACode {
		t.Errorf("code fields = %q / %q, want the code under both names",
			form.Get("mfa-code"), form.Get("mfa-verification-code"))
	}
	if form.Get("embed") != "true" {
		t.Errorf("embed = %q, want true", form.Get("embed"))
	}
	if form.Get("fromPage") != "setupEnterMfaCode" {
		t.Errorf("fromPage = %q, want setupEnterMfaCode", form.Get("fromPage"))
	}
	if form.Get("_csrf") != testMFACSRF {
		t.Errorf("_csrf = %q, want the challenge page's %q", form.Get("_csrf"), testMFACSRF)
	}
	if form.Get("password") != "" || form.Get("username") != "" {
		t.Error("the verify POST carried credentials; it must not")
	}
	if fake.verifyQuery.Get("service") == "" || fake.verifyQuery.Get("clientId") != "GarminConnect" {
		t.Errorf("verify query = %v, want the sign-in query params", fake.verifyQuery)
	}
	if !strings.Contains(fake.verifyRef, "/sso/signin?") {
		t.Errorf("Referer = %q, want the challenge page's own URL", fake.verifyRef)
	}
}

// Two different re-rendered pages, so no single string is load-bearing.
func TestWrongCodeIsRecognisedByAnyMFAMarker(t *testing.T) {
	for _, page := range []string{"input", "verification-input", "vars"} {
		t.Run(page, func(t *testing.T) {
			c, fake := newFakeConnect(t)
			fake.mfa, fake.wrongCodePage = true, page
			ch := mfaLogin(t, c)

			_, err := freshClient(c).ResumeMFA(t.Context(), ch, "000000")
			if !errors.Is(err, ErrMFACodeRejected) {
				t.Errorf("error = %v, want ErrMFACodeRejected", err)
			}
		})
	}
}

// An unrecognised page is our parsing gap, not the rider's wrong code.
func TestUnrecognisedVerifyPageIsAPlainError(t *testing.T) {
	c, fake := newFakeConnect(t)
	fake.mfa, fake.wrongCodePage = true, "unknown"
	ch := mfaLogin(t, c)

	_, err := freshClient(c).ResumeMFA(t.Context(), ch, "000000")
	if err == nil {
		t.Fatal("no error")
	}
	if errors.Is(err, ErrMFACodeRejected) || errors.Is(err, ErrBlocked) || errors.Is(err, ErrBadCredentials) {
		t.Errorf("error = %v, want a plain error", err)
	}
	if !strings.Contains(err.Error(), "Scheduled maintenance") {
		t.Errorf("error = %q, want the page fingerprint (title)", err)
	}
	if strings.Contains(err.Error(), "000000") {
		t.Errorf("error leaks the code: %q", err)
	}
}

func TestVerifyBlockedAndRateLimited(t *testing.T) {
	t.Run("cloudflare", func(t *testing.T) {
		c, fake := newFakeConnect(t)
		fake.mfa, fake.verifyBlocked = true, true
		ch := mfaLogin(t, c)
		if _, err := freshClient(c).ResumeMFA(t.Context(), ch, testMFACode); !errors.Is(err, ErrBlocked) {
			t.Errorf("error = %v, want ErrBlocked", err)
		}
	})
	t.Run("429", func(t *testing.T) {
		c, fake := newFakeConnect(t)
		fake.mfa, fake.verifyStatus = true, http.StatusTooManyRequests
		ch := mfaLogin(t, c)
		if _, err := freshClient(c).ResumeMFA(t.Context(), ch, testMFACode); !errors.Is(err, ErrBlocked) {
			t.Errorf("error = %v, want ErrBlocked", err)
		}
	})
}

func TestResumeMFARequiresACodeAndACSRF(t *testing.T) {
	c, fake := newFakeConnect(t)
	fake.mfa = true
	ch := mfaLogin(t, c)

	if _, err := freshClient(c).ResumeMFA(t.Context(), ch, ""); err == nil {
		t.Error("an empty code was accepted")
	}
	ch.CSRF = ""
	if _, err := freshClient(c).ResumeMFA(t.Context(), ch, testMFACode); err == nil {
		t.Error("a challenge without a CSRF token was accepted")
	}
	if fake.verifyCalls != 0 {
		t.Errorf("verify was called %d times for input that cannot work", fake.verifyCalls)
	}
}

func TestCookiesRoundTripForEveryConfiguredBase(t *testing.T) {
	a := New()
	a.SSOBase, a.APIBase, a.WebBase = "https://sso.test/sso", "https://api.test", "https://web.test"
	for _, base := range []string{a.SSOBase, a.APIBase, a.WebBase} {
		u, _ := url.Parse(base)
		a.HTTP.Jar.SetCookies(u, []*http.Cookie{{Name: "c-" + u.Host, Value: "v-" + u.Host, Path: "/"}})
	}

	exported := through(t, MFAChallenge{Cookies: a.ExportCookies()}).Cookies

	b := New()
	b.SSOBase, b.APIBase, b.WebBase = a.SSOBase, a.APIBase, a.WebBase
	b.ImportCookies(exported)

	for _, base := range []string{a.SSOBase, a.APIBase, a.WebBase} {
		u, _ := url.Parse(base)
		got := b.HTTP.Jar.Cookies(u)
		if len(got) != 1 || got[0].Name != "c-"+u.Host || got[0].Value != "v-"+u.Host {
			t.Errorf("cookies for %s = %v, want the one exported", base, got)
		}
	}
}

func TestCookiesForOtherHostsNeverLeaveOrEnter(t *testing.T) {
	a := New()
	a.SSOBase, a.APIBase, a.WebBase = "https://sso.test/sso", "https://api.test", "https://web.test"
	foreign, _ := url.Parse("https://evil.example")
	a.HTTP.Jar.SetCookies(foreign, []*http.Cookie{{Name: "tracker", Value: "foreign-secret", Path: "/"}})
	sso, _ := url.Parse(a.SSOBase)
	a.HTTP.Jar.SetCookies(sso, []*http.Cookie{{Name: "ok", Value: "1", Path: "/"}})

	raw, err := json.Marshal(a.ExportCookies())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "foreign-secret") || strings.Contains(string(raw), "evil.example") {
		t.Errorf("export contains a foreign host's cookie: %s", raw)
	}

	// Import drops a smuggled entry for a host that is not one of the bases.
	b := New()
	b.SSOBase, b.APIBase, b.WebBase = a.SSOBase, a.APIBase, a.WebBase
	b.ImportCookies(map[string][]*http.Cookie{
		"https://evil.example": {{Name: "tracker", Value: "planted", Path: "/"}},
		a.SSOBase:              {{Name: "ok", Value: "1", Path: "/"}},
	})
	if got := b.HTTP.Jar.Cookies(foreign); len(got) != 0 {
		t.Errorf("import set a cookie for a foreign host: %v", got)
	}
	if got := b.HTTP.Jar.Cookies(sso); len(got) != 1 {
		t.Errorf("import dropped the legitimate cookie: %v", got)
	}
}
