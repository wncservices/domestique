package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/calendarfeed"
	"github.com/wncservices/domestique/apps/api/internal/ics"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
)

const (
	calendarPrefix = "/api/calendar/"
	calendarSuffix = ".ics"
	// calendarTokenLen is 32 random bytes in unpadded base64url.
	calendarTokenLen = 43
	// calendarName is what a calendar app shows for the subscription.
	calendarName = "Domestique training"
	// calendarRefreshHours is advertised only as a hint: Google ignores it
	// and refreshes roughly every 12 to 24 hours regardless.
	calendarRefreshHours = 6
)

// NewCalendarLimiter is the per-token budget for the feed: a polling calendar
// app needs a few fetches a day, so 30 an hour is generous and still stops a
// loop. Keyed by a hash of the token, never the token.
func NewCalendarLimiter() *ratelimit.Limiter { return ratelimit.New(30, time.Hour) }

// NewCalendarMissLimiter is the one global budget for a token nothing matches.
// It is global rather than per client because there is no trustworthy client
// address behind the proxy (the app does not parse X-Forwarded-For). Guessing
// a 256-bit token is not the threat; log noise and database load are.
func NewCalendarMissLimiter() *ratelimit.Limiter { return ratelimit.New(60, time.Minute) }

// isCalendarFeedPath is the one predicate for "this is the public calendar
// feed": exactly /api/calendar/<43 base64url characters>.ics. Both the mux
// handler and the authenticate bypass use it, so nothing else under
// /api/calendar/ can ever inherit the bypass by sharing a prefix.
func isCalendarFeedPath(p string) bool {
	rest, ok := strings.CutPrefix(p, calendarPrefix)
	if !ok {
		return false
	}
	tok, ok := strings.CutSuffix(rest, calendarSuffix)
	if !ok || len(tok) != calendarTokenLen {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		isAlnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !isAlnum && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// bypassesAuth is the whole of the authenticate exemption for the feed: a GET
// or HEAD of exactly the feed path. A forged Remote-User or a cookie on such a
// request is never looked at. HEAD is the same handler with no body (Go's
// server drops it), which some calendar apps and link checkers use to see
// whether the feed changed; it reads nothing a GET does not.
func bypassesAuth(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && isCalendarFeedPath(r.URL.Path)
}

// redactPath replaces every secret segment of a path with [redacted], for
// anything that copies a request path somewhere a third party or an operator
// reads (the debug log, a span name, the url.path attribute).
//
// A segment is secret when it has the shape of a token (43 base64url
// characters, optionally with .ics in any case), wherever it sits, or when it
// is the segment right after api/calendar or api/shares. Matching is by
// segment, ignoring empty and "." / ".." segments for the "after" rule and
// folding case, so non-canonical spellings of the same URL (//api/calendar/x,
// /./, /API/...) cannot carry a token past it. The shape of everything else,
// the .ics suffix and a share's /track, is kept.
func redactPath(p string) string {
	segs := strings.Split(p, "/")
	var sig []string // the meaningful segments seen so far
	for i, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		body, suffix := seg, ""
		if len(seg) >= len(calendarSuffix) && strings.EqualFold(seg[len(seg)-len(calendarSuffix):], calendarSuffix) {
			body, suffix = seg[:len(seg)-len(calendarSuffix)], seg[len(seg)-len(calendarSuffix):]
		}
		follows := len(sig) >= 2 && strings.EqualFold(sig[len(sig)-2], "api") &&
			(strings.EqualFold(sig[len(sig)-1], "calendar") || strings.EqualFold(sig[len(sig)-1], "shares"))
		if follows || isTokenShaped(body) {
			segs[i] = "[redacted]" + suffix
		}
		sig = append(sig, seg)
	}
	return strings.Join(segs, "/")
}

// isTokenShaped is 43 base64url characters: what a calendar or share token
// looks like, whatever path it turns up in.
func isTokenShaped(s string) bool {
	if len(s) != calendarTokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !isAlnum && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

type realPathKey struct{}

type realPath struct{ path, rawPath, requestURI string }

// hideSecretPaths sits outside otelhttp. The span's name is redacted by its
// formatter, but otelhttp also records the request path as the url.path
// attribute, which a name formatter cannot reach. So for a path that carries a
// secret the request the tracer sees has the redacted path, and
// restoreSecretPaths, just inside the tracer, puts the real one back for the
// mux and the handlers.
func hideSecretPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redacted := redactPath(r.URL.Path)
		if redacted == r.URL.Path {
			next.ServeHTTP(w, r)
			return
		}
		orig := realPath{r.URL.Path, r.URL.RawPath, r.RequestURI}
		r2 := r.WithContext(context.WithValue(r.Context(), realPathKey{}, orig))
		u := *r.URL
		u.Path, u.RawPath = redacted, ""
		r2.URL = &u
		r2.RequestURI = redacted
		next.ServeHTTP(w, r2)
	})
}

func restoreSecretPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if orig, ok := r.Context().Value(realPathKey{}).(realPath); ok {
			u := *r.URL
			u.Path, u.RawPath = orig.path, orig.rawPath
			r.URL = &u
			r.RequestURI = orig.requestURI
		}
		next.ServeHTTP(w, r)
	})
}

// handleCalendarFeed serves GET /api/calendar/<token>.ics with no session: the
// token's owner is the only rider whose plan is served, and nothing in the
// request (header, cookie) is read to decide it.
//
// Every failure that depends on the token — malformed, unknown, replaced,
// revoked — is the same 404, so a prober learns nothing.
func (s *Server) handleCalendarFeed(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")

	notFound := func() {
		h.Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
	tooMany := func() {
		h.Set("Cache-Control", "no-store")
		h.Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
	}
	// TODO: check the miss bucket (without spending it) before the Lookup, so a
	// flood of random well-formed tokens is shed before it reaches the
	// database. Left out on purpose: the bucket is global, so once a flood
	// empties it the shed would also 429 the real tokens that are still
	// fetching fine today, turning a database-load nuisance into a way to lock
	// every rider's calendar out. Per-client keying (needs a trustworthy client
	// address behind the proxy) is what would make it safe.
	miss := func() {
		if s.CalendarMissLimiter != nil && !s.CalendarMissLimiter.Allow("miss") {
			tooMany()
			return
		}
		notFound()
	}

	if !isCalendarFeedPath(r.URL.Path) {
		miss()
		return
	}
	if s.CalendarFeeds == nil || s.Training == nil {
		s.logger().Warn("calendar feed requested but this deployment has no calendar store")
		notFound()
		return
	}
	token := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, calendarPrefix), calendarSuffix)

	rider, err := s.CalendarFeeds.Lookup(r.Context(), token)
	if errors.Is(err, calendarfeed.ErrNotFound) {
		miss()
		return
	}
	if err != nil {
		s.logger().Error("calendar feed: token lookup failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
		return
	}
	if s.CalendarLimiter != nil && !s.CalendarLimiter.Allow(limiterKey(token)) {
		tooMany()
		return
	}

	workouts, err := s.Training.ListWorkouts(r.Context(), rider)
	if err != nil {
		s.logger().Error("calendar feed: listing workouts failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
		return
	}
	loc := time.UTC
	zone := ""
	if sched, err := s.syncSchedule(); err == nil && sched.Location() != nil {
		loc = sched.Location()
		zone = loc.String()
	}
	var startHour func(string) (int, bool)
	if s.RideStartHour != nil {
		ctx := r.Context()
		startHour = func(rider string) (int, bool) { return s.RideStartHour(ctx, rider) }
	}
	body := ics.Calendar{
		Name: calendarName, Timezone: zone, RefreshHours: calendarRefreshHours,
		Events: calendarfeed.Events(workouts, s.now(), loc, startHour, s.publicURL()),
	}.Bytes()

	// A fetch is a read; the profile's "last fetched" is written at most once
	// an hour by the store, and a failure to record it must not fail the feed.
	if err := s.CalendarFeeds.Touch(r.Context(), rider, s.now()); err != nil {
		s.logger().Warn("calendar feed: could not record the fetch", "err", err)
	}

	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, max-age=300")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "text/calendar; charset=utf-8")
	h.Set("Content-Disposition", `inline; filename="domestique.ics"`)
	_, _ = w.Write(body)
}

// limiterKey is a stable key for a token that is not the token, so the
// limiter's map never holds a live credential.
func limiterKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

// etagMatches implements If-None-Match for a strong or weak validator, a
// list, and "*".
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// publicURL is config's public_url, "" when unset.
func (s *Server) publicURL() string {
	if s.Config == nil {
		return ""
	}
	return s.Config.PublicURL
}

// ---------- rider-facing management, behind the ordinary gate ----------

type calendarStatusDTO struct {
	// Available is false without a store or a public_url: the link could not
	// be built, so the UI hides the card.
	Available     bool   `json:"available"`
	Active        bool   `json:"active"`
	CreatedAt     string `json:"createdAt,omitempty"`
	LastFetchedAt string `json:"lastFetchedAt,omitempty"`
}

// calendarLinkDTO is the only time the URL exists: only its hash is stored.
type calendarLinkDTO struct {
	URL       string `json:"url"`
	WebcalURL string `json:"webcalUrl"`
}

func (s *Server) calendarAvailable() bool {
	return s.CalendarFeeds != nil && s.publicURL() != ""
}

func (s *Server) handleGetCalendar(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) {
		return
	}
	out := calendarStatusDTO{Available: s.calendarAvailable()}
	if out.Available {
		rider := auth.FromContext(r.Context()).User
		st, err := s.CalendarFeeds.Status(r.Context(), rider)
		if err != nil {
			s.fail(w, err)
			return
		}
		out.Active = st.Active
		if st.Active {
			out.CreatedAt = st.CreatedAt.Format(time.RFC3339)
		}
		if !st.LastFetchedAt.IsZero() {
			out.LastFetchedAt = st.LastFetchedAt.Format(time.RFC3339)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateCalendar generates, or regenerates, the rider's link. The rider
// is the session's, never the body's.
func (s *Server) handleCreateCalendar(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) {
		return
	}
	if !s.calendarAvailable() {
		s.logger().Warn("calendar link refused: this deployment has no public_url or no calendar store")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "calendar links are not set up on this deployment (public_url is not configured)",
		})
		return
	}
	rider := auth.FromContext(r.Context()).User
	token, err := s.CalendarFeeds.Regenerate(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("calendar link generated", "rider", rider)
	link := s.publicURL() + calendarPrefix + token + calendarSuffix
	webcal := link
	if i := strings.Index(link, "://"); i >= 0 {
		webcal = "webcal" + link[i:]
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, calendarLinkDTO{URL: link, WebcalURL: webcal})
}

func (s *Server) handleRevokeCalendar(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) {
		return
	}
	if s.CalendarFeeds == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "calendar links are not set up on this deployment"})
		return
	}
	rider := auth.FromContext(r.Context()).User
	if err := s.CalendarFeeds.Revoke(r.Context(), rider); err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("calendar link revoked", "rider", rider)
	w.WriteHeader(http.StatusNoContent)
}
