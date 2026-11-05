package oidcflow

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testClientID = "domestique-test-client"

// fakeIssuer is a genuinely working minimal OIDC issuer, not canned JSON:
// go-oidc does real signature verification against whatever JWKS URI its
// discovery document advertises, so the fake has to serve a real discovery
// document, a real JWKS, and a real RS256-signed JWT for verification to mean
// anything at all.
type fakeIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string

	advertiseEndSession bool

	// What /token returns. nextClaims is signed with key/kid on the next
	// request unless nextRawToken or tokenStatus/tokenError override it.
	nextClaims  map[string]any
	signWithKey *rsa.PrivateKey // nil means key
	tokenStatus int             // 0 means 200
	tokenError  string          // set to make /token answer an OAuth error
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	f := &fakeIssuer{key: key, kid: "test-key"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"issuer":                 f.server.URL,
			"authorization_endpoint": f.server.URL + "/authorize",
			"token_endpoint":         f.server.URL + "/token",
			"jwks_uri":               f.server.URL + "/jwks",
		}
		if f.advertiseEndSession {
			doc["end_session_endpoint"] = f.server.URL + "/logout"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []any{jwkFor(f.kid, &f.key.PublicKey)},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if f.tokenError != "" {
			status := f.tokenStatus
			if status == 0 {
				status = http.StatusBadRequest
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": f.tokenError, "error_description": "fake issuer said no",
			})
			return
		}
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
		}
		signer := f.key
		if f.signWithKey != nil {
			signer = f.signWithKey
		}
		raw, err := signJWT(t, signer, f.kid, f.nextClaims)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": raw})
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// baseClaims is a plausible, valid ID token for this issuer — every negative
// test starts here and breaks exactly one thing.
func (f *fakeIssuer) baseClaims(nonce string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":                f.server.URL,
		"sub":                "auth0|abc123",
		"aud":                testClientID,
		"exp":                now.Add(time.Hour).Unix(),
		"iat":                now.Unix(),
		"nonce":              nonce,
		"preferred_username": "wilant",
		"groups":             []string{"cyclists"},
	}
}

func newFlow(t *testing.T, f *fakeIssuer) *Flow {
	t.Helper()
	flow, err := New(context.Background(), Config{
		Issuer:       f.server.URL,
		ClientID:     testClientID,
		ClientSecret: "test-secret",
		Scopes:       []string{"openid", "profile", "email"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return flow
}

// The whole path: authorization URL carries what it should, exchange
// returns an ID token, verification accepts it and exposes the claims a
// caller needs to build an identity.
func TestFlowCompletesTheFullExchangeAndVerification(t *testing.T) {
	f := newFakeIssuer(t)
	f.nextClaims = f.baseClaims("nonce-1")
	flow := newFlow(t, f)

	authURL := flow.AuthCodeURL("state-1", "nonce-1", "challenge-1", "https://app.example.test/sso/callback")
	if !strings.HasPrefix(authURL, f.server.URL+"/authorize?") {
		t.Fatalf("AuthCodeURL = %q, want it on the issuer's authorization endpoint", authURL)
	}
	for _, want := range []string{"client_id=" + testClientID, "state=state-1", "nonce=nonce-1",
		"code_challenge=challenge-1", "code_challenge_method=S256", "response_type=code"} {
		if !strings.Contains(authURL, want) {
			t.Errorf("AuthCodeURL missing %q: %s", want, authURL)
		}
	}

	raw, err := flow.Exchange(context.Background(), "any-code", "verifier-1",
		"https://app.example.test/sso/callback")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	token, err := flow.VerifyIDToken(context.Background(), raw)
	if err != nil {
		t.Fatalf("VerifyIDToken: %v", err)
	}
	if token.Subject != "auth0|abc123" {
		t.Errorf("Subject = %q", token.Subject)
	}
	if token.Nonce != "nonce-1" {
		t.Errorf("Nonce = %q, want it carried through unverified (caller's job)", token.Nonce)
	}

	var claims struct {
		PreferredUsername string   `json:"preferred_username"`
		Groups            []string `json:"groups"`
	}
	if err := token.Claims(&claims); err != nil {
		t.Fatal(err)
	}
	if claims.PreferredUsername != "wilant" {
		t.Errorf("preferred_username = %q", claims.PreferredUsername)
	}
	if len(claims.Groups) != 1 || claims.Groups[0] != "cyclists" {
		t.Errorf("groups = %v", claims.Groups)
	}
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	f := newFakeIssuer(t)
	claims := f.baseClaims("n")
	claims["aud"] = "somebody-elses-client"
	f.nextClaims = claims
	flow := newFlow(t, f)

	raw, err := flow.Exchange(context.Background(), "code", "v", "https://app.example.test/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.VerifyIDToken(context.Background(), raw); err == nil {
		t.Fatal("a token for a different client was accepted")
	}
}

func verifyWith(t *testing.T, mutate func(map[string]any)) error {
	t.Helper()
	f := newFakeIssuer(t)
	claims := f.baseClaims("n")
	mutate(claims)
	f.nextClaims = claims
	flow := newFlow(t, f)
	raw, err := flow.Exchange(context.Background(), "code", "v", "https://app.example.test/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	_, err = flow.VerifyIDToken(context.Background(), raw)
	return err
}

func TestVerifyChecksAuthorizedParty(t *testing.T) {
	multi := []string{testClientID, "other-client"}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		ok     bool
	}{
		{"single aud, no azp", func(c map[string]any) {}, true},
		{"single aud, own azp", func(c map[string]any) { c["azp"] = testClientID }, true},
		{"single aud, foreign azp", func(c map[string]any) { c["azp"] = "other-client" }, false},
		{"multi aud, no azp", func(c map[string]any) { c["aud"] = multi }, false},
		{"multi aud, foreign azp", func(c map[string]any) { c["aud"] = multi; c["azp"] = "other-client" }, false},
		{"multi aud, own azp", func(c map[string]any) { c["aud"] = multi; c["azp"] = testClientID }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyWith(t, tc.mutate)
			if tc.ok && err != nil {
				t.Errorf("rejected: %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	f := newFakeIssuer(t)
	claims := f.baseClaims("n")
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	f.nextClaims = claims
	flow := newFlow(t, f)

	raw, err := flow.Exchange(context.Background(), "code", "v", "https://app.example.test/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.VerifyIDToken(context.Background(), raw); err == nil {
		t.Fatal("an expired token was accepted")
	}
}

// A token signed by a key the issuer never published must be rejected, not
// merely a token with the wrong claims — this is what actually proves
// verification checks the signature against the real JWKS rather than
// trusting whatever the token claims about itself.
func TestVerifyRejectsUntrustedSignature(t *testing.T) {
	f := newFakeIssuer(t)
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.signWithKey = wrongKey // signed with a key never published to /jwks
	f.nextClaims = f.baseClaims("n")
	flow := newFlow(t, f)

	raw, err := flow.Exchange(context.Background(), "code", "v", "https://app.example.test/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.VerifyIDToken(context.Background(), raw); err == nil {
		t.Fatal("a token signed by an untrusted key was accepted")
	}
}

func TestExchangeSurfacesIssuerError(t *testing.T) {
	f := newFakeIssuer(t)
	f.tokenError = "invalid_grant"
	flow := newFlow(t, f)

	_, err := flow.Exchange(context.Background(), "bad-code", "v", "https://app.example.test/sso/callback")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("err = %v, want it to name the issuer's error", err)
	}
}

func TestEndSessionURLWhenAdvertised(t *testing.T) {
	f := newFakeIssuer(t)
	f.advertiseEndSession = true
	flow := newFlow(t, f)

	got := flow.EndSessionURL("https://app.example.test/", "id-token-hint")
	if !strings.HasPrefix(got, f.server.URL+"/logout?") {
		t.Fatalf("EndSessionURL = %q", got)
	}
	if !strings.Contains(got, "post_logout_redirect_uri=") || !strings.Contains(got, "id_token_hint=") {
		t.Errorf("EndSessionURL missing params: %s", got)
	}
}

// Domestique's one real caller never has an ID token to hand back (the
// session stores only the derived Identity) — client_id is what actually
// reaches this method, and per Auth0's own logout endpoint, that is the one
// thing standing between post_logout_redirect_uri being honoured and the
// visitor being stranded on the issuer's own logout page.
func TestEndSessionURLFallsBackToClientIDWithoutAnIDTokenHint(t *testing.T) {
	f := newFakeIssuer(t)
	f.advertiseEndSession = true
	flow := newFlow(t, f)

	got := flow.EndSessionURL("https://app.example.test/", "")
	if !strings.Contains(got, "post_logout_redirect_uri=") {
		t.Errorf("EndSessionURL missing post_logout_redirect_uri: %s", got)
	}
	if !strings.Contains(got, "client_id="+testClientID) {
		t.Errorf("EndSessionURL missing client_id: %s", got)
	}
	if strings.Contains(got, "id_token_hint=") {
		t.Errorf("EndSessionURL sent an empty id_token_hint: %s", got)
	}
}

func TestEndSessionURLWhenNotAdvertised(t *testing.T) {
	f := newFakeIssuer(t) // advertiseEndSession left false
	flow := newFlow(t, f)

	if got := flow.EndSessionURL("https://app.example.test/", "hint"); got != "" {
		t.Errorf("EndSessionURL = %q, want empty when the issuer advertises none", got)
	}
}

func TestPKCEChallengeIsDeterministicS256(t *testing.T) {
	verifier, err := NewPKCEVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier) < 43 {
		t.Errorf("verifier is %d chars, RFC 7636 wants at least 43", len(verifier))
	}

	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := PKCEChallenge(verifier); got != want {
		t.Errorf("PKCEChallenge = %q, want %q", got, want)
	}
	// Deterministic: the same verifier always derives the same challenge.
	// Two separate calls into two separate variables, not one expression
	// compared against itself — staticcheck flags the latter as trivially
	// always-equal, which misses the point of the check.
	first, second := PKCEChallenge(verifier), PKCEChallenge(verifier)
	if first != second {
		t.Errorf("PKCEChallenge is not deterministic: %q vs %q", first, second)
	}
}

func TestStateAndNonceAreRandomAndDistinct(t *testing.T) {
	a, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two calls to NewState produced the same value")
	}
	n, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	if n == a {
		t.Error("NewNonce collided with NewState — both should be independently random")
	}
}

func TestNewFailsWithoutAClientSecret(t *testing.T) {
	f := newFakeIssuer(t)
	_, err := New(context.Background(), Config{
		Issuer: f.server.URL, ClientID: testClientID,
	})
	if err == nil {
		t.Fatal("New accepted a config with no client secret")
	}
}

// --- JWT signing for the fake issuer ---

func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}

	h, err := b64JSON(header)
	if err != nil {
		return "", err
	}
	p, err := b64JSON(claims)
	if err != nil {
		return "", err
	}
	signingInput := h + "." + p

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64JSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// jwkFor is the JWK Set entry for an RSA public key — the fields go-jose
// (used internally by go-oidc for JWKS parsing) needs: kty/kid/use/alg plus
// the modulus and exponent, base64url of their big-endian bytes.
func jwkFor(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA",
		"kid": kid,
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}
