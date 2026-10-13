package api

import (
	"net/http"
	"strings"
	"time"
)

// The http.Server's own timeouts (cmd/domestique) are the defaults every request
// lives under; they are short on purpose, because a slow body or a client that
// never reads a response holds a connection and a goroutine for as long as the
// server lets it. The two below are the exceptions handlers legitimately need.
const (
	// ServerReadTimeout bounds reading a whole request, headers and body. An
	// ordinary JSON body is a few kilobytes, so thirty seconds is generous.
	ServerReadTimeout = 30 * time.Second
	// ServerWriteTimeout bounds a handler plus writing its response. It covers
	// everything that only reads from the library or a database, including the
	// gzip of a long track.
	ServerWriteTimeout = 2 * time.Minute

	// uploadReadDeadline is how long a GPX upload (up to maxUploadBytes) may take
	// to arrive: a phone on a weak uplink moves 20 MiB in a few minutes, not
	// thirty seconds.
	uploadReadDeadline = 5 * time.Minute
	// mutationWriteDeadline is for requests that call out to somebody else: a push
	// to head units, an import from Komoot or Garmin, a season plan. Each of those
	// call-outs has its own client timeout; this only has to outlast their sum.
	mutationWriteDeadline = 5 * time.Minute
)

// extendDeadlines gives the requests that need it more room than the server
// defaults, through http.ResponseController so no other request is affected:
//
//   - every request that is not a GET or HEAD gets a longer write deadline, as
//     they are the ones that call out (push, import, plan);
//   - the GPX upload endpoints also get a longer read deadline, since a 20 MiB
//     body is the one thing that legitimately takes minutes to arrive.
//
// It runs outermost, before anything wraps the ResponseWriter, so the controller
// reaches the connection. The error from either call is ignored on purpose: it
// is http.ErrNotSupported for a writer with no connection behind it (a test
// recorder), and then there is no deadline to extend.
func extendDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Now().Add(mutationWriteDeadline))
			if isUpload(r) {
				_ = rc.SetReadDeadline(time.Now().Add(uploadReadDeadline))
			}
		}
		next.ServeHTTP(w, r)
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
