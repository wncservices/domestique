package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

// garminProvider is this provider's key in the shared connection store.
const garminProvider = "garmin"

type garminConnectionDTO struct {
	Connected   bool   `json:"connected"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	// ExpiresAt is when the stored session stops working. Garmin's long-lived
	// token lasts about a year and then everything quietly fails, so the date
	// is shown rather than waited for.
	ExpiresAt string `json:"expiresAt,omitempty"`
	Expired   bool   `json:"expired,omitempty"`
	// CanConnect is false when a sign-in could not be stored or completed.
	CanConnect bool `json:"canConnect"`
	// Unavailable says which of those it is, in words a person can act on.
	Unavailable string `json:"unavailable,omitempty"`
	// Consumer is set when the thing standing in the way is the missing
	// OAuth1 consumer, which an admin can supply from the UI. Absent
	// otherwise, so the form appears only where it would help.
	Consumer *garminConsumerDTO `json:"consumer,omitempty"`
}

// handleGarminConnection reports the caller's own connection.
func (s *Server) handleGarminConnection(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}
	writeJSON(w, http.StatusOK, s.garminConnectionDTO(r))
}

// garminConnectionDTO describes the caller's Garmin connection, if any.
func (s *Server) garminConnectionDTO(r *http.Request) garminConnectionDTO {
	dto := garminConnectionDTO{}

	consumer, _ := s.garminConsumer()
	switch {
	case s.Garmin == nil:
		dto.Unavailable = "this deployment has no Garmin sign-in configured"
	case !s.Links.CanStore():
		dto.Unavailable = "this deployment cannot store a Garmin connection: " + secrets.ErrNoKey.Error()
	case !consumer.Configured():
		dto.Unavailable = "Garmin has not been set up on this deployment yet"
	default:
		dto.CanConnect = true
	}

	// Only an admin ever sees the consumer. A rider cannot set it, cannot act
	// on knowing it is missing, and has no reason to learn that Garmin app
	// keys are a thing that exists — they get "not set up yet" and someone to
	// ask. An admin gets it whether or not it is configured, because a pair
	// that turned out to be wrong has to be replaceable.
	if auth.FromContext(r.Context()).Role.Can(auth.PermManageSettings) {
		consumerDTO := s.garminConsumerDTOFor(r)
		dto.Consumer = &consumerDTO
	}

	rider := auth.FromContext(r.Context()).User
	if s.Links == nil || rider == "" {
		return dto
	}

	link, err := s.Links.Get(garminProvider, rider)
	if err != nil {
		return dto
	}

	dto.Connected = true
	dto.Email = link.Email
	dto.DisplayName = link.DisplayName
	dto.UpdatedAt = link.UpdatedAt.UTC().Format(time.RFC3339)

	// The stored session carries no expiry of its own; it is a year from when
	// it was obtained, and UpdatedAt is when that was.
	expiry := garmin.Session{ObtainedAt: link.UpdatedAt}.TokenExpiry()
	dto.ExpiresAt = expiry.UTC().Format(time.RFC3339)
	dto.Expired = time.Now().After(expiry)
	return dto
}

// handleGarminConnect signs in and stores the session.
func (s *Server) handleGarminConnect(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}
	if s.Garmin == nil {
		// Error, not warn: main.go wires srv.Garmin unconditionally, so nil
		// here means that wiring broke, not that an admin left it off.
		s.logger().Error("garmin connect requested but no Garmin client is wired — this should never happen outside tests")
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "this deployment has no Garmin sign-in configured",
		})
		return
	}
	consumer, _ := s.garminConsumer()
	if !consumer.Configured() {
		// Before the password is asked for, let alone sent.
		s.logger().Warn("garmin connect requested but no OAuth1 consumer is configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": garmin.ErrNoConsumer.Error(),
		})
		return
	}
	if !s.Links.CanStore() {
		// Refusing is the whole point: without a key the only way to honour
		// this request would be to write the session somewhere readable.
		s.logger().Warn("garmin connect requested but this deployment has no encryption key configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment cannot store a Garmin connection: " + secrets.ErrNoKey.Error(),
		})
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" || body.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "email and password are both required",
		})
		return
	}

	// The rider comes from the session, never the body — the same rule as
	// linking a head unit, and for the same reason.
	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "no rider in the session to attach the connection to",
		})
		return
	}
	if !s.rateLimitConnect(w, rider) {
		return
	}

	session, err := s.Garmin.Connect(r.Context(), consumer, body.Email, body.Password)
	if err != nil {
		s.writeGarminLoginError(w, rider, err)
		return
	}

	// The session is two tokens plus when they were issued, so it is stored as
	// JSON rather than as one opaque string. providerlink neither knows nor
	// cares what is inside.
	sealed, err := json.Marshal(session)
	if err != nil {
		s.logger().Error("encoding the garmin session failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not store the connection",
		})
		return
	}

	if _, err := s.Links.Save(garminProvider, rider, providerlink.Connection{
		Email:       body.Email,
		DisplayName: session.DisplayName,
		Secret:      string(sealed),
	}); err != nil {
		s.logger().Error("storing garmin connection failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not store the connection",
		})
		return
	}

	// Signing in *is* linking the head unit. Asking a rider to sign in and
	// then separately add a Garmin as a push target would be two steps for one
	// intention, and would leave a linked account with no way to reach it.
	s.ensureAccount(rider, model.ProviderGarmin, session.DisplayName)

	s.logger().Info("garmin connected", "rider", rider, "account", session.DisplayName)
	writeJSON(w, http.StatusOK, s.garminConnectionDTO(r))
}

// writeGarminLoginError says which of the four sign-in failures happened.
//
// They need different words, and only one of them is the password. An MFA
// challenge cannot be answered by this flow at all. A Cloudflare block never
// reached Garmin, so it says nothing about the account. Anything else is
// Garmin having a bad day. Reporting them all as "sign-in failed" is how
// somebody ends up resetting a password that was never wrong.
func (s *Server) writeGarminLoginError(w http.ResponseWriter, rider string, err error) {
	// Logged without the password and without the upstream body, which can
	// echo the request. The error text is safe and worth having: `reason` is
	// one of four words, and on its own it cannot say whether "credentials"
	// meant a rejected password or a response shape we stopped recognising.
	// That distinction decides whether the rider retypes something or somebody
	// reads Garmin's HTML again, and guessing wrong wastes an afternoon.
	s.logger().Warn("garmin connect failed",
		"rider", rider, "reason", classifyGarminError(err), "detail", err.Error())

	switch {
	case errors.Is(err, garmin.ErrBlocked):
		// 503, not 401: nothing is wrong with the account and nothing the
		// rider types will change the outcome right now.
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "Garmin's protection blocked the sign-in before it reached them. " +
				"This usually follows several failed attempts — leave it a while and try again.",
			"blocked": true,
		})
	case errors.Is(err, garmin.ErrMFARequired):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "This Garmin account uses two-factor authentication, which this sign-in cannot complete.",
			"mfa":   true,
		})
	case errors.Is(err, garmin.ErrBadCredentials):
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Garmin did not accept those details",
		})
	case errors.Is(err, garmin.ErrNoConsumer):
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Garmin could not be signed in to just now — try again later",
		})
	}
}

func classifyGarminError(err error) string {
	switch {
	case errors.Is(err, garmin.ErrBlocked):
		return "blocked"
	case errors.Is(err, garmin.ErrMFARequired):
		return "mfa"
	case errors.Is(err, garmin.ErrBadCredentials):
		return "credentials"
	case errors.Is(err, garmin.ErrNoConsumer):
		return "no-consumer"
	default:
		return "upstream"
	}
}

// handleGarminDisconnect forgets the caller's connection.
func (s *Server) handleGarminDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}

	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no rider in the session"})
		return
	}
	if s.Links == nil {
		writeJSON(w, http.StatusOK, garminConnectionDTO{})
		return
	}

	if err := s.Links.Delete(garminProvider, rider); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// The head unit goes with the sign-in. Leaving it linked would leave a
	// push target there is no longer any way to reach, which shows up as a
	// failing sync rather than as the disconnection the rider asked for.
	if s.Accounts != nil {
		if err := s.Accounts.Unlink(accounts.ID(model.ProviderGarmin, rider)); err != nil &&
			!errors.Is(err, accounts.ErrNotFound) {
			s.logger().Warn("unlinking the garmin head unit failed", "rider", rider, "err", err)
		}
	}

	s.logger().Info("garmin disconnected", "rider", rider)
	writeJSON(w, http.StatusOK, s.garminConnectionDTO(r))
}

// ensureAccount links a head unit for a rider who has just signed in, and says
// nothing if they already had one. Failing to link is logged rather than
// returned: the connection itself was stored, and reporting the sign-in as
// failed would invite the rider to do it again to no effect.
func (s *Server) ensureAccount(rider string, provider model.Provider, label string) {
	if s.Accounts == nil {
		return
	}
	if label == "" {
		label = string(provider)
	}

	switch _, err := s.Accounts.Link(provider, rider, label); {
	case err == nil, errors.Is(err, accounts.ErrExists):
	default:
		s.logger().Warn("linking the head unit after sign-in failed",
			"rider", rider, "provider", provider, "err", err)
	}
}

// Reading the session back belongs to the push adapter, which is the only
// caller that needs it: providerlink.Secret(garminProvider, rider) returns the
// JSON above. It is not here yet because nothing pushes yet, and a decrypt
// path with no caller is a decrypt path nothing exercises.

// handleGarminDevices lists the head units on the caller's own account.
//
// Informational, and worth being clear about why it exists: linking a Garmin
// account tells a rider nothing about whether their Edge will actually see a
// course. Their devices, named, answer that. A course is pushed to the
// account and Connect syncs it to whichever units can take it — so this is
// not a list to choose from, it is a list of who is listening.
func (s *Server) handleGarminDevices(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}
	if s.Garmin == nil || s.Links == nil {
		writeJSON(w, http.StatusOK, []garmin.Device{})
		return
	}

	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusOK, []garmin.Device{})
		return
	}

	_, secret, err := s.Links.Secret(garminProvider, rider)
	if err != nil {
		// Not connected, or nothing readable. Neither is an error worth a
		// screenful: there is simply nothing to list.
		writeJSON(w, http.StatusOK, []garmin.Device{})
		return
	}

	var session garmin.Session
	if err := json.Unmarshal([]byte(secret), &session); err != nil {
		s.logger().Warn("stored garmin session is unreadable", "rider", rider, "err", err)
		writeJSON(w, http.StatusOK, []garmin.Device{})
		return
	}

	consumer, _ := s.garminConsumer()
	devices, err := s.Garmin.Devices(r.Context(), consumer, session)
	if err != nil {
		// An undocumented endpoint that moved is Garmin's problem, not a
		// fault in the connection — which still works for everything else.
		s.logger().Warn("garmin device list failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Garmin would not list the devices on this account just now.",
		})
		return
	}

	writeJSON(w, http.StatusOK, devices)
}
