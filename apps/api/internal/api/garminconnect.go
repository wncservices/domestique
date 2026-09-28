package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/garminmfa"
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

// garminConnectReady runs the checks both sign-in steps share, before a
// password or a code is read, let alone sent. It writes the refusal itself.
func (s *Server) garminConnectReady(w http.ResponseWriter, r *http.Request) (GarminConsumer, bool) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return GarminConsumer{}, false
	}
	if s.Garmin == nil {
		// Error, not warn: main.go wires srv.Garmin unconditionally, so nil
		// here means that wiring broke, not that an admin left it off.
		s.logger().Error("garmin connect requested but no Garmin client is wired — this should never happen outside tests")
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "this deployment has no Garmin sign-in configured",
		})
		return GarminConsumer{}, false
	}
	consumer, _ := s.garminConsumer()
	if !consumer.Configured() {
		// Before the password is asked for, let alone sent.
		s.logger().Warn("garmin connect requested but no OAuth1 consumer is configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": garmin.ErrNoConsumer.Error(),
		})
		return GarminConsumer{}, false
	}
	if !s.Links.CanStore() {
		// Refusing is the whole point: without a key the only way to honour
		// this request would be to write the session somewhere readable.
		s.logger().Warn("garmin connect requested but this deployment has no encryption key configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment cannot store a Garmin connection: " + secrets.ErrNoKey.Error(),
		})
		return GarminConsumer{}, false
	}
	return consumer, true
}

// handleGarminConnect signs in and stores the session.
//
// For an account with two-factor on this is only the first of two requests:
// it answers 409 with a challenge id, and handleGarminConnectMFA finishes it.
func (s *Server) handleGarminConnect(w http.ResponseWriter, r *http.Request) {
	consumer, ok := s.garminConnectReady(w, r)
	if !ok {
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
		s.writeGarminLoginError(w, r, rider, err)
		return
	}

	s.completeGarminConnect(w, r, rider, body.Email, session)
}

// completeGarminConnect stores a signed-in session and links the head unit —
// the one success path both sign-in steps end in, so a code-completed sign-in
// cannot drift from a password-only one.
func (s *Server) completeGarminConnect(w http.ResponseWriter, r *http.Request, rider, email string, session garmin.Session) {
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
		Email:       email,
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
	s.ensureAccount(r.Context(), rider, model.ProviderGarmin, session.DisplayName)

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
func (s *Server) writeGarminLoginError(w http.ResponseWriter, r *http.Request, rider string, err error) {
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
		s.writeGarminMFARequired(w, r, rider, err)
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

// mfaRequiredMessage is today's dead-end copy. It stays until the two-factor
// flow has been checked against a real account (see the design's release
// gate); the UI keys off the JSON flags, not this text.
const mfaRequiredMessage = "This Garmin account uses two-factor authentication, which this sign-in cannot complete."

// writeGarminMFARequired answers step one when Garmin asked for a code: it
// keeps the mid-flight state (never the password) behind an opaque id and
// hands the id back. With nothing to resume from — no store, no key, or state
// Garmin's page did not give enough of — it is the bare 409 it always was.
func (s *Server) writeGarminMFARequired(w http.ResponseWriter, r *http.Request, rider string, err error) {
	// The bare answer is a deployment or parsing gap, not the regular path, so
	// it says which — reasons only, never values.
	bare := func(reason string) {
		s.logger().Warn("garmin mfa challenge not kept; answering without a way to enter the code",
			"rider", rider, "reason", reason)
		writeJSON(w, http.StatusConflict, map[string]any{"error": mfaRequiredMessage, "mfa": true})
	}

	var challenged *GarminMFAChallengeError
	switch {
	case s.GarminMFA == nil:
		bare("no-store")
		return
	case !s.GarminMFA.CanStore():
		bare("no-key")
		return
	case !errors.As(err, &challenged) || challenged.Challenge.CSRF == "":
		// Nothing to resume from: no state came back, or the challenge page
		// had no token to answer with.
		bare("no-csrf")
		return
	}

	id, createErr := s.GarminMFA.Create(r.Context(), rider, challenged.Challenge, garminmfa.DefaultTTL)
	switch {
	case errors.Is(createErr, garminmfa.ErrTooLarge):
		// Size only: the state is cookies from a third party.
		s.logger().Warn("garmin mfa challenge refused: state too large", "rider", rider, "detail", createErr.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Garmin could not be signed in to just now — try again later",
		})
		return
	case createErr != nil:
		s.logger().Error("storing the garmin mfa challenge failed", "rider", rider, "err", createErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not start the two-factor sign-in",
		})
		return
	}

	// Info: the regular, expected path for any two-factor account. The id is
	// not logged — it is the bearer value for the second step.
	s.logger().Info("garmin mfa challenge created", "rider", rider, "method", challenged.Challenge.Method)
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":     mfaRequiredMessage,
		"mfa":       true,
		"challenge": id,
		"method":    challenged.Challenge.Method,
	})
}

func classifyGarminError(err error) string {
	switch {
	case errors.Is(err, garmin.ErrMFACodeRejected):
		return "mfa-wrong"
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
		if err := s.Accounts.Unlink(r.Context(), accounts.ID(model.ProviderGarmin, rider)); err != nil &&
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
func (s *Server) ensureAccount(ctx context.Context, rider string, provider model.Provider, label string) {
	if s.Accounts == nil {
		return
	}
	if label == "" {
		label = string(provider)
	}

	switch _, err := s.Accounts.Link(ctx, provider, rider, label); {
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

// handleGarminConnectMFA is the second step of a two-factor sign-in: the
// rider's code, and the challenge id step one handed back.
//
// The challenge is only ever the caller's own — the rider comes from the
// session, and someone else's id is a 404, the same answer as an id that never
// existed. Nothing here logs the code, a cookie or the challenge id.
func (s *Server) handleGarminConnectMFA(w http.ResponseWriter, r *http.Request) {
	consumer, ok := s.garminConnectReady(w, r)
	if !ok {
		return
	}
	if !s.GarminMFA.CanStore() {
		s.logger().Warn("garmin mfa code submitted but this deployment cannot hold a challenge")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment cannot complete a two-factor sign-in",
		})
		return
	}

	var body struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	body.Code = strings.TrimSpace(body.Code)
	if body.Challenge == "" || body.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "the challenge and the code are both required",
		})
		return
	}

	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "no rider in the session to attach the connection to",
		})
		return
	}
	if !rateLimit(w, s.GarminMFALimiter, rider, "too many two-factor attempts — wait a few minutes and try again") {
		return
	}

	ctx := r.Context()
	challenge, err := s.GarminMFA.Resolve(ctx, rider, body.Challenge)
	switch {
	case errors.Is(err, garminmfa.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such sign-in in progress"})
		return
	case errors.Is(err, garminmfa.ErrExpired):
		s.logger().Warn("garmin mfa challenge expired before the code arrived", "rider", rider, "reason", "mfa-expired")
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this code has expired — sign in again"})
		return
	case err != nil:
		s.logger().Error("reading the garmin mfa challenge failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the sign-in in progress"})
		return
	}

	// The attempt is taken before Garmin is asked, atomically, so N requests in
	// flight together cannot each get a look at the code. A refused reserve is
	// the same answer as a spent challenge.
	used, err := s.GarminMFA.Reserve(ctx, rider, body.Challenge)
	switch {
	case errors.Is(err, garminmfa.ErrAttemptsExhausted):
		s.logger().Warn("garmin mfa attempts exhausted", "rider", rider, "reason", "mfa-exhausted")
		writeJSON(w, http.StatusConflict, map[string]string{"error": "too many wrong codes — sign in again"})
		return
	case errors.Is(err, garminmfa.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such sign-in in progress"})
		return
	case err != nil:
		s.logger().Error("reserving a garmin mfa attempt failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the sign-in in progress"})
		return
	}

	session, refreshed, err := s.Garmin.ResumeMFA(ctx, consumer, challenge, body.Code)
	if errors.Is(err, garmin.ErrMFACodeRejected) {
		// A wrong code keeps its attempt.
		s.writeGarminMFAWrongCode(w, r, rider, body.Challenge, used, refreshed)
		return
	}
	if err != nil {
		// Blocked, an unrecognised page, an outage: none of them is the
		// rider's mistake, so the attempt goes back.
		if refundErr := s.GarminMFA.Refund(ctx, rider, body.Challenge); refundErr != nil {
			s.logger().Warn("refunding a garmin mfa attempt failed", "rider", rider, "err", refundErr)
		}
		s.writeGarminLoginError(w, r, rider, err)
		return
	}

	// Spent the moment it works, before anything that could fail, so a
	// challenge can never sign in twice. Error, not Warn: a spent challenge
	// left in the table is replayable for the rest of its TTL, which should
	// not be happening. It still returns success — Garmin has accepted the
	// code and completeGarminConnect below saves the link, so failing the
	// request would tell the rider a working sign-in failed.
	if err := s.GarminMFA.Consume(ctx, rider, body.Challenge); err != nil {
		s.logger().Error("deleting a used garmin mfa challenge failed", "rider", rider, "err", err)
	}
	s.completeGarminConnect(w, r, rider, challenge.Email, session)
}

// writeGarminMFAWrongCode answers a rejected code. The attempt was already
// taken by Reserve. At the cap the challenge is deleted and the answer is 409;
// otherwise the refreshed state Garmin's re-rendered page gave (a new CSRF, moved
// cookies) is re-sealed into the same row, and the answer is 422 with the same
// challenge id so the UI keeps the code field open.
func (s *Server) writeGarminMFAWrongCode(w http.ResponseWriter, r *http.Request, rider, id string, used int, refreshed garmin.MFAChallenge) {
	ctx := r.Context()
	s.logger().Warn("garmin mfa code rejected", "rider", rider, "reason", classifyGarminError(garmin.ErrMFACodeRejected))

	if used >= garminmfa.MaxAttempts {
		if err := s.GarminMFA.Consume(ctx, rider, id); err != nil {
			s.logger().Error("deleting an exhausted garmin mfa challenge failed", "rider", rider, "err", err)
		}
		s.logger().Warn("garmin mfa attempts exhausted", "rider", rider, "reason", "mfa-exhausted")
		writeJSON(w, http.StatusConflict, map[string]string{"error": "too many wrong codes — sign in again"})
		return
	}

	// Best effort: if the refreshed state cannot be kept the old one stays,
	// which may be stale, but the rider can still retry or restart.
	if err := s.GarminMFA.Update(ctx, rider, id, refreshed); err != nil {
		s.logger().Warn("keeping the refreshed garmin mfa state failed", "rider", rider, "err", err)
	}

	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":             "That code didn't work — check it and try again",
		"mfaInvalid":        true,
		"challenge":         id,
		"attemptsRemaining": garminmfa.MaxAttempts - used,
	})
}
