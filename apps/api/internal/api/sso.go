package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/oidcflow"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

// sessionTTL is fixed rather than configurable. There is no refresh-token
// handling in this design — the RP session is a server-side cookie with its
// own lifetime, independent of the ID token's — so the TTL is purely "how
// long before a rider clicks through the issuer again", not a security
// boundary. 30 days matches this app's general convenience-over-paranoia
// posture elsewhere (ModeNone is the local admin by default, default_role is
// a permissive fallback). A configurable session_ttl is a reasonable later
// addition; not worth designing speculatively now.
const sessionTTL = 30 * 24 * time.Hour

// oidcState is what /sso/login seals into auth.OIDCStateCookie and
// /sso/callback opens: everything needed to complete one specific
// authorization request and nothing that would still matter if it were
// replayed a second time (Create's session token is separate).
type oidcState struct {
	State    string    `json:"state"`
	Nonce    string    `json:"nonce"`
	Verifier string    `json:"verifier"`
	ReturnTo string    `json:"returnTo,omitempty"`
	IssuedAt time.Time `json:"issuedAt"`
}

// oidcReady reports whether these endpoints have anything to do. All three
// 404 rather than error when OIDC is not this deployment's mode — the same
// shape as any feature nobody asked for, not a broken one.
func (s *Server) oidcReady() bool {
	return s.authenticator().Mode() == auth.ModeOIDC && s.OIDC != nil
}

func (s *Server) oidcNotConfigured(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error": "this deployment is not running auth.mode: oidc",
	})
}

// handleSSOLogin starts the flow: generates state/nonce/PKCE, seals them
// into a short-lived cookie, and redirects to the issuer.
func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	if !s.oidcReady() {
		s.oidcNotConfigured(w)
		return
	}
	if s.Box == nil {
		// Unlike Komoot/Garmin sign-in, this cannot degrade to "the button is
		// just hidden" — there is nowhere safe to put the state, and without
		// it the callback cannot be trusted at all.
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "this deployment cannot start a sign-in: no encryption key — set " + secrets.EnvKey,
		})
		return
	}

	returnTo, err := safeReturnTo(r.URL.Query().Get("return_to"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	state, err := oidcflow.NewState()
	if err != nil {
		s.fail(w, err)
		return
	}
	nonce, err := oidcflow.NewNonce()
	if err != nil {
		s.fail(w, err)
		return
	}
	verifier, err := oidcflow.NewPKCEVerifier()
	if err != nil {
		s.fail(w, err)
		return
	}

	if err := s.setStateCookie(w, r, oidcState{
		State: state, Nonce: nonce, Verifier: verifier, ReturnTo: returnTo, IssuedAt: time.Now().UTC(),
	}); err != nil {
		s.fail(w, err)
		return
	}

	redirectURI := s.redirectURLForRequest(r)
	authURL := s.OIDC.AuthCodeURL(state, nonce, oidcflow.PKCEChallenge(verifier), redirectURI)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleSSOCallback verifies what the issuer sent back and, if it checks
// out, signs the rider in.
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	if !s.oidcReady() {
		s.oidcNotConfigured(w)
		return
	}

	st, err := s.openStateCookie(r)
	// The state cookie is single-use regardless of outcome: a failed
	// callback must not leave a cookie a client could replay.
	s.clearStateCookie(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing or expired sign-in — start over at /sso/login",
		})
		return
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		s.logger().Warn("oidc callback reported by issuer", "error", errParam, "description", desc)

		// access_denied is the one shape a rider actually reaches routinely
		// — the tenant's own post-login Action denies a login on purpose
		// mid-provisioning (linking an identity, granting a new signup its
		// roles) and asks for a second attempt, with the explanation in
		// error_description. Every other error here is a genuinely broken
		// flow (a stale state cookie, a misconfigured issuer) rare enough
		// that the plain JSON body below is an acceptable landing.
		//
		// This is a top-level browser navigation, not a fetch — writeJSON
		// here would render as a literal JSON blob on screen instead of the
		// app, since nothing else ever paints this response. Redirecting
		// back into the SPA with the explanation as a query param, the same
		// safeReturnTo-validated destination a successful callback already
		// uses, is what lets it show up as an ordinary toast instead.
		if errParam == "access_denied" {
			returnTo := st.ReturnTo
			if returnTo == "" {
				returnTo = "/"
			}
			notice := desc
			if notice == "" {
				notice = "Sign-in was not completed — please try again."
			}
			http.Redirect(w, r, withNotice(returnTo, notice), http.StatusFound)
			return
		}

		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "sign-in was not completed: " + errParam,
		})
		return
	}

	if got := r.URL.Query().Get("state"); got == "" || got != st.State {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "state did not match — start over"})
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the issuer sent no code"})
		return
	}

	redirectURI := s.redirectURLForRequest(r)
	rawIDToken, err := s.OIDC.Exchange(r.Context(), code, st.Verifier, redirectURI)
	if err != nil {
		s.logger().Warn("oidc token exchange failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "the identity provider did not accept the sign-in",
		})
		return
	}

	idToken, err := s.OIDC.VerifyIDToken(r.Context(), rawIDToken)
	if err != nil {
		s.logger().Warn("oidc id token failed verification", "err", err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "the sign-in could not be verified"})
		return
	}
	if idToken.Nonce != st.Nonce {
		// go-oidc verifies signature/issuer/audience/expiry but deliberately
		// leaves nonce to the caller — only the caller knows what it asked
		// for. This is that check.
		s.logger().Warn("oidc nonce mismatch")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "the sign-in could not be verified"})
		return
	}

	identity, err := s.identityFromToken(idToken)
	if err != nil {
		s.logger().Warn("oidc token carried no usable identity", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "the identity provider did not identify who signed in",
		})
		return
	}

	// A blocked rider is refused here, before a session is ever created —
	// regardless of which Auth0 identity the token names. Auth0's own
	// blocked flag (auth0mgmt.SetBlocked) only stops the specific identity
	// an admin saw and blocked; this stops a fresh signup with the same
	// email from getting back in too. Requires the token to carry an email
	// claim (auth.oidc.scopes must include "email") — without one, a
	// blocked rider signing in with a fresh identity simply is not caught,
	// the same limitation handleSelfPasswordReset already documents for
	// id.Email.
	if s.Blocklist != nil && identity.Email != "" {
		blocked, err := s.Blocklist.IsBlocked(r.Context(), identity.Email)
		if err != nil {
			s.logger().Warn("checking the blocklist failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "sign-in could not be completed"})
			return
		}
		if blocked {
			s.logger().Info("blocked email tried to sign in", "email", identity.Email, "user", identity.User)
			returnTo := st.ReturnTo
			if returnTo == "" {
				returnTo = "/"
			}
			http.Redirect(w, r, withNotice(returnTo, "This account has been blocked."), http.StatusFound)
			return
		}
	}

	token, expiresAt, err := s.Sessions.Create(identity, sessionTTL)
	if err != nil {
		s.fail(w, err)
		return
	}
	// #nosec G124 -- Secure, HttpOnly and SameSite are all set; gosec wants
	// Secure to be the literal `true` and cannot see through requestIsHTTPS,
	// which is conditional on purpose — see its own doc comment.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	})

	s.logger().Info("oidc login", "user", identity.User, "groups", identity.Groups)

	returnTo := st.ReturnTo
	if returnTo == "" {
		returnTo = "/"
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// handleSSOLogout ends the session and tells the frontend where to go next.
//
// A JSON response rather than a redirect: signing out is a same-origin
// fetch, and a fetch cannot itself carry the browser to a cross-origin
// top-level page the way a plain link can. The frontend does that
// navigation; this only says where.
func (s *Server) handleSSOLogout(w http.ResponseWriter, r *http.Request) {
	if !s.oidcReady() {
		s.oidcNotConfigured(w)
		return
	}

	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		if err := s.Sessions.Delete(cookie.Value); err != nil {
			s.logger().Warn("deleting oidc session failed", "err", err)
		}
	}
	// #nosec G124 -- see the identical note above; Secure is intentionally
	// conditional, not a literal true.
	http.SetCookie(w, &http.Cookie{
		Name: auth.SessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})

	// Land back on the public front door, not wherever the logout request
	// happened to be made from — that is the app host (app.domestique.dev),
	// which a now-anonymous visitor cannot do anything on anyway (see
	// spaHandler's own anonymous-oidc redirect). Falls back to the request's
	// own origin only when no separate landing host is configured, the same
	// "everyone gets the app" case spaHandler's redirect skips too.
	postLogout := requestOrigin(r) + "/"
	if s.LandingHost != "" {
		postLogout = "https://" + s.LandingHost + "/"
	}
	redirectTo := s.OIDC.EndSessionURL(postLogout, "")
	writeJSON(w, http.StatusOK, map[string]string{"redirectTo": redirectTo})
}

// identityFromToken builds an Identity from a verified ID token's claims.
//
// User is preferred_username, falling back to name, falling back to
// nickname, falling back to sub. Auth0's database connection never
// populates preferred_username, but does send both name and nickname
// (nickname auto-defaults to the local part of the email; name defaults to
// the same thing but, unlike nickname, is what the Auth0 dashboard's own
// user page prompts an admin to edit) whenever the "profile" scope is
// requested. name is tried first, ahead of nickname, on the theory that a
// value someone deliberately typed into a "Name" field is a better rider
// identity than one nobody has ever looked at — confirmed against a real
// case, not a guess: a nickname left at its auto-generated default
// ("wilant.nackaerts") split what should have been one rider into two,
// where the same person's already-edited name ("Wilant") matched an
// existing rider exactly, once lower-cased. sub (shaped like
// "auth0|64f2a1b2c3d4e5f6") is only the fallback for an issuer that sends
// none of the three. Each candidate is skipped, not just lower-cased, if it
// does not survive RiderPattern (an OIDC name or nickname can contain
// spaces or other characters an account id and a URL cannot) — falling
// through rather than handing Link a value it will reject one step later.
func (s *Server) identityFromToken(idToken *oidc.IDToken) (auth.Identity, error) {
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return auth.Identity{}, err
	}

	user := strings.ToLower(strings.TrimSpace(stringClaim(claims, "preferred_username")))
	for _, claim := range []string{"name", "nickname"} {
		if user != "" {
			break
		}
		if candidate := strings.ToLower(strings.TrimSpace(stringClaim(claims, claim))); accounts.RiderPattern.MatchString(candidate) {
			user = candidate
		}
	}
	if user == "" {
		user = strings.ToLower(strings.TrimSpace(idToken.Subject))
	}
	if user == "" {
		return auth.Identity{}, errors.New("neither preferred_username, name, nickname nor sub was present")
	}

	// Absent groups claim is not an error — every issuer that sends none
	// (Google, or Auth0 before its groups Action exists) falls through to
	// default_role, which is what that setting is for. But when it stays
	// silent, a deployment with required_group set (auth.RequiredGroup)
	// locks everyone out with nothing but "not a member of X" — the token
	// itself, the one place that would explain why, is gone by the time
	// Authorize runs a request later, since only the resolved Identity
	// survives into the session. So this is the one place to log it: a
	// line naming the configured claim key and every top-level claim key
	// the token actually carried, specific enough on its own to settle
	// whether the fix is "assign the role in the IdP" or "the
	// groups_claim config doesn't match what the IdP actually sends."
	// Warn, not Debug — main.go's handler is built at LevelInfo with no
	// way to turn Debug on short of a redeploy, and a login that resolves
	// no groups is exactly the kind of anomaly Warn exists for elsewhere
	// in this file (the nonce-mismatch case just above logs the same way).
	groupsClaim := s.authenticator().OIDC().GroupsClaim
	groups := stringSliceClaim(claims, groupsClaim)
	if len(groups) == 0 {
		s.logger().Warn("oidc token resolved no groups",
			"user", user, "configured_claim", groupsClaim, "token_claim_keys", claimKeys(claims))
	}
	return auth.Identity{
		User:   user,
		Name:   strings.TrimSpace(stringClaim(claims, "name")),
		Email:  strings.TrimSpace(stringClaim(claims, "email")),
		Groups: groups,
		Sub:    strings.TrimSpace(idToken.Subject),
	}, nil
}

// claimKeys lists a token's top-level claim names, sorted — never values,
// so this is safe to log unconditionally: some claims (email, sub) are
// identifying, but their names never are.
func claimKeys(claims map[string]any) []string {
	keys := make([]string, 0, len(claims))
	for k := range claims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func stringClaim(claims map[string]any, key string) string {
	s, _ := claims[key].(string)
	return s
}

func stringSliceClaim(claims map[string]any, key string) []string {
	raw, ok := claims[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// setStateCookie seals st and sets it as auth.OIDCStateCookie.
func (s *Server) setStateCookie(w http.ResponseWriter, r *http.Request, st oidcState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	sealed, err := s.Box.Seal(string(raw))
	if err != nil {
		return err
	}
	// #nosec G124 -- see the note on the session cookie above.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.OIDCStateCookie,
		Value:    base64.RawURLEncoding.EncodeToString(sealed),
		Path:     "/sso/",
		MaxAge:   600, // 10 minutes: long enough for a slow login, short enough not to matter if abandoned
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode, // the callback is a cross-site top-level GET; Strict would drop this cookie before the handler ever saw it
	})
	return nil
}

// openStateCookie reads and unseals auth.OIDCStateCookie. Any failure —
// missing cookie, undecodable, tampered, unparseable — is reported
// identically to the caller: the sign-in cannot be trusted, start over.
func (s *Server) openStateCookie(r *http.Request) (oidcState, error) {
	cookie, err := r.Cookie(auth.OIDCStateCookie)
	if err != nil {
		return oidcState{}, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return oidcState{}, err
	}
	raw, err := s.Box.Open(sealed)
	if err != nil {
		return oidcState{}, err
	}
	var st oidcState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return oidcState{}, err
	}
	return st, nil
}

func (s *Server) clearStateCookie(w http.ResponseWriter, r *http.Request) {
	// #nosec G124 -- see the note on the session cookie above.
	http.SetCookie(w, &http.Cookie{
		Name: auth.OIDCStateCookie, Value: "", Path: "/sso/", MaxAge: -1,
		HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

// safeReturnTo rejects anything that is not a path on this site. Without
// this, ?return_to= on /sso/login would be an open redirect: the state
// cookie is trusted, and an attacker cannot forge it, but they can still
// hand a victim a login link that legitimately signs them in and then sends
// them somewhere else entirely.
func safeReturnTo(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "", errors.New("return_to must be a path on this site")
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "", errors.New("return_to must be a path on this site")
	}
	return raw, nil
}

// withNotice appends (or replaces) a "notice" query param on path — App.vue
// picks it up on mount, shows it as a toast, and strips it from the URL bar.
// path is always already safeReturnTo-validated by the caller here, so this
// only has to add a param, not re-validate the path itself.
func withNotice(path, notice string) string {
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	q := u.Query()
	q.Set("notice", notice)
	u.RawQuery = q.Encode()
	return u.String()
}

// redirectURLForRequest picks which of the (at most two) registered
// redirect_uris applies to r: auth.oidc.preview_redirect_url if r's own
// Host exactly matches that URL's host, auth.oidc.redirect_url otherwise.
// Called identically from both handleSSOLogin (to tell the issuer where to
// send the browser back) and handleSSOCallback (to tell the issuer's token
// endpoint which redirect_uri the exchange should be validated against) —
// they agree by construction, since the issuer always redirects back to
// literally the URI it was given, so the callback request's own Host
// already matches whichever one handleSSOLogin chose.
//
// Never derived from an arbitrary Host — matched only against this one
// additional, explicitly configured value. A request cannot talk the app
// into handing the issuer a redirect_uri nobody registered: the only two
// possible results are both server-side config, and both already
// registered with the issuer as valid callback URLs, so there is nothing
// here for a spoofed Host header to redirect a login to that was not
// already one of this app's own legitimate landing spots.
func (s *Server) redirectURLForRequest(r *http.Request) string {
	cfg := s.authenticator().OIDC()
	if cfg.PreviewRedirectURL == "" {
		return cfg.RedirectURL
	}
	preview, err := url.Parse(cfg.PreviewRedirectURL)
	if err != nil || !strings.EqualFold(r.Host, preview.Host) {
		return cfg.RedirectURL
	}
	return cfg.PreviewRedirectURL
}

// requestIsHTTPS decides the cookie Secure attribute. Traefik terminates TLS
// in front of the pod and sets X-Forwarded-Proto, so production cookies are
// still Secure; a local dev loop against a test issuer over plain HTTP still
// works without a config flag to remember.
func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// requestOrigin is scheme://host for the request that reached this server —
// used only as the default post-logout landing place, never trusted for
// anything security-relevant (the state cookie is what carries trust here).
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
