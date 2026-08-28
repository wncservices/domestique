package api

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// compress gzips a response when the client says it accepts gzip and the
// handler's own Content-Type is application/json — every writeJSON-based
// endpoint (GET /api/tracks/{slug} among them) shipped as plain
// uncompressed JSON before this, which is a real part of what made opening
// the full interactive map slow: that endpoint carries every raw GPS point
// with no simplification, and nothing between the handler and the wire ever
// shrank it.
//
// Scoped to application/json specifically, not every response this server
// sends: GET /api/track-preview-image is already-compressed PNG (gzipping
// it again wastes CPU for no benefit), and the GPX/FIT download and static
// SPA-file paths share this same mux but may depend on Range-request
// semantics a naive gzip wrapper does not account for — narrowing to JSON
// keeps this change from touching either without needing to know each
// handler's own Range-request behavior.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressingWriter{ResponseWriter: w}
		// A Close failure here means the gzip.Writer couldn't flush its
		// trailer to a connection that's likely already gone (the client
		// disconnected mid-response) — nothing left to do about it this
		// late, and it's the same "already logged elsewhere" reasoning
		// logRequests/otelhttp's own span cover for a broken connection.
		defer func() { _ = cw.Close() }()
		next.ServeHTTP(cw, r)
	})
}

// compressingWriter decides whether to gzip on the first WriteHeader (or
// the first Write, for a handler that never calls WriteHeader explicitly —
// Write implies an initial 200, the same as the stdlib's own
// http.ResponseWriter contract) — by then every handler in this codebase
// has already set Content-Type via w.Header().Set, the same order
// http.ResponseWriter itself requires headers to be set in. Deciding here,
// not eagerly in compress above, is what keeps a "plain 404, no body"
// response — several handlers' deliberate "quietly missing" contract, e.g.
// handleTrackPreview's own — from gaining a Content-Encoding header it
// never earns just because the client happened to send Accept-Encoding.
type compressingWriter struct {
	http.ResponseWriter
	wroteHeader bool
	gz          *gzip.Writer
}

func (w *compressingWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		// Vary tells any cache — the browser's own private one included,
		// which is exactly what handleTrack/handleTrackPreview's
		// Cache-Control: private, max-age=86400 already relies on — that
		// this response's body differs by Accept-Encoding, so a gzip-
		// encoded response never gets served back to a request that never
		// claimed to accept it.
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length") // no longer accurate once gzipped
		w.Header().Add("Vary", "Accept-Encoding")
		w.gz = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *compressingWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Close flushes and closes the gzip stream, if one was ever opened —
// deferred by compress above so a handler that panics or returns early
// still ends the stream correctly rather than truncating it.
func (w *compressingWriter) Close() error {
	if w.gz != nil {
		return w.gz.Close()
	}
	return nil
}
