// Package oidcflow drives the authorization-code-with-PKCE dance against an
// OIDC issuer, and verifies what comes back.
//
// This is the one place in Domestique that adds a dependency beyond the
// standard-library-first budget (see AGENTS.md): github.com/coreos/go-oidc
// for discovery, JWKS handling and ID-token verification — a spec where
// writing it yourself means writing the vulnerabilities yourself. Everything
// else here — PKCE generation, the authorization redirect, the token
// exchange — is one POST and a handful of query strings, small enough that
// pulling in golang.org/x/oauth2 as a second direct dependency for it would
// cost more than it saves. It still appears in go.sum as an indirect
// dependency, via go-oidc's own use of it — unavoidable, and no different in
// kind from the golang.org/x/* indirects pgx and the sqlite driver already
// bring in.
package oidcflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/wncservices/domestique/apps/api/internal/telemetry"
)

// EnvClientSecret is where the OIDC client secret comes from. Never the
// config file — the same rule as every other credential in this app.
//
// #nosec G101 -- this is the name of an environment variable, not a secret
// value; gosec's entropy heuristic cannot tell the two apart from the string
// alone.
const EnvClientSecret = "DOMESTIQUE_OIDC_CLIENT_SECRET"

// Config is what Flow needs to talk to one issuer.
//
// No RedirectURL here, deliberately: AuthCodeURL and Exchange both take it as
// an explicit parameter instead, since it names a route in the caller's own
// app (api.Server's /sso/callback), not anything about the issuer. A field
// that is only ever assigned and never read by this package is worse than no
// field.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	GroupsClaim  string
	Scopes       []string
}

// Flow is a configured connection to one issuer, built once at startup.
type Flow struct {
	cfg      Config
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier

	authEndpoint       string
	tokenEndpoint      string
	endSessionEndpoint string // "" if the issuer does not advertise one

	httpClient *http.Client
}

// instrumentedClient is what every outbound call this package makes goes
// through — discovery, JWKS fetches inside Verify, and the token exchange —
// so all three show up as spans under whatever request triggered them
// (New's caller bounds ctx with a timeout but startup has no parent request;
// Exchange and VerifyIDToken run inside /sso/callback's).
var instrumentedClient = &http.Client{
	Transport: telemetry.OutboundTransport(http.DefaultTransport),
}

// New runs discovery against cfg.Issuer — the one network call in this
// feature. Callers should bound ctx with a timeout: a DNS hiccup at startup
// must not hang `domestique serve` forever.
func New(ctx context.Context, cfg Config) (*Flow, error) {
	if cfg.ClientSecret == "" {
		return nil, errors.New("oidcflow: no client secret — set " + EnvClientSecret)
	}

	// go-oidc reads the client to use for discovery back out of ctx via
	// oidc.ClientContext, falling back to http.DefaultClient otherwise.
	ctx = oidc.ClientContext(ctx, instrumentedClient)
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidcflow: discovery against %s: %w", cfg.Issuer, err)
	}

	// authorization_endpoint/token_endpoint/end_session_endpoint are read via
	// Claims rather than Provider.Endpoint(): that method returns an
	// oauth2.Endpoint, and referencing its type would mean importing
	// golang.org/x/oauth2 directly for two field reads — see the package
	// doc for why that is not worth it here.
	var endpoints struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		EndSessionEndpoint    string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&endpoints); err != nil {
		return nil, fmt.Errorf("oidcflow: reading discovery document for %s: %w", cfg.Issuer, err)
	}
	if endpoints.AuthorizationEndpoint == "" || endpoints.TokenEndpoint == "" {
		return nil, fmt.Errorf(
			"oidcflow: discovery document for %s is missing authorization_endpoint or token_endpoint",
			cfg.Issuer)
	}

	return &Flow{
		cfg:                cfg,
		provider:           provider,
		verifier:           provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		authEndpoint:       endpoints.AuthorizationEndpoint,
		tokenEndpoint:      endpoints.TokenEndpoint,
		endSessionEndpoint: endpoints.EndSessionEndpoint,
		httpClient:         instrumentedClient,
	}, nil
}

// AuthCodeURL builds the redirect to the issuer's authorization endpoint.
func (f *Flow) AuthCodeURL(state, nonce, codeChallenge, redirectURI string) string {
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(f.cfg.Scopes, " ")},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	return f.authEndpoint + "?" + v.Encode()
}

// tokenResponse is the token endpoint's JSON shape — the fields this package
// reads, ignoring the rest (access_token, refresh_token, expires_in: there is
// no refresh flow here, the RP session has its own TTL independent of the ID
// token's).
type tokenResponse struct {
	IDToken          string `json:"id_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Exchange trades an authorization code for a raw ID token.
func (f *Flow) Exchange(ctx context.Context, code, codeVerifier, redirectURI string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {f.cfg.ClientID},
		"client_secret": {f.cfg.ClientSecret},
		"code_verifier": {codeVerifier},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.tokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidcflow: token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("oidcflow: reading token response: %w", err)
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("oidcflow: unreadable token response (status %d): %s",
			resp.StatusCode, snippet(body))
	}
	if tok.Error != "" {
		return "", fmt.Errorf("oidcflow: %s: %s", tok.Error, tok.ErrorDescription)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidcflow: token endpoint returned %d: %s", resp.StatusCode, snippet(body))
	}
	if tok.IDToken == "" {
		return "", errors.New("oidcflow: token response carried no id_token")
	}
	return tok.IDToken, nil
}

// VerifyIDToken checks signature (against the issuer's JWKS, with rotation
// handled by go-oidc), issuer, audience and expiry.
//
// It does not check the nonce — go-oidc deliberately leaves that to the
// caller, since only the caller knows what nonce it asked for. Compare
// the returned token's Nonce field against the one sealed in the state
// cookie before trusting anything else about it.
func (f *Flow) VerifyIDToken(ctx context.Context, raw string) (*oidc.IDToken, error) {
	return f.verifier.Verify(oidc.ClientContext(ctx, f.httpClient), raw)
}

// EndSessionURL returns the issuer's RP-initiated logout URL, or "" if the
// issuer's discovery document did not advertise one — not every issuer does,
// and a deployment against one that doesn't just ends the local session.
//
// Always carries client_id alongside post_logout_redirect_uri, with or
// without idTokenHint. Per the RP-Initiated Logout spec, and confirmed
// against Auth0's own docs, an issuer will not honour
// post_logout_redirect_uri at all unless the request also identifies the
// client — either id_token_hint or client_id. Domestique's session never
// retains the raw ID token past login (sessions.Store seals only the
// derived Identity), so idTokenHint here is "" from the one real caller;
// client_id is what is actually available, and is exactly this parameter's
// documented purpose for that case. Without it, the browser lands on the
// issuer's own logout page and stays there instead of coming back.
func (f *Flow) EndSessionURL(postLogoutRedirect, idTokenHint string) string {
	if f.endSessionEndpoint == "" {
		return ""
	}
	v := url.Values{}
	if postLogoutRedirect != "" {
		v.Set("post_logout_redirect_uri", postLogoutRedirect)
	}
	if idTokenHint != "" {
		v.Set("id_token_hint", idTokenHint)
	} else if f.cfg.ClientID != "" {
		v.Set("client_id", f.cfg.ClientID)
	}
	if len(v) == 0 {
		return f.endSessionEndpoint
	}
	return f.endSessionEndpoint + "?" + v.Encode()
}

// NewState and NewNonce are both just an unguessable random string — same
// shape, different purpose (state is CSRF protection on the callback, nonce
// binds the ID token to this specific authorization request). Kept as two
// names rather than one, so a call site reads as what it is for.
func NewState() (string, error) { return randomString() }
func NewNonce() (string, error) { return randomString() }

// NewPKCEVerifier is the "code_verifier" RFC 7636 asks for: 32 random bytes,
// base64url — 43 characters, inside the RFC's allowed charset with no
// further work needed.
func NewPKCEVerifier() (string, error) { return randomString() }

// PKCEChallenge derives the S256 "code_challenge" sent with the authorization
// request from a verifier generated by NewPKCEVerifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomString() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("oidcflow: generating random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// snippet keeps an error readable when the body is unexpected — an HTML
// error page from a misconfigured issuer, say — rather than dumping the
// whole thing into a log line.
func snippet(raw []byte) string {
	const limit = 300
	text := strings.TrimSpace(string(raw))
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}
