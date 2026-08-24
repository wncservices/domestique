package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/oidcflow"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

// wahooProvider is this provider's key in the shared connection store.
const wahooProvider = "wahoo"

// wahooStateCookie carries the CSRF state and the rider it belongs to
// between /wahoo/connect and /wahoo/callback — a separate cookie from
// auth.OIDCStateCookie, since this is linking a provider account to an
// already-signed-in rider, not signing anyone in.
const wahooStateCookie = "domestique_wahoo_state"

// wahooState is what /wahoo/connect seals into wahooStateCookie and
// /wahoo/callback opens. Rider is bound in here rather than trusted from
// anywhere in the callback request, for the same reason the rider on a
// Garmin/Komoot connect never comes from the request body: the callback
// arrives after a cross-site redirect, and only the sealed cookie — set
// while the rider was still authenticated on this app — can be trusted to
// say who this connection is for.
type wahooState struct {
	State    string    `json:"state"`
	Rider    string    `json:"rider"`
	ReturnTo string    `json:"returnTo,omitempty"`
	IssuedAt time.Time `json:"issuedAt"`
}

type wahooConnectionDTO struct {
	Connected   bool   `json:"connected"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	Expired     bool   `json:"expired,omitempty"`
	// CanConnect is false when a connection could not be stored or
	// completed — no encryption key, or this deployment has no Wahoo app
	// credentials configured.
	CanConnect bool `json:"canConnect"`
	// Unavailable says which of those it is, in words a person can act on.
	Unavailable string `json:"unavailable,omitempty"`
}

// handleWahooConnection reports the caller's own connection.
func (s *Server) handleWahooConnection(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}
	writeJSON(w, http.StatusOK, s.wahooConnectionDTO(r))
}

func (s *Server) wahooConnectionDTO(r *http.Request) wahooConnectionDTO {
	dto := wahooConnectionDTO{}

	switch {
	case s.Wahoo == nil:
		dto.Unavailable = "this deployment has no Wahoo app credentials configured"
	case !s.Links.CanStore():
		dto.Unavailable = "this deployment cannot store a Wahoo connection: " + secrets.ErrNoKey.Error()
	default:
		dto.CanConnect = true
	}

	rider := auth.FromContext(r.Context()).User
	if s.Links == nil || rider == "" {
		return dto
	}

	link, err := s.Links.Get(wahooProvider, rider)
	if err != nil {
		return dto
	}

	dto.Connected = true
	dto.Email = link.Email
	dto.DisplayName = link.DisplayName
	dto.UpdatedAt = link.UpdatedAt.UTC().Format(time.RFC3339)

	// The expiry lives in the stored session, unlike Garmin's guessed
	// one-year window — an OAuth2 token says exactly when it stops working.
	_, secret, err := s.Links.Secret(wahooProvider, rider)
	if err == nil {
		var session wahooSession
		if json.Unmarshal([]byte(secret), &session) == nil {
			dto.ExpiresAt = session.ExpiresAt.UTC().Format(time.RFC3339)
			dto.Expired = time.Now().After(session.ExpiresAt)
		}
	}
	return dto
}

// wahooSession mirrors wahoo.Session's JSON shape, duplicated rather than
// imported so this file only ever reads the two fields it needs (ExpiresAt
// here; AccessToken/RefreshToken belong to the push adapter, not yet
// written) without pulling the whole wahoo package's exported surface into
// every place that peeks at a stored connection's expiry.
type wahooSession struct {
	ExpiresAt time.Time `json:"expires_at"`
}

// handleWahooConnect starts the flow: seals state and the caller's rider
// into a short-lived cookie, and redirects to Wahoo.
func (s *Server) handleWahooConnect(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}
	if s.Wahoo == nil {
		s.logger().Warn("wahoo connect requested but not configured for this deployment")
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "this deployment has no Wahoo app credentials configured",
		})
		return
	}
	if !s.Links.CanStore() {
		s.logger().Warn("wahoo connect requested but this deployment has no encryption key configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment cannot store a Wahoo connection: " + secrets.ErrNoKey.Error(),
		})
		return
	}

	// The rider comes from the session, never the body or a query param —
	// the same rule as linking a head unit, and for the same reason.
	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "sign in before connecting a Wahoo account",
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

	if err := s.setWahooStateCookie(w, r, wahooState{
		State: state, Rider: rider, ReturnTo: returnTo, IssuedAt: time.Now().UTC(),
	}); err != nil {
		s.fail(w, err)
		return
	}

	http.Redirect(w, r, s.Wahoo.AuthCodeURL(state), http.StatusFound)
}

// handleWahooCallback verifies what Wahoo sent back and, if it checks out,
// stores the connection.
func (s *Server) handleWahooCallback(w http.ResponseWriter, r *http.Request) {
	if s.Wahoo == nil {
		// handleWahooConnect above already refuses to start this flow when
		// Wahoo isn't configured, so a callback landing here means either a
		// config change mid-flow or a stale/forged callback URL — unusual,
		// still not broken, still just a warning.
		s.logger().Warn("wahoo callback received but not configured for this deployment")
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "this deployment has no Wahoo app credentials configured",
		})
		return
	}

	st, err := s.openWahooStateCookie(r)
	// Single-use regardless of outcome, same rule as the OIDC state cookie:
	// a failed callback must not leave something a client could replay.
	s.clearWahooStateCookie(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing or expired connection attempt — start over at /wahoo/connect",
		})
		return
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		s.logger().Warn("wahoo callback reported by wahoo", "error", errParam, "description", desc)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "the connection was not completed: " + errParam,
		})
		return
	}

	if got := r.URL.Query().Get("state"); got == "" || got != st.State {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "state did not match — start over"})
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "wahoo sent no code"})
		return
	}

	session, err := s.Wahoo.Exchange(r.Context(), code)
	if err != nil {
		s.logger().Warn("wahoo token exchange failed", "rider", st.Rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "wahoo did not accept the connection",
		})
		return
	}

	profile, err := s.Wahoo.Me(r.Context(), session.AccessToken)
	if err != nil {
		// The token itself is good — only the profile call failed. Worth
		// storing the connection anyway rather than discarding a working
		// token over a display name; email/display name are left blank.
		s.logger().Warn("wahoo profile fetch failed", "rider", st.Rider, "err", err)
	}

	// #nosec G117 -- gosec flags AccessToken as a marshaled secret-looking
	// field, but this JSON exists only long enough to reach
	// providerlink.Save two lines down, which seals it with the app's
	// AES-256-GCM key before it ever reaches the database — the same shape
	// garmin.Session's OAuth1Token/OAuth1Secret already take, just with
	// field names gosec's pattern happens to match.
	sealed, err := json.Marshal(session)
	if err != nil {
		s.logger().Error("encoding the wahoo session failed", "rider", st.Rider, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not store the connection",
		})
		return
	}

	if _, err := s.Links.Save(wahooProvider, st.Rider, providerlink.Connection{
		Email:       profile.Email,
		DisplayName: profile.DisplayName(),
		Secret:      string(sealed),
	}); err != nil {
		s.logger().Error("storing wahoo connection failed", "rider", st.Rider, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not store the connection",
		})
		return
	}

	// Connecting is linking the head unit, the same rule as Garmin/Komoot:
	// one intention, one step, rather than a second trip to add a push
	// target for a connection that now exists but reaches nothing.
	s.ensureAccount(st.Rider, model.ProviderWahoo, profile.DisplayName())

	s.logger().Info("wahoo connected", "rider", st.Rider)

	returnTo := st.ReturnTo
	if returnTo == "" {
		returnTo = "/"
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// handleWahooDisconnect forgets the caller's connection.
func (s *Server) handleWahooDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}

	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no rider in the session"})
		return
	}
	if s.Links == nil {
		writeJSON(w, http.StatusOK, wahooConnectionDTO{})
		return
	}

	if err := s.Links.Delete(wahooProvider, rider); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if s.Accounts != nil {
		if err := s.Accounts.Unlink(accounts.ID(model.ProviderWahoo, rider)); err != nil &&
			!errors.Is(err, accounts.ErrNotFound) {
			s.logger().Warn("unlinking the wahoo head unit failed", "rider", rider, "err", err)
		}
	}

	s.logger().Info("wahoo disconnected", "rider", rider)
	writeJSON(w, http.StatusOK, s.wahooConnectionDTO(r))
}

// setWahooStateCookie seals st and sets it as wahooStateCookie.
func (s *Server) setWahooStateCookie(w http.ResponseWriter, r *http.Request, st wahooState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	sealed, err := s.Box.Seal(string(raw))
	if err != nil {
		return err
	}
	// #nosec G124 -- Secure, HttpOnly and SameSite are all set; gosec wants
	// Secure to be the literal `true` and cannot see through requestIsHTTPS,
	// which is conditional on purpose — see its own doc comment in sso.go.
	http.SetCookie(w, &http.Cookie{
		Name:     wahooStateCookie,
		Value:    base64.RawURLEncoding.EncodeToString(sealed),
		Path:     "/wahoo/",
		MaxAge:   600, // 10 minutes: long enough to complete Wahoo's consent screen
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode, // the callback is a cross-site top-level GET
	})
	return nil
}

// openWahooStateCookie reads and unseals wahooStateCookie. Any failure —
// missing, undecodable, tampered, unparseable — is reported identically:
// the connection attempt cannot be trusted, start over.
func (s *Server) openWahooStateCookie(r *http.Request) (wahooState, error) {
	cookie, err := r.Cookie(wahooStateCookie)
	if err != nil {
		return wahooState{}, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return wahooState{}, err
	}
	raw, err := s.Box.Open(sealed)
	if err != nil {
		return wahooState{}, err
	}
	var st wahooState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return wahooState{}, err
	}
	return st, nil
}

func (s *Server) clearWahooStateCookie(w http.ResponseWriter, r *http.Request) {
	// #nosec G124 -- see the note on setWahooStateCookie above.
	http.SetCookie(w, &http.Cookie{
		Name: wahooStateCookie, Value: "", Path: "/wahoo/", MaxAge: -1,
		HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}
