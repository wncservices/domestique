// Package garmin talks to Garmin Connect as one rider.
//
// There is no self-serve Garmin API. The official Courses API is Connect
// Developer Program only — commercial partners — so this uses the same
// endpoints Connect's own web app does. That has consequences worth stating
// plainly, because they shape the whole package:
//
//   - It can break on any Garmin deploy. Failures are contained: a push fails,
//     the route stays in the library, and nothing else stops working.
//   - It is acceptable for two personal accounts and not for anything shared
//     more widely.
//
// # The handshake
//
// Signing in is four steps, and only the first involves the password:
//
//  1. GET the SSO sign-in page for a CSRF token and its cookies.
//  2. POST the credentials. A successful response embeds a service ticket.
//  3. Exchange the ticket for an OAuth1 token, signed with Connect's own
//     consumer key.
//  4. Exchange the OAuth1 token for an OAuth2 bearer token.
//
// The bearer token is what every later call uses. It expires in about a day
// and is refreshed from the OAuth1 token, which lasts roughly a year — so the
// OAuth1 token is what has to be stored, and its eventual expiry is a thing to
// notice deliberately rather than discover at the start of a ride. See
// TokenExpiry.
package garmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	ssoBase     = "https://sso.garmin.com/sso"
	connectAPI  = "https://connectapi.garmin.com"
	connectBase = "https://connect.garmin.com"

	defaultTimeout = 45 * time.Second
	maxBody        = 8 << 20
)

// ErrMFARequired means the account has two-factor authentication on.
//
// Its own error because it is not a failure to fix by retrying, and the UI has
// to say something different: this flow cannot complete an MFA challenge, and
// pretending the password was wrong would send the rider round in circles.
var ErrMFARequired = errors.New("garmin: this account requires two-factor authentication, which this sign-in cannot complete")

// ErrBadCredentials means Garmin rejected the email or password.
var ErrBadCredentials = errors.New("garmin: email or password not accepted")

// ErrBlocked means Cloudflare refused the request before Garmin saw it.
//
// Its own error because it is the one failure that says nothing about the
// account: the sign-in never reached Garmin. Reporting it as a wrong password
// sends someone to reset a password that was fine, which is the same mistake
// as reporting an MFA challenge that way. Observed after repeated failed
// attempts from one address, so it usually clears on its own.
var ErrBlocked = errors.New(
	"garmin: the sign-in was blocked by Garmin's bot protection before it reached them")

// Session is what has to be kept to stay signed in.
//
// OAuth1Token and OAuth1Secret are the long-lived pair; everything else is
// derived and can be thrown away. Store these two encrypted, the way a Komoot
// session is stored.
type Session struct {
	OAuth1Token  string `json:"oauth1Token"`
	OAuth1Secret string `json:"oauth1Secret"`
	// DisplayName is the account's name for the UI: the rider's full name
	// when Connect has one.
	DisplayName string `json:"displayName,omitempty"`
	// ProfileID is Connect's own "displayName", the opaque handle the
	// wellness endpoints take in their path (sleep, daily summary, resting
	// HR). Not the same as DisplayName above: putting the full name in
	// those paths gets a 404, read as "no reading", and every night's sleep
	// and resting HR came back empty. Empty on sessions stored before this
	// field existed; Client.ProfileID looks it up.
	ProfileID  string    `json:"profileId,omitempty"`
	ObtainedAt time.Time `json:"obtainedAt"`
}

// TokenExpiry is when an OAuth1 token stops working, near enough.
//
// Garmin does not say. A year is the observed lifetime, and the point of
// exposing it is so a deployment can warn *before* pushes start failing —
// silence is the failure mode that matters here.
func (s Session) TokenExpiry() time.Time { return s.ObtainedAt.AddDate(1, 0, 0) }

// Expired reports whether the stored token is past its expected life.
func (s Session) Expired(now time.Time) bool { return now.After(s.TokenExpiry()) }

// Client is one rider's Connect session.
type Client struct {
	HTTP *http.Client

	// Base URLs, overridable so tests do not touch Garmin.
	SSOBase    string
	APIBase    string
	WebBase    string
	UserAgent  string
	Now        func() time.Time
	consumer   consumerKey
	session    Session
	bearer     string
	bearerTill time.Time
}

// consumerKey is the OAuth1 consumer Connect's own clients use.
type consumerKey struct {
	Key    string
	Secret string
}

// New returns a client that has not signed in yet.
func New() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		HTTP: &http.Client{
			Timeout:   defaultTimeout,
			Jar:       jar,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
			// A redirect would carry the Authorization header to wherever it
			// points, which is the same credential-disclosure problem
			// allowedHost exists to prevent. The SSO flow is followed
			// explicitly instead.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		SSOBase: ssoBase,
		APIBase: connectAPI,
		WebBase: connectBase,
		// Connect's SSO rejects requests that do not look like a browser.
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36",
		Now: time.Now,
	}
}

// Session returns what should be stored to sign in again without a password.
func (c *Client) Session() Session { return c.session }

// Resume rebuilds a client from a stored session. No password, no SSO: the
// OAuth1 token is exchanged for a fresh bearer on the next call.
func (c *Client) Resume(s Session) { c.session = s }

var (
	csrfPattern = regexp.MustCompile(`name="_csrf"\s+value="([^"]+)"`)
	// The service ticket, wherever the success page happens to put it.
	//
	// This used to require `embed?ticket=...` inside double quotes. Garmin
	// still signs the rider in and still returns its "Success" page, but the
	// ticket now sits in a JavaScript string the old pattern did not match —
	// so a completed sign-in was reported as a wrong password, which is about
	// the most misleading thing this package could have said.
	//
	// Matched on `ticket=` after a `?` or `&`, ending at whatever quote,
	// ampersand or whitespace follows. Nothing about the surrounding page is
	// assumed: that assumption is what broke.
	ticketPattern = regexp.MustCompile(`[?&]ticket=([^"'&\s<>]+)`)
	// Connect returns 200 with an MFA page rather than an error status.
	mfaPattern = regexp.MustCompile(`(?i)mfa-code|verificationCode|two-step`)
	// Cloudflare's block page. Matched on the interstitial's own markers
	// rather than on the status alone, because 403 is also what Garmin
	// itself returns for other things.
	blockedPattern = regexp.MustCompile(`(?i)Attention Required!|Sorry, you have been blocked|cf-wrapper|cf-error-details`)

	// Used only to describe a page nobody expected. See fingerprint.
	titlePattern     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	inputNamePattern = regexp.MustCompile(`(?i)<input[^>]+name="([^"]+)"`)
)

// fingerprint describes an unexpected page in terms safe to log.
//
// The body itself never goes near a log line — it echoes the form that was
// posted, password included. A title, a size and the names of the form fields
// carry none of that and are enough to recognise a page: a sign-in form that
// came back has a password field, an MFA challenge asks for a code, and a
// consent wall has neither.
//
// Field *names* only. A hidden input's value is a token; its name is
// structure.
func fingerprint(body []byte) string {
	parts := make([]string, 0, 3)

	if m := titlePattern.FindSubmatch(body); m != nil {
		title := strings.Join(strings.Fields(string(m[1])), " ")
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		parts = append(parts, fmt.Sprintf("title=%q", title))
	}
	parts = append(parts, fmt.Sprintf("bytes=%d", len(body)))

	seen := map[string]bool{}
	names := make([]string, 0, 6)
	for _, m := range inputNamePattern.FindAllSubmatch(body, -1) {
		name := string(m[1])
		if seen[name] {
			continue
		}
		seen[name] = true
		if names = append(names, name); len(names) == 6 {
			break
		}
	}
	if len(names) > 0 {
		parts = append(parts, "fields=["+strings.Join(names, " ")+"]")
	}
	return strings.Join(parts, " ")
}

// blocked reports whether a response is Cloudflare's block page rather than
// anything Garmin generated.
func blocked(status int, body []byte) bool {
	return (status == http.StatusForbidden || status == http.StatusTooManyRequests) &&
		blockedPattern.Match(body)
}

// Login signs in with a password.
//
// The password is used here and nowhere else: what comes back is a Session,
// and that is what a caller stores in its place.
//
// When Garmin asks for a two-factor code instead of issuing a ticket, the
// error is ErrMFARequired and the returned MFAChallenge is what ResumeMFA
// needs to answer it from a later request. It never contains the password.
// On every other outcome the challenge is the zero value.
func (c *Client) Login(ctx context.Context, email, password string) (MFAChallenge, error) {
	if email == "" || password == "" {
		return MFAChallenge{}, errors.New("garmin: email and password are both required")
	}

	// Connect's own page loads the embedded widget before the form, which
	// sets cookies the later requests are expected to carry. Skipping it
	// worked for a while and is the kind of difference from a real browser
	// that bot protection notices, so it is no longer skipped.
	if err := c.preflight(ctx); err != nil {
		return MFAChallenge{}, err
	}

	csrf, err := c.signinPage(ctx)
	if err != nil {
		return MFAChallenge{}, err
	}

	ticket, challenge, err := c.submitCredentials(ctx, email, password, csrf)
	if err != nil {
		return challenge, err
	}

	if err := c.exchangeTicket(ctx, ticket); err != nil {
		return MFAChallenge{}, err
	}

	c.session.ObtainedAt = c.now()
	return MFAChallenge{}, nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// embedURL is the service this client authenticates for.
//
// One function because the value has to appear in two places that must agree:
// the `service` a ticket is issued for, and the `login-url` that ticket is
// later presented with. A CAS service ticket is only valid for the service it
// was issued for, so when those two drifted apart the sign-in succeeded and
// the exchange came back 401 — a failure that looks nothing like its cause.
func (c *Client) embedURL() string { return c.SSOBase + "/embed" }

// signinParams are what Connect's own sign-in page carries. They are not
// optional: the SSO service refuses a request that does not name the service
// it is authenticating for.
func (c *Client) signinParams() url.Values {
	return url.Values{
		// Must equal the login-url in exchangeTicket. See embedURL.
		"service":              {c.embedURL()},
		"webhost":              {c.WebBase},
		"source":               {c.SSOBase + "/signin"},
		"gauthHost":            {c.SSOBase},
		"clientId":             {"GarminConnect"},
		"consumeServiceTicket": {"false"},
	}
}

// embedParams are what the sign-in widget is loaded with.
func (c *Client) embedParams() url.Values {
	return url.Values{
		"id":                              {"gauth-widget"},
		"embedWidget":                     {"true"},
		"gauthHost":                       {c.SSOBase},
		"redirectAfterAccountLoginUrl":    {c.WebBase + "/modern/"},
		"redirectAfterAccountCreationUrl": {c.WebBase + "/modern/"},
	}
}

// preflight loads the widget, for its cookies.
func (c *Client) preflight(ctx context.Context) error {
	endpoint := c.SSOBase + "/embed?" + c.embedParams().Encode()
	body, status, err := c.do(ctx, http.MethodGet, endpoint, nil, "")
	if err != nil {
		return fmt.Errorf("garmin: loading the sign-in widget: %w", err)
	}
	if blocked(status, body) {
		return ErrBlocked
	}
	return nil
}

func (c *Client) signinPage(ctx context.Context) (csrf string, err error) {
	endpoint := c.SSOBase + "/signin?" + c.signinParams().Encode()
	body, status, err := c.do(ctx, http.MethodGet, endpoint, nil, "",
		header{"Referer", c.SSOBase + "/embed?" + c.embedParams().Encode()})
	if err != nil {
		return "", fmt.Errorf("garmin: fetching the sign-in page: %w", err)
	}
	if blocked(status, body) {
		return "", ErrBlocked
	}

	match := csrfPattern.FindSubmatch(body)
	if match == nil {
		return "", errors.New("garmin: no CSRF token on the sign-in page — the SSO flow has changed")
	}
	return string(match[1]), nil
}

func (c *Client) submitCredentials(ctx context.Context, email, password, csrf string) (ticket string, challenge MFAChallenge, err error) {
	form := url.Values{
		"username": {email},
		"password": {password},
		"embed":    {"false"},
		"_csrf":    {csrf},
	}

	endpoint := c.SSOBase + "/signin?" + c.signinParams().Encode()
	body, status, err := c.do(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded",
		// A browser posts a form from the page it just loaded. Without this
		// the request looks like it came from nowhere.
		header{"Referer", endpoint})
	if err != nil {
		return "", MFAChallenge{}, err
	}

	if match := ticketPattern.FindSubmatch(body); match != nil {
		return string(match[1]), MFAChallenge{}, nil
	}

	// No ticket. Work out why, because "login failed" covers four very
	// different situations and only one of them is the password.
	//
	// The credentials case is both 200 and 401: Garmin answered 200 with an
	// error page for years and now answers 401, and both were observed within
	// a day of each other. Treating only 200 as "wrong password" turned a
	// rejected password into "Garmin could not be signed in to just now — try
	// again later", which is a maddening thing to be told when the fix is to
	// check what you typed.
	switch {
	case blocked(status, body):
		return "", MFAChallenge{}, ErrBlocked
	case mfaPattern.Match(body):
		return "", c.challengeFrom(email, body), ErrMFARequired
	case status == http.StatusOK, status == http.StatusUnauthorized:
		// Carry the status and a description of the page. Both codes mean "no
		// ticket", and the status alone was enough to learn that a rejected
		// password answers 401 — which makes a 200 something else, and the
		// question becomes *what*. The fingerprint answers that without
		// quoting the body, which can echo the request.
		return "", MFAChallenge{}, fmt.Errorf("%w (sign-in returned %d, %s)",
			ErrBadCredentials, status, fingerprint(body))
	default:
		return "", MFAChallenge{}, fmt.Errorf("garmin: sign-in returned %d and no ticket", status)
	}
}

// exchangeTicket turns a service ticket into the OAuth1 token pair.
func (c *Client) exchangeTicket(ctx context.Context, ticket string) error {
	if err := c.loadConsumer(); err != nil {
		return err
	}

	// login-url must be the service the ticket was issued for — the same
	// value signinParams sent. See embedURL.
	endpoint := fmt.Sprintf(
		"%s/oauth-service/oauth/preauthorized?ticket=%s&login-url=%s&accepts-mfa-tokens=true",
		c.APIBase, url.QueryEscape(ticket), url.QueryEscape(c.embedURL()))

	signed, err := c.signOAuth1(http.MethodGet, endpoint, "", "")
	if err != nil {
		return err
	}

	body, status, err := c.do(ctx, http.MethodGet, endpoint, nil, "", header{"Authorization", signed})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("garmin: exchanging the ticket returned %d", status)
	}

	// The response is form-encoded, not JSON.
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return fmt.Errorf("garmin: unreadable OAuth1 response: %w", err)
	}
	token, secret := values.Get("oauth_token"), values.Get("oauth_token_secret")
	if token == "" || secret == "" {
		return errors.New("garmin: the OAuth1 exchange returned no token")
	}

	c.session.OAuth1Token, c.session.OAuth1Secret = token, secret
	return nil
}

type header struct{ Name, Value string }

// bearerToken returns a valid OAuth2 bearer, refreshing it when needed.
//
// The bearer lasts about a day and the OAuth1 token about a year, so this is
// the routine that runs on essentially every call — hence the cache.
func (c *Client) bearerToken(ctx context.Context) (string, error) {
	if c.bearer != "" && c.now().Before(c.bearerTill) {
		return c.bearer, nil
	}
	if c.session.OAuth1Token == "" {
		return "", errors.New("garmin: not signed in")
	}
	if err := c.loadConsumer(); err != nil {
		return "", err
	}

	endpoint := c.APIBase + "/oauth-service/oauth/exchange/user/2.0"
	signed, err := c.signOAuth1(http.MethodPost, endpoint,
		c.session.OAuth1Token, c.session.OAuth1Secret)
	if err != nil {
		return "", err
	}

	body, status, err := c.do(ctx, http.MethodPost, endpoint, strings.NewReader(""),
		"application/x-www-form-urlencoded", header{"Authorization", signed})
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized {
		return "", fmt.Errorf("garmin: the stored token is no longer accepted — sign in again")
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("garmin: the OAuth2 exchange returned %d", status)
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("garmin: unreadable OAuth2 response: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("garmin: the OAuth2 exchange returned no access token")
	}

	life := time.Duration(out.ExpiresIn) * time.Second
	if life <= 0 {
		life = time.Hour
	}
	// Refresh a minute early rather than racing the expiry mid-push.
	c.bearer, c.bearerTill = out.AccessToken, c.now().Add(life-time.Minute)
	return c.bearer, nil
}

// allowedHost reports whether a URL may be requested.
//
// Every call here carries a credential: the OAuth1 signature during sign-in,
// the bearer afterwards. A request to a host we were not configured for would
// hand that credential to whoever answers — so anything off the three
// configured bases is refused before it is sent, not after.
//
// The same guard internal/komoot needed, for the same reason. There it was
// pagination following a URL out of a response body; here the bases are
// fields, and a field is exactly the sort of thing that later gets wired to
// configuration by somebody who has not read this comment.
func (c *Client) allowedHost(raw string) error {
	target, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("garmin: unusable URL %q: %w", raw, err)
	}

	for _, base := range []string{c.SSOBase, c.APIBase, c.WebBase} {
		known, err := url.Parse(base)
		if err != nil || known.Host == "" {
			continue
		}
		if target.Scheme == known.Scheme && strings.EqualFold(target.Host, known.Host) {
			return nil
		}
	}
	return fmt.Errorf("garmin: refusing to send credentials to %q, which is not a configured host", target.Host)
}

// do performs a request and reads a capped body.
func (c *Client) do(ctx context.Context, method, endpoint string, body io.Reader, contentType string, extra ...header) ([]byte, int, error) {
	if err := c.allowedHost(endpoint); err != nil {
		return nil, 0, err
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	// What a browser sends and a bare HTTP client does not. Set before the
	// caller's own headers so anything explicit still wins — the API calls
	// ask for JSON and get it.
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, h := range extra {
		req.Header.Set(h.Name, h.Value)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Capped: this is a third party, and an unbounded read is a way to be
	// taken down by one.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}
