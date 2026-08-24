package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/settings"
)

// Setting names for the Garmin OAuth1 consumer.
const (
	SettingGarminConsumerKey = "garmin.oauth_consumer_key"
	// #nosec G101 -- the *name* of a setting, not a credential.
	SettingGarminConsumerSecret = "garmin.oauth_consumer_secret"
)

// Where a consumer pair came from, for the UI to say so.
const (
	consumerFromSettings    = "settings"
	consumerFromEnvironment = "environment"
)

// garminConsumer resolves the pair to sign with.
//
// **What an admin set here wins over the environment.** Both are deliberate
// acts, but the one performed in the UI is the more recent and the more
// specific, and silently ignoring somebody who has just pasted a key in and
// pressed Save is the worse surprise of the two. The UI names the source, and
// clearing the stored pair falls back to the environment.
func (s *Server) garminConsumer() (consumer GarminConsumer, source string) {
	if s.Settings.CanStore() {
		key, keyErr := s.Settings.Get(SettingGarminConsumerKey)
		secret, secretErr := s.Settings.Get(SettingGarminConsumerSecret)
		switch {
		case keyErr == nil && secretErr == nil:
			return GarminConsumer{Key: key, Secret: secret}, consumerFromSettings
		case keyErr != nil && !errors.Is(keyErr, settings.ErrNotFound):
			// Undecryptable, usually a rotated encryption key. Say so rather
			// than falling through to the environment silently: the admin
			// needs to know theirs stopped being readable.
			s.logger().Error("stored garmin consumer unusable", "err", keyErr)
		case secretErr != nil && !errors.Is(secretErr, settings.ErrNotFound):
			s.logger().Error("stored garmin consumer unusable", "err", secretErr)
		}
	}

	if key, secret, ok := garmin.ConsumerFromEnv(); ok {
		return GarminConsumer{Key: key, Secret: secret}, consumerFromEnvironment
	}
	return GarminConsumer{}, ""
}

type garminConsumerDTO struct {
	// Configured says a pair is available, without saying what it is. The
	// value never leaves the process.
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
	UpdatedBy  string `json:"updatedBy,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
	// CanManage is whether *this caller* may set it here — admin, and a place
	// to keep it. False hides the form rather than offering a 403.
	CanManage bool `json:"canManage"`
	// Why CanManage is false, when it is worth explaining.
	Unavailable string `json:"unavailable,omitempty"`
}

// garminConsumerDTOFor describes the consumer for this caller.
func (s *Server) garminConsumerDTOFor(r *http.Request) garminConsumerDTO {
	consumer, source := s.garminConsumer()
	dto := garminConsumerDTO{Configured: consumer.Configured(), Source: source}

	id := auth.FromContext(r.Context())
	switch {
	case !id.Role.Can(auth.PermManageSettings):
		// Not an error worth explaining: a rider has no business setting a
		// deployment-wide credential, and saying so invites them to try.
	case !s.Settings.CanStore():
		dto.Unavailable = "this deployment has no encryption key, so a consumer set here could not be kept: " +
			secrets.ErrNoKey.Error()
	default:
		dto.CanManage = true
	}

	if source == consumerFromSettings {
		if meta, err := s.Settings.Describe(SettingGarminConsumerKey); err == nil {
			dto.UpdatedBy = meta.UpdatedBy
			dto.UpdatedAt = meta.UpdatedAt.UTC().Format(time.RFC3339)
		}
	}
	return dto
}

// handleGarminConsumer reports whether a consumer is configured, and where
// from. It never returns the pair itself.
func (s *Server) handleGarminConsumer(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageSettings) {
		return
	}
	writeJSON(w, http.StatusOK, s.garminConsumerDTOFor(r))
}

// handleSetGarminConsumer stores the pair an admin pasted in.
func (s *Server) handleSetGarminConsumer(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageSettings) {
		return
	}
	if !s.Settings.CanStore() {
		s.logger().Warn("garmin consumer set requested but this deployment has no encryption key configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment cannot store settings: " + secrets.ErrNoKey.Error(),
		})
		return
	}

	var body struct {
		Key    string `json:"key"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	body.Key = strings.TrimSpace(body.Key)
	body.Secret = strings.TrimSpace(body.Secret)
	if body.Key == "" || body.Secret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "both the consumer key and the consumer secret are required",
		})
		return
	}

	admin := auth.FromContext(r.Context()).User
	if err := s.Settings.Set(SettingGarminConsumerKey, body.Key, admin); err != nil {
		s.logger().Error("storing the garmin consumer key failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not store the consumer",
		})
		return
	}
	if err := s.Settings.Set(SettingGarminConsumerSecret, body.Secret, admin); err != nil {
		// The key landed and the secret did not, which would leave a pair
		// that signs nothing. Undo rather than leave it half-set.
		if err := s.Settings.Delete(SettingGarminConsumerKey); err != nil {
			s.logger().Error("rolling back the garmin consumer key failed", "err", err)
		}
		s.logger().Error("storing the garmin consumer secret failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not store the consumer",
		})
		return
	}

	s.logger().Info("garmin consumer set", "by", admin)
	writeJSON(w, http.StatusOK, s.garminConsumerDTOFor(r))
}

// handleClearGarminConsumer removes the stored pair, falling back to whatever
// the environment supplies.
func (s *Server) handleClearGarminConsumer(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageSettings) {
		return
	}

	for _, name := range []string{SettingGarminConsumerKey, SettingGarminConsumerSecret} {
		if err := s.Settings.Delete(name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	s.logger().Info("garmin consumer cleared", "by", auth.FromContext(r.Context()).User)
	writeJSON(w, http.StatusOK, s.garminConsumerDTOFor(r))
}
