package api

import (
	"bytes"
	"context"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

// deadlineWriter is a ResponseWriter with a "connection": it records every
// deadline http.ResponseController is asked to set, in order.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	mu    sync.Mutex
	write []time.Time
	read  []time.Time
}

func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.write = append(d.write, t)
	return nil
}

func (d *deadlineWriter) SetReadDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.read = append(d.read, t)
	return nil
}

func (d *deadlineWriter) lastWrite() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.write) == 0 {
		return 0
	}
	return time.Until(d.write[len(d.write)-1]).Round(time.Minute)
}

func deadlineServer(t *testing.T) *Server {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := state.Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{Mode: auth.ModeNone})
	if err != nil {
		t.Fatal(err)
	}
	accts, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Source: db, Store: st, Auth: a, Accounts: accts, Config: &config.Config{},
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
}

func serveWith(t *testing.T, s *Server, method, path string, body []byte, ctype string) *deadlineWriter {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	s.Handler().ServeHTTP(w, req)
	return w
}

// Every request gets the server's short defaults; these are the ones that need
// more, and how much, through the whole real handler chain.
func TestRequestsGetTheWriteDeadlineTheirKindNeeds(t *testing.T) {
	s := deadlineServer(t)
	for _, tc := range []struct {
		name, method, path string
		want               time.Duration
	}{
		{"ordinary mutation", http.MethodPost, "/api/routes/from-points", 15 * time.Minute},
		{"push to devices", http.MethodPost, "/api/push", 30 * time.Minute},
		{"komoot import", http.MethodPost, "/api/komoot/import", 30 * time.Minute},
		{"garmin course import", http.MethodPost, "/api/garmin/courses/import", 30 * time.Minute},
		{"wahoo import", http.MethodPost, "/api/wahoo/routes/import", 30 * time.Minute},
		{"crew sync now", http.MethodPost, "/api/crews/c1/rides/r1/sync", 30 * time.Minute},
		{"GPX download", http.MethodGet, "/api/gpx/some-route", 10 * time.Minute},
		{"FIT download", http.MethodGet, "/api/fit/some-route", 10 * time.Minute},
		{"track", http.MethodGet, "/api/tracks/some-route", 10 * time.Minute},
		{"shared GPX", http.MethodGet, "/api/shares/tok/gpx", 10 * time.Minute},
		{"komoot tour list", http.MethodGet, "/api/komoot/tours", 10 * time.Minute},
		{"pacing FIT", http.MethodGet, "/api/routes/some-route/pacing.fit", 10 * time.Minute},
		{"an ordinary read keeps the server default", http.MethodGet, "/api/routes", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serveWith(t, s, tc.method, tc.path, nil, "")
			if got := w.lastWrite(); got != tc.want {
				t.Fatalf("last write deadline = %v from now, want %v", got, tc.want)
			}
		})
	}
}

// Once the body of an upload is in, the long window has done its job and the
// response is bounded again, so a client that will not read it cannot hold the
// connection for the whole of the 15 minutes.
func TestAnUploadRearmsItsWriteDeadlineAfterTheBody(t *testing.T) {
	s := deadlineServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.gpx")
	_, _ = fw.Write([]byte(`<?xml version="1.0"?><gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>` +
		`<trkpt lat="50.79" lon="2.81"><ele>42</ele></trkpt><trkpt lat="50.80" lon="2.84"><ele>128</ele></trkpt></trkseg></trk></gpx>`))
	_ = mw.Close()

	w := serveWith(t, s, http.MethodPost, "/api/routes", buf.Bytes(), mw.FormDataContentType())
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: status = %d: %s", w.Code, w.Body.String())
	}
	if len(w.write) < 2 {
		t.Fatalf("write deadlines set = %d, want the long one and then a re-arm", len(w.write))
	}
	if got := w.lastWrite(); got != 2*time.Minute {
		t.Fatalf("after the body: %v, want 2m", got)
	}
}

// A request that runs past its write deadline is the one whose answer the
// client may never receive, and nothing else says so.
func TestARequestThatOutlivesItsWriteDeadlineIsWarnedAboutWithItsRoute(t *testing.T) {
	var logs bytes.Buffer
	s := &Server{Log: slog.New(slog.NewTextHandler(&logs, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow/{id}", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
	})
	mux.HandleFunc("GET /quick/{id}", func(w http.ResponseWriter, r *http.Request) {})
	h := s.extendDeadlines(withRoutePattern(mux))

	run := func(path string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(context.WithValue(req.Context(), http.ServerContextKey, &http.Server{WriteTimeout: 50 * time.Millisecond}))
		h.ServeHTTP(&deadlineWriter{ResponseRecorder: httptest.NewRecorder()}, req)
	}

	run("/quick/7")
	if strings.Contains(logs.String(), "deadline") {
		t.Fatalf("a request inside its deadline was warned about: %s", logs.String())
	}
	run("/slow/42")
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, `route="GET /slow/{id}"`) {
		t.Fatalf("no Warn naming the route pattern: %q", out)
	}
	if strings.Contains(out, "/slow/42") {
		t.Fatalf("the concrete path (which can carry an id or a token) is in the log: %q", out)
	}
}
