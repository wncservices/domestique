package api_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

// timeoutHarness is the real handler chain behind a server with deliberately
// tiny read and write timeouts, standing in for the production ones.
func timeoutHarness(t *testing.T, tiny time.Duration) *httptest.Server {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
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
	srv := &api.Server{Source: db, Store: st, Auth: a, Accounts: accts, Config: &config.Config{}}

	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.ReadTimeout = tiny
	ts.Config.WriteTimeout = tiny
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

// stalled is a request body that sends its first part, waits, then sends the
// rest: a client on a slow uplink.
func stalled(first, rest []byte, wait time.Duration) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(first)
		time.Sleep(wait)
		_, _ = pw.Write(rest)
		_ = pw.Close()
	}()
	return pr
}

const slowGPX = `<?xml version="1.0"?><gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>` +
	`<trkpt lat="50.79" lon="2.81"><ele>42</ele></trkpt><trkpt lat="50.80" lon="2.84"><ele>128</ele></trkpt></trkseg></trk></gpx>`

// A GPX upload is up to 20 MiB; on a phone's uplink that takes longer than any
// sensible blanket read timeout, so the upload handlers extend their own.
func TestASlowGPXUploadIsNotCutOffByTheServerReadTimeout(t *testing.T) {
	ts := timeoutHarness(t, 300*time.Millisecond)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "slow.gpx")
	_, _ = fw.Write([]byte(slowGPX))
	_ = mw.Close()
	body := buf.Bytes()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/routes", stalled(body[:40], body[40:], 900*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("the upload was cut off: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, b)
	}
}

// ...but only the uploads: any other request that stalls mid-body is exactly
// what the read timeout exists to drop.
func TestAStalledJSONBodyIsCutOffByTheServerReadTimeout(t *testing.T) {
	ts := timeoutHarness(t, 300*time.Millisecond)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/routebuilder/preview",
		stalled([]byte(`{"points":[`), []byte(`]}`), 900*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		// A dropped connection is the usual outcome; a 4xx for the half-read body
		// is as good. What must not happen is the handler being handed the
		// completed body late and answering 200.
		if resp.StatusCode == http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("a body that stalled past the read timeout was served: %s", b)
		}
	}
}
