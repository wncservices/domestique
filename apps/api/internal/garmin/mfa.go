package garmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// MFAChallenge is what has to survive between "Garmin asked for a code" and
// "the rider typed it" — an unbounded human wait, so it crosses two HTTP
// requests and possibly two processes.
//
// Exactly four things, and the password is deliberately not one of them: it
// is used once inside Login and is gone by the time this is returned. Cookies
// are the SSO session the challenge belongs to; CSRF is the token on the
// *challenge page*, which is not the one from the original sign-in page;
// Email is not a credential and is only needed again to name the connection
// once the code is accepted. signinParams is deterministic, so it is
// recomputed rather than kept.
type MFAChallenge struct {
	Cookies map[string][]*http.Cookie `json:"cookies"`
	CSRF    string                    `json:"csrf"`
	Email   string                    `json:"email"`
	Method  string                    `json:"method,omitempty"` // "email" | "sms" | "totp" | ""
}

// ErrMFACodeRejected means Garmin answered the code with the challenge page
// again: the code was wrong (or stale), the sign-in is otherwise fine.
var ErrMFACodeRejected = errors.New("garmin: that two-factor code was not accepted")

var (
	// The widget's script vars name how the code was sent. Read tolerantly:
	// Garmin's page wording has drifted before and this only feeds copy.
	mfaMethodPattern = regexp.MustCompile(`(?i)mfaMethod['"]?\s*[:=]\s*['"]([A-Za-z_ -]+)['"]`)
	// Markers of a page that is still asking for a code. A disjunction on
	// purpose, not one string: see ResumeMFA.
	//
	// An input by that name, not script vars: a page that only mentions the
	// MFA method or customer could be anything, and mistaking it for a wrong
	// code would cost the rider an attempt for our parsing gap.
	mfaStillPattern = regexp.MustCompile(`(?i)<input[^>]+name=["']mfa-(?:verification-)?code["']`)
	mfaAppPattern   = regexp.MustCompile(`(?i)\bapp\b`)
)

// mfaMethodOf maps the page's own word for how the code was sent onto the
// three this package promises, or "" when it says something unexpected.
func mfaMethodOf(page []byte) string {
	m := mfaMethodPattern.FindSubmatch(page)
	if m == nil {
		return ""
	}
	raw := strings.ToLower(string(m[1]))
	switch {
	case strings.Contains(raw, "email"):
		return "email"
	case strings.Contains(raw, "sms"), strings.Contains(raw, "phone"), strings.Contains(raw, "text"):
		return "sms"
	case strings.Contains(raw, "totp"), strings.Contains(raw, "authenticator"), mfaAppPattern.MatchString(raw):
		return "totp"
	}
	return ""
}

// challengeFrom captures the state a resumed login needs from the challenge
// page the credentials POST just returned.
//
// The CSRF is re-read from *that* page. Reusing the sign-in page's token is
// the bug that passes against a fake and fails against the real service.
func (c *Client) challengeFrom(email string, page []byte) MFAChallenge {
	ch := MFAChallenge{
		Cookies: c.ExportCookies(),
		Email:   email,
		Method:  mfaMethodOf(page),
	}
	if m := csrfPattern.FindSubmatch(page); m != nil {
		ch.CSRF = string(m[1])
	}
	return ch
}

// bases are the only hosts this client ever talks to. allowedHost enumerates
// exactly these three, and cookies are scoped to them for the same reason.
func (c *Client) bases() []string { return []string{c.SSOBase, c.APIBase, c.WebBase} }

// ExportCookies returns the cookies the jar would send to each configured
// base, keyed by that base.
//
// Scoped on purpose: nothing is enumerated wholesale, so what gets sealed can
// never hold a cookie for anywhere else.
func (c *Client) ExportCookies() map[string][]*http.Cookie {
	out := map[string][]*http.Cookie{}
	if c.HTTP == nil || c.HTTP.Jar == nil {
		return out
	}
	for _, base := range c.bases() {
		u, err := url.Parse(base)
		if err != nil || u.Host == "" {
			continue
		}
		if cookies := c.HTTP.Jar.Cookies(u); len(cookies) > 0 {
			out[base] = cookies
		}
	}
	return out
}

// ImportCookies is the inverse, into this client's jar. Anything keyed by a
// host that is not one of the three configured bases is dropped: the state
// came out of a database and is not trusted to name where cookies go.
func (c *Client) ImportCookies(cookies map[string][]*http.Cookie) {
	if c.HTTP == nil || c.HTTP.Jar == nil {
		return
	}
	for _, base := range c.bases() {
		u, err := url.Parse(base)
		if err != nil || u.Host == "" {
			continue
		}
		if set := cookies[base]; len(set) > 0 {
			c.HTTP.Jar.SetCookies(u, set)
		}
	}
}

// ResumeMFA answers a challenge with the code and, on success, finishes the
// sign-in exactly as Login would have: the ticket goes through the same
// exchangeTicket.
//
// The receiver should be a fresh Client built with the same bases as the one
// that returned the challenge, with the consumer set (SetConsumer): it is the
// same OAuth1 pair that signs the exchange, so it is not a parameter here.
//
// On ErrMFACodeRejected the returned challenge is the one to keep: the
// re-rendered page carries a fresh CSRF token (Garmin's widget rotates it) and
// the jar has moved on, so the next attempt on the same challenge needs both.
// A page with no token keeps the old one. Every other outcome returns the
// challenge unchanged.
//
// The response is classified in this order, stopping at the first match:
//
//  1. Cloudflare's block page, or a 429: ErrBlocked. It says nothing about
//     the code.
//  2. A ticket: success.
//  3. No ticket and still an MFA page: ErrMFACodeRejected. Deliberately any
//     of several markers rather than one string — Garmin has reworded this
//     page before.
//  4. Anything else: a plain error carrying a fingerprint (title, size, field
//     names — never the body, which echoes the form). Not ErrMFACodeRejected:
//     the rider did nothing wrong and must not lose an attempt to our gap.
func (c *Client) ResumeMFA(ctx context.Context, ch MFAChallenge, code string) (Session, MFAChallenge, error) {
	if code == "" {
		return Session{}, ch, errors.New("garmin: a two-factor code is required")
	}
	if ch.CSRF == "" {
		return Session{}, ch, errors.New("garmin: the challenge has no CSRF token to resume with")
	}

	c.ImportCookies(ch.Cookies)

	// Both field names, deliberately. The current python-garminconnect reads
	// `mfa-code`; older garth widget code used `mfa-verification-code`.
	// Garmin's widget ignores fields it does not know, so sending both
	// survives whichever is live — do not tidy this down to one. Which one
	// Garmin actually reads is still unconfirmed against a real account.
	form := url.Values{
		"mfa-code":              {code},
		"mfa-verification-code": {code},
		"embed":                 {"true"},
		"_csrf":                 {ch.CSRF},
		"fromPage":              {"setupEnterMfaCode"},
	}

	query := c.signinParams().Encode()
	challengePage := c.SSOBase + "/signin?" + query
	endpoint := c.SSOBase + "/verifyMFA/loginEnterMfaCode?" + query
	body, status, err := c.do(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded", header{"Referer", challengePage})
	if err != nil {
		return Session{}, ch, fmt.Errorf("garmin: submitting the two-factor code: %w", err)
	}

	if status == http.StatusTooManyRequests || blocked(status, body) {
		return Session{}, ch, ErrBlocked
	}

	if match := ticketPattern.FindSubmatch(body); match != nil {
		if err := c.exchangeTicket(ctx, string(match[1])); err != nil {
			return Session{}, ch, err
		}
		c.session.ObtainedAt = c.now()
		return c.session, MFAChallenge{}, nil
	}

	if mfaPattern.Match(body) || mfaStillPattern.Match(body) {
		return Session{}, c.refreshed(ch, body), ErrMFACodeRejected
	}

	return Session{}, ch, fmt.Errorf("garmin: the two-factor code was answered with an unrecognised page (status %d, %s)",
		status, fingerprint(body))
}

// refreshed is ch with the state the re-rendered challenge page moved on:
// its own CSRF token if it has one, and the current cookies.
func (c *Client) refreshed(ch MFAChallenge, page []byte) MFAChallenge {
	out := ch
	if m := csrfPattern.FindSubmatch(page); m != nil {
		out.CSRF = string(m[1])
	}
	out.Cookies = c.ExportCookies()
	if method := mfaMethodOf(page); method != "" {
		out.Method = method
	}
	return out
}
