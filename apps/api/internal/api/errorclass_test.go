package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func uploadBody(t *testing.T, content string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.gpx")
	_, _ = fw.Write([]byte(content))
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

const goodGPX = `<?xml version="1.0"?><gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>` +
	`<trkpt lat="50.79" lon="2.81"><ele>42</ele></trkpt><trkpt lat="50.80" lon="2.84"><ele>128</ele></trkpt></trkseg></trk></gpx>`

// A GPX the rider can fix is a 400 with its reason; a storage failure is ours.
func TestUploadSeparatesBadGPXFromAFailingStore(t *testing.T) {
	s := deadlineServer(t)

	body, ct := uploadBody(t, "this is not GPX at all")
	w := serveWith(t, s, http.MethodPost, "/api/routes", body, ct)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "GPX") {
		t.Fatalf("bad GPX: status = %d body = %s; want 400 with the reason", w.Code, w.Body.String())
	}

	body, ct = uploadBody(t, "")
	if w := serveWith(t, s, http.MethodPost, "/api/routes", body, ct); w.Code != http.StatusBadRequest {
		t.Fatalf("empty GPX: status = %d, want 400", w.Code)
	}

	if _, err := s.Source.(*source.DB).Conn().Exec(`DROP TABLE routes`); err != nil {
		t.Fatal(err)
	}
	body, ct = uploadBody(t, goodGPX)
	w = serveWith(t, s, http.MethodPost, "/api/routes", body, ct)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("store down: status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "routes") || strings.Contains(w.Body.String(), "SQL") {
		t.Fatalf("the store's error reached the client: %s", w.Body.String())
	}
}

// Linking: a bad request is the rider's to fix; a failing store is not.
func TestLinkAccountSeparatesBadInputFromAFailingStore(t *testing.T) {
	s := deadlineServer(t)

	w := serveWith(t, s, http.MethodPost, "/api/accounts", []byte(`{"provider":"nonsense"}`), "application/json")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unknown provider") {
		t.Fatalf("bad provider: status = %d body = %s; want 400 with the reason", w.Code, w.Body.String())
	}

	if _, err := s.Source.(*source.DB).Conn().Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	w = serveWith(t, s, http.MethodPost, "/api/accounts", []byte(`{"provider":"garmin"}`), "application/json")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("store down: status = %d body = %s; want 500", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "accounts") || strings.Contains(w.Body.String(), "SQL") {
		t.Fatalf("the store's error reached the client: %s", w.Body.String())
	}
}
