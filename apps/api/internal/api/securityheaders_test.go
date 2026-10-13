package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
)

// newBareServer is a server with nothing wired but what these tests are about:
// the auth mode, an optional metrics token and an optional built frontend.
func newBareServer(t *testing.T, mode auth.Mode, metricsToken string) *httptest.Server {
	t.Helper()
	a, err := auth.New(auth.Config{Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{
		Auth:         a,
		MetricsToken: metricsToken,
		WebFS: fstest.MapFS{
			"index.html": {Data: []byte("<!doctype html><title>x</title>")},
		},
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// /api/metrics names every rider with a Garmin session and when it lapses;
// in oidc mode the app faces the internet, so it needs a credential.
func TestMetricsRequireTheTokenWhenAuthIsOn(t *testing.T) {
	ts := newBareServer(t, auth.ModeProxy, "s3cret-scrape-token")

	cases := []struct {
		name   string
		header map[string]string
		want   int
	}{
		{"anonymous", nil, http.StatusUnauthorized},
		{"wrong token", map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
		{"right token, wrong scheme", map[string]string{"Authorization": "Basic s3cret-scrape-token"}, http.StatusUnauthorized},
		{"a forged proxy identity is not a credential", map[string]string{auth.HeaderUser: "root", auth.HeaderGroups: "admins"}, http.StatusUnauthorized},
		{"right token", map[string]string{"Authorization": "Bearer s3cret-scrape-token"}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := get(t, ts, "/api/metrics", tc.header).StatusCode; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// Auth on and no token configured is a closed door, not an open one.
func TestMetricsAreClosedWhenAuthIsOnAndNoTokenIsSet(t *testing.T) {
	ts := newBareServer(t, auth.ModeProxy, "")
	if got := get(t, ts, "/api/metrics", nil).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", got)
	}
	if got := get(t, ts, "/api/metrics", map[string]string{"Authorization": "Bearer "}).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("empty bearer: status = %d, want 401", got)
	}
}

// `just demo` and a laptop run with auth.mode none and no token: metrics stay
// readable there, because there is nothing to protect them from.
func TestMetricsStayOpenWithoutAuthAndWithoutAToken(t *testing.T) {
	ts := newBareServer(t, auth.ModeNone, "")
	if got := get(t, ts, "/api/metrics", nil).StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
}

// Setting a token always means it is required, auth or not.
func TestAConfiguredMetricsTokenIsRequiredEvenWithoutAuth(t *testing.T) {
	ts := newBareServer(t, auth.ModeNone, "tok")
	if got := get(t, ts, "/api/metrics", nil).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", got)
	}
	if got := get(t, ts, "/api/metrics", map[string]string{"Authorization": "Bearer tok"}).StatusCode; got != http.StatusOK {
		t.Fatalf("with token: status = %d, want 200", got)
	}
}

func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	ts := newBareServer(t, auth.ModeNone, "")
	for _, path := range []string{"/", "/api/health", "/api/nope", "/some/spa/route"} {
		t.Run(path, func(t *testing.T) {
			h := get(t, ts, path, nil).Header
			if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q", got)
			}
			if got := h.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q", got)
			}
			if got := h.Get("Referrer-Policy"); got != "same-origin" {
				t.Errorf("Referrer-Policy = %q", got)
			}
			csp := h.Get("Content-Security-Policy")
			for _, want := range []string{
				"default-src 'self'",
				"script-src 'self'",
				"frame-ancestors 'none'",
				"object-src 'none'",
				"base-uri 'self'",
				"worker-src 'self' blob:",
				"https://protomaps.github.io",
			} {
				if !strings.Contains(csp, want) {
					t.Errorf("CSP lacks %q: %s", want, csp)
				}
			}
			// Scripts may never be inline or eval'd: that is the point of the policy.
			scriptSrc := directive(csp, "script-src")
			if strings.Contains(scriptSrc, "unsafe") {
				t.Errorf("script-src is not strict: %q", scriptSrc)
			}
		})
	}
}

func directive(csp, name string) string {
	for _, part := range strings.Split(csp, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, name+" ") {
			return part
		}
	}
	return ""
}

// HSTS tells a browser to refuse plain HTTP for a year. Sent over plain HTTP it
// is ignored by browsers at best and a trap at worst, so only when the request
// itself came in over HTTPS (here via the proxy's X-Forwarded-Proto).
func TestHSTSIsOnlySentForHTTPS(t *testing.T) {
	ts := newBareServer(t, auth.ModeNone, "")
	if got := get(t, ts, "/api/health", nil).Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("plain HTTP: HSTS = %q, want none", got)
	}
	got := get(t, ts, "/api/health", map[string]string{"X-Forwarded-Proto": "https"}).Header.Get("Strict-Transport-Security")
	if !strings.HasPrefix(got, "max-age=") {
		t.Errorf("HTTPS: HSTS = %q, want a max-age", got)
	}
}
