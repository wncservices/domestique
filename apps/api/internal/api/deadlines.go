package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The http.Server's own timeouts (cmd/domestique) are the defaults every request
// lives under; they are short on purpose, because a slow body or a client that
// never reads a response holds a connection and a goroutine for as long as the
// server lets it. The values below are the exceptions handlers legitimately need.
const (
	// ServerReadTimeout bounds reading a whole request, headers and body. An
	// ordinary JSON body is a few kilobytes, so thirty seconds is generous.
	ServerReadTimeout = 30 * time.Second
	// ServerWriteTimeout bounds a handler plus writing its response. It covers
	// everything that only reads from the library or a database.
	ServerWriteTimeout = 2 * time.Minute

	// uploadReadDeadline is how long a GPX upload (up to maxUploadBytes) may take
	// to arrive: a phone on a weak uplink moves 20 MiB in a few minutes, not
	// thirty seconds.
	uploadReadDeadline = 5 * time.Minute
	// mutationWriteDeadline is for any request that is not a GET: most of them
	// call out to somebody else. Each call-out has its own client timeout; this
	// has to outlast their sum, with room.
	mutationWriteDeadline = 15 * time.Minute
	// uploadResponseDeadline re-bounds an upload's response once its body is in.
	uploadResponseDeadline = 2 * time.Minute
	// downloadWriteDeadline is for GETs that stream a file or call out: a slow
	// link downloading a long track, a listing that fans out to Komoot.
	downloadWriteDeadline = 10 * time.Minute
	// longWriteDeadline is for the requests that fan out to many head units or
	// import many routes one by one, where the sum of the call-outs can be long.
	longWriteDeadline = 30 * time.Minute
)

// patternWriteDeadlines are the routes that need more than their method's
// default, keyed by the mux pattern (not the path, which carries slugs and
// tokens). A new endpoint that pushes to several devices, imports in bulk or
// streams a file belongs here.
var patternWriteDeadlines = map[string]time.Duration{
	"POST /api/push": longWriteDeadline,
	"POST /api/crews/{id}/rides/{rideId}/sync": longWriteDeadline,
	"POST /api/komoot/import":                  longWriteDeadline,
	"POST /api/garmin/courses/import":          longWriteDeadline,
	"POST /api/wahoo/routes/import":            longWriteDeadline,
	"GET /api/tracks/{slug...}":                downloadWriteDeadline,
	"GET /api/gpx/{slug...}":                   downloadWriteDeadline,
	"GET /api/fit/{slug...}":                   downloadWriteDeadline,
	"GET /api/routes/{slug}/pacing.fit":        downloadWriteDeadline,
	"GET /api/shares/{token}/track":            downloadWriteDeadline,
	"GET /api/shares/{token}/gpx":              downloadWriteDeadline,
	"GET /api/shares/{token}/fit":              downloadWriteDeadline,
	"GET /api/training/workouts/{id}/fit":      downloadWriteDeadline,
	"GET /api/komoot/tours":                    downloadWriteDeadline,
	"GET /api/garmin/courses":                  downloadWriteDeadline,
	"GET /api/wahoo/routes":                    downloadWriteDeadline,
	"GET /api/komoot/tours/duplicates":         downloadWriteDeadline,
	"GET /api/garmin/courses/duplicates":       downloadWriteDeadline,
	"GET /api/wahoo/routes/duplicates":         downloadWriteDeadline,
}

// reqTiming is what the outermost middleware needs to know about a request when
// it ends: the write deadline now in force, and the route pattern the mux chose.
// Handlers and the pattern middleware below it update the one value in the
// context, which survives every WithContext copy in between.
type reqTiming struct {
	mu      sync.Mutex
	writeBy time.Time
	pattern string
}

type reqTimingKey struct{}

func timingOf(ctx context.Context) *reqTiming {
	t, _ := ctx.Value(reqTimingKey{}).(*reqTiming)
	return t
}

// setWriteDeadline sets the write deadline of this request to now+d through
// http.ResponseController, and remembers it so the overrun warning compares
// against the deadline actually in force. The error is ignored on purpose: it
// is http.ErrNotSupported for a writer with no connection behind it (a test
// recorder), and then there is no deadline to move.
func setWriteDeadline(w http.ResponseWriter, r *http.Request, d time.Duration) {
	by := time.Now().Add(d)
	_ = http.NewResponseController(w).SetWriteDeadline(by)
	if t := timingOf(r.Context()); t != nil {
		t.mu.Lock()
		t.writeBy = by
		t.mu.Unlock()
	}
}

// extendDeadlines gives the requests that need it more room than the server
// defaults, through http.ResponseController so no other request is affected:
//
//   - every request that is not a GET or HEAD gets a longer write deadline, as
//     they are the ones that call out (push, import, plan);
//   - the GPX upload endpoints also get a longer read deadline, since a 20 MiB
//     body is the one thing that legitimately takes minutes to arrive;
//   - withRoutePattern then raises the write deadline further for the routes in
//     patternWriteDeadlines.
//
// It runs outermost, before anything wraps the ResponseWriter. When a request
// ends after the write deadline in force, it logs a Warn naming the route
// pattern: the client has probably been cut off and nothing else would say so.
func (s *Server) extendDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := &reqTiming{}
		if srv, ok := r.Context().Value(http.ServerContextKey).(*http.Server); ok && srv.WriteTimeout > 0 {
			t.writeBy = time.Now().Add(srv.WriteTimeout)
		}
		r = r.WithContext(context.WithValue(r.Context(), reqTimingKey{}, t))

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			setWriteDeadline(w, r, mutationWriteDeadline)
			if isUpload(r) {
				_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadReadDeadline))
			}
		}
		next.ServeHTTP(w, r)

		t.mu.Lock()
		by, pattern := t.writeBy, t.pattern
		t.mu.Unlock()
		if !by.IsZero() && time.Now().After(by) {
			s.logger().Warn("request outlived its write deadline; the response may not have reached the client",
				"route", pattern, "overrun", time.Since(by).Round(time.Millisecond))
		}
	})
}

// withRoutePattern wraps the mux: it records which pattern a request matched and
// applies that pattern's longer write deadline, if it has one. Placed inside
// authenticate, so an unauthorised request never gets the extra time.
func withRoutePattern(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if t := timingOf(r.Context()); t != nil {
			t.mu.Lock()
			t.pattern = pattern
			t.mu.Unlock()
		}
		if d, ok := patternWriteDeadlines[pattern]; ok {
			setWriteDeadline(w, r, d)
		}
		mux.ServeHTTP(w, r)
	})
}

// isUpload is the requests that carry a track or a GPX in the body: the
// multipart upload, a route built on the map, and an edit of a route's points.
func isUpload(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost:
		return r.URL.Path == "/api/routes" || r.URL.Path == "/api/routes/from-points"
	case http.MethodPut:
		return strings.HasPrefix(r.URL.Path, "/api/routes/") && strings.HasSuffix(r.URL.Path, "/points")
	}
	return false
}
