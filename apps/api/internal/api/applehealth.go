package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/applehealth"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const (
	// appleHealthPath is the second endpoint that bypasses authenticate, and
	// the only one that is a write. See bypassesAuth.
	appleHealthPath = "/api/wellness/apple"
	// appleHealthMaxBody caps what is read before Decode sees a byte; 2000
	// samples of three kinds fit well inside it.
	appleHealthMaxBody = 256 << 10
	bearerPrefix       = "Bearer "
)

// NewAppleHealthLimiter is the per-token budget for accepted posts: a shortcut
// posts about once a day, so 12 an hour only stops a loop. Keyed by a hash of
// the token, never the token.
func NewAppleHealthLimiter() *ratelimit.Limiter { return ratelimit.New(12, time.Hour) }

// NewAppleHealthMissLimiter is the one global budget for a token nothing
// matches, global for the same reason as NewCalendarMissLimiter: there is no
// trustworthy client address behind the proxy.
func NewAppleHealthMissLimiter() *ratelimit.Limiter { return ratelimit.New(60, time.Minute) }

// bearerToken is the token in an Authorization header, or "" unless the header
// is exactly "Bearer " and 43 base64url characters. The token is never read
// from the path or the query, which proxies and access logs keep.
func bearerToken(r *http.Request) string {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
	if !ok || !isTokenShaped(tok) {
		return ""
	}
	return tok
}

// handleAppleHealthPost takes a day's Health readings from the rider's
// shortcut and stores them as that rider's wellness. The bearer token is the
// only credential and the rider is whoever owns its hash: nothing else in the
// request (header, cookie, body) says who.
//
// A missing, malformed, unknown or revoked token is the same 401, so a prober
// learns nothing. Logs and metrics never carry the token, a reading or a
// sample time.
func (s *Server) handleAppleHealthPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	reply := func(code int, result, msg string) {
		recordAppleHealthPost(result)
		if code == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "60")
		}
		writeJSON(w, code, map[string]string{"error": msg})
	}
	// miss spends the global budget: a probe that matched nothing.
	miss := func() {
		if s.AppleHealthMissLimiter != nil && !s.AppleHealthMissLimiter.Allow("miss") {
			reply(http.StatusTooManyRequests, "limited", "too many requests")
			return
		}
		reply(http.StatusUnauthorized, "unauthorized", "unauthorized")
	}

	token := bearerToken(r)
	if token == "" {
		miss()
		return
	}
	if s.AppleHealth == nil || s.Training == nil {
		s.logger().Warn("apple health post received but this deployment has no store", "stage", "lookup")
		miss()
		return
	}
	rider, err := s.AppleHealth.Lookup(r.Context(), token)
	if err != nil {
		s.logger().Error("apple health: token lookup failed", "stage", "lookup", "err", err)
		reply(http.StatusServiceUnavailable, "error", "unavailable")
		return
	}
	if rider == "" {
		miss()
		return
	}
	// Taken before the body is read: a burst of bad posts from a real token
	// is as much a loop as a burst of good ones.
	if s.AppleHealthLimiter != nil && !s.AppleHealthLimiter.Allow(limiterKey(token)) {
		reply(http.StatusTooManyRequests, "limited", "too many requests")
		return
	}

	// Read through the cap first: Decode does not say why it failed, and a
	// body past the cap is a 413, not a 400.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, appleHealthMaxBody))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		reply(http.StatusRequestEntityTooLarge, "too_large", "request too large")
		return
	}
	if err != nil {
		reply(http.StatusBadRequest, "invalid", "invalid request")
		return
	}
	payload, err := applehealth.Decode(bytes.NewReader(body))
	switch {
	case errors.Is(err, applehealth.ErrTooMany):
		reply(http.StatusRequestEntityTooLarge, "too_large", "too many samples")
		return
	case err != nil:
		reply(http.StatusBadRequest, "invalid", "invalid request")
		return
	}

	loc := s.trainingLocation()
	if loc == nil {
		loc = time.UTC
	}
	days := applehealth.Days(payload, loc, s.localToday())
	for _, d := range days {
		err := s.Training.SaveWellness(r.Context(), workout.DailyWellness{
			Rider: rider, Date: d.Date, Source: "apple",
			HRVLastNight: d.HRV, SleepSeconds: d.SleepSeconds, RestingHR: d.RestingHR,
		})
		if err != nil {
			recordAppleHealthPost("error")
			s.logger().Error("apple health: could not save a day", "rider", rider, "stage", "save", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
	}
	if err := s.AppleHealth.Touch(r.Context(), rider, s.now()); err != nil {
		s.logger().Warn("apple health: could not record the post", "rider", rider, "err", err)
	}
	s.logger().Info("apple health received", "rider", rider, "days", len(days))
	recordAppleHealthPost("ok")
	writeJSON(w, http.StatusOK, map[string]int{"days": len(days)})
}
