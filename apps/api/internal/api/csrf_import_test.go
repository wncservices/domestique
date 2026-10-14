package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// The CSRF guard refuses any non-JSON body except on the multipart upload
// routes. The ride-history import is one of them; no other training route is.
func TestCSRFGuardLetsMultipartThroughOnlyOnTheUploadRoutes(t *testing.T) {
	h := newTrainingHarness(t)
	body := "--b\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\ny\r\n--b--\r\n"
	post := func(path string) int {
		req, err := http.NewRequest(http.MethodPost, h.base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
		req.Header.Set("Remote-User", "wilant")
		req.Header.Set("Remote-Groups", "cyclists")
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("/api/training/import"); got == http.StatusUnsupportedMediaType {
		t.Errorf("import: %d, the guard refused the multipart upload", got)
	}
	if got := post("/api/training/workouts"); got != http.StatusUnsupportedMediaType {
		t.Errorf("workouts: %d, want 415 for a multipart body", got)
	}
}
