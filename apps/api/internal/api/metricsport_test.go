package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
)

// The internal listener serves the registry at /metrics and nothing else.
func TestMetricsListenerServesOnlyMetrics(t *testing.T) {
	ts := httptest.NewServer(api.MetricsHandler())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "domestique_") && !strings.Contains(string(body), "# HELP") {
		t.Fatalf("GET /metrics: status = %d, body = %.200s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q", got)
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/metrics"},
		{http.MethodGet, "/api/me"},
		{http.MethodGet, "/"},
		{http.MethodPost, "/metrics"},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		r, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode == http.StatusOK {
			t.Errorf("%s %s answered 200 on the metrics listener", tc.method, tc.path)
		}
	}
}

// It is its own http.Server, with timeouts, that shuts down gracefully.
func TestMetricsServerHasTimeoutsAndShutsDown(t *testing.T) {
	srv := api.NewMetricsServer("127.0.0.1:0")
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 {
		t.Fatalf("missing timeouts: %+v", srv)
	}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	time.Sleep(100 * time.Millisecond)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != http.ErrServerClosed {
		t.Fatalf("ListenAndServe returned %v", err)
	}
}

// With auth on, the public port does not serve metrics at all: they are on the
// internal port, which the ingress never routes to.
func TestMetricsAreNotOnThePublicPortWhenAuthIsOn(t *testing.T) {
	proxy := newBareServer(t, auth.ModeProxy)
	for name, hdr := range map[string]map[string]string{
		"anonymous":      nil,
		"a proxy rider":  {auth.HeaderUser: "alice", auth.HeaderGroups: "domestique-admins"},
		"a bearer token": {"Authorization": "Bearer anything"},
	} {
		if got := get(t, proxy, "/api/metrics", hdr).StatusCode; got != http.StatusNotFound {
			t.Errorf("proxy mode, %s: status = %d, want 404", name, got)
		}
	}

	h := newSSOHarness(t)
	if got := h.get("/api/metrics").StatusCode; got != http.StatusNotFound {
		t.Errorf("oidc mode: status = %d, want 404", got)
	}
}

// Auth off (`just demo`, a laptop) keeps it, so local dev needs nothing extra.
func TestMetricsStayOnThePublicPortWithoutAuth(t *testing.T) {
	ts := newBareServer(t, auth.ModeNone)
	if got := get(t, ts, "/api/metrics", nil).StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
}
