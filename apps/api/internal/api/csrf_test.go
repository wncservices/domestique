package api_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// csrfCall sends one request to the crews harness with exactly the headers
// given, so a case can say what a browser would and would not send.
func (h *crewHarness) csrfCall(method, path, body string, headers map[string]string) int {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Remote-User", "wilant")
	req.Header.Set("Remote-Groups", "cyclists")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// Audit N4: every state-changing endpoint parsed its body as JSON whatever the
// Content-Type, so a cross-site form or a no-cors fetch (both "simple"
// requests, no preflight) could drive it with whatever cookie the browser
// chose to attach. Browser-initiated writes now have to say they are ours.
func TestCSRFGuard(t *testing.T) {
	h := newCrewHarness(t)
	host := strings.TrimPrefix(h.base, "http://")
	own := "http://" + host
	const json = "application/json"

	cases := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
		want    int
	}{
		{"audit PoC: cross-site text/plain form", "POST", "/api/crews", `{"name":"=csrf crew","pad":"\"}`,
			map[string]string{"Content-Type": "text/plain", "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site JSON", "POST", "/api/crews", `{"name":"x"}`,
			map[string]string{"Content-Type": json, "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site sibling subdomain", "POST", "/api/crews", `{"name":"x"}`,
			map[string]string{"Content-Type": json, "Origin": "https://other.example", "Sec-Fetch-Site": "same-site"}, 403},
		{"cross-site DELETE with no body", "DELETE", "/api/crews/crew:nope", "",
			map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, 403},
		{"foreign Origin and no Fetch Metadata", "POST", "/api/crews", `{"name":"x"}`,
			map[string]string{"Content-Type": json, "Origin": "https://evil.example"}, 403},
		{"opaque Origin and no Fetch Metadata", "POST", "/api/crews", `{"name":"x"}`,
			map[string]string{"Content-Type": json, "Origin": "null"}, 403},

		{"same-origin JSON", "POST", "/api/crews", `{"name":"Same Origin"}`,
			map[string]string{"Content-Type": json, "Origin": own, "Sec-Fetch-Site": "same-origin"}, 201},
		{"Sec-Fetch-Site none (user-initiated)", "POST", "/api/crews", `{"name":"None"}`,
			map[string]string{"Content-Type": json + "; charset=utf-8", "Sec-Fetch-Site": "none"}, 201},
		{"older browser: own Origin, no Fetch Metadata", "POST", "/api/crews", `{"name":"Own Origin"}`,
			map[string]string{"Content-Type": json, "Origin": own}, 201},
		{"no browser headers at all (curl, tests, forwardAuth)", "POST", "/api/crews", `{"name":"Bare"}`,
			map[string]string{"Content-Type": json}, 201},
		{"cross-site GET is a read", "GET", "/api/crews", "",
			map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, 200},

		{"same-origin text/plain body", "POST", "/api/crews", `{"name":"x"}`,
			map[string]string{"Content-Type": "text/plain", "Sec-Fetch-Site": "same-origin"}, 415},
		{"same-origin form body", "POST", "/api/crews", `name=x`,
			map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin"}, 415},
		{"body with no Content-Type", "POST", "/api/crews", `{"name":"x"}`, nil, 415},
		{"no body needs no Content-Type", "DELETE", "/api/crews/crew:nope", "",
			map[string]string{"Sec-Fetch-Site": "same-origin"}, 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := h.csrfCall(c.method, c.path, c.body, c.headers); got != c.want {
				t.Errorf("%s %s = %d, want %d", c.method, c.path, got, c.want)
			}
		})
	}

	// The refused ones changed nothing.
	list := h.as("wilant", "cyclists", http.MethodGet, "/api/crews", "")
	raw, _ := io.ReadAll(list.Body)
	if strings.Contains(string(raw), "csrf") || strings.Contains(string(raw), `"x"`) {
		t.Errorf("a refused request created a crew: %s", raw)
	}
}

// The upload is the one multipart endpoint, and a legitimate same-origin
// browser form: it must get past the Content-Type rule.
func TestCSRFGuardLetsTheMultipartUploadThrough(t *testing.T) {
	h := newCrewHarness(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("name", "x")
	_ = mw.Close()

	send := func(site string) int {
		req, err := http.NewRequest("POST", h.base+"/api/routes", bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Remote-User", "wilant")
		req.Header.Set("Remote-Groups", "cyclists")
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Sec-Fetch-Site", site)
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := send("same-origin"); got == http.StatusUnsupportedMediaType || got == http.StatusForbidden {
		t.Errorf("same-origin upload = %d, want it to reach the handler", got)
	}
	if got := send("cross-site"); got != http.StatusForbidden {
		t.Errorf("cross-site upload = %d, want 403", got)
	}
}
