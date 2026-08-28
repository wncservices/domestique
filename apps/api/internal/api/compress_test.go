package api

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// samplePointsJSON builds a JSON body shaped like a real handleTrack
// response — repetitive coordinate pairs, the same reason a real one
// compresses well — rather than a body too small for gzip's own ~20-byte
// header/footer overhead to ever pay off.
func samplePointsJSON(t *testing.T) string {
	t.Helper()
	points := make([][2]float64, 500)
	for i := range points {
		points[i] = [2]float64{50.1 + float64(i)*0.0001, 4.1 + float64(i)*0.0001}
	}
	body, err := json.Marshal(map[string]any{"slug": "x", "points": points})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestCompressGzipsJSONWhenAccepted pins the actual point of this file: a
// JSON response is smaller on the wire when the client says it accepts
// gzip, with the headers (Content-Encoding, Vary) a client or cache needs
// to know that.
func TestCompressGzipsJSONWhenAccepted(t *testing.T) {
	body := samplePointsJSON(t)
	handler := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/tracks/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	if rec.Body.Len() >= len(body) {
		t.Errorf("compressed body is %d bytes, want smaller than the %d-byte original", rec.Body.Len(), len(body))
	}

	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("response body is not valid gzip: %v", err)
	}
	decoded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("decoding gzip body: %v", err)
	}
	if string(decoded) != body {
		t.Errorf("decoded body = %q, want %q", decoded, body)
	}
}

// TestCompressPassesThroughWithoutAcceptEncoding pins the other half of the
// contract: a client that never claimed to accept gzip gets a plain
// response — compress must not assume every caller can decode it.
func TestCompressPassesThroughWithoutAcceptEncoding(t *testing.T) {
	body := `{"ok":true}`
	handler := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want unset — this client never sent Accept-Encoding", got)
	}
	if rec.Body.String() != body {
		t.Errorf("body = %q, want %q unchanged", rec.Body.String(), body)
	}
}

// TestCompressSkipsNonJSONContentTypes pins why this is an allowlist, not a
// denylist: GET /api/track-preview-image (image/png) must never be
// gzipped again — it already is one, at the PNG level — and neither must
// anything else this mux serves whose Range-request behavior this
// middleware was never taught about (GPX/FIT downloads, the static SPA
// files).
func TestCompressSkipsNonJSONContentTypes(t *testing.T) {
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}
	handler := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pngBytes)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/track-preview-image/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want unset for image/png", got)
	}
	if rec.Body.String() != string(pngBytes) {
		t.Error("PNG body was modified — it should have passed through byte-for-byte")
	}
}

// TestCompressLeavesAnEmptyBodyEmpty pins several handlers' deliberate
// "plain 404, no body worth parsing" contract (handleTrackPreview,
// handleTrackPreviewImage when unavailable) — a bare WriteHeader with no
// following Write must not gain a Content-Encoding header or any gzip
// framing bytes it never earned.
func TestCompressLeavesAnEmptyBodyEmpty(t *testing.T) {
	handler := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/track-preview/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want unset — no Content-Type was ever set to allowlist", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body is %d bytes, want 0", rec.Body.Len())
	}
}
