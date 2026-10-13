package api_test

import (
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/auth"
)

// RFC 6750: the scheme is "Bearer", compared case-insensitively. Any other
// scheme carrying the right secret is still not a bearer credential.
func TestMetricsBearerSchemeRules(t *testing.T) {
	ts := newBareServer(t, auth.ModeProxy, "tok-123")
	for header, want := range map[string]int{
		"Bearer tok-123":   http.StatusOK,
		"bearer tok-123":   http.StatusOK,
		"BEARER tok-123":   http.StatusOK,
		"Basic tok-123":    http.StatusUnauthorized,
		"Token tok-123":    http.StatusUnauthorized,
		"tok-123":          http.StatusUnauthorized,
		"Bearer":           http.StatusUnauthorized,
		"Bearer ":          http.StatusUnauthorized,
		"Bearer tok-1234":  http.StatusUnauthorized,
		"Bearer  tok-123":  http.StatusUnauthorized,
		"Bearertok-123":    http.StatusUnauthorized,
		"Bearer tok-12":    http.StatusUnauthorized,
		"Bearer TOK-123":   http.StatusUnauthorized,
		"Bearer tok-123 x": http.StatusUnauthorized,
	} {
		if got := get(t, ts, "/api/metrics", map[string]string{"Authorization": header}).StatusCode; got != want {
			t.Errorf("Authorization %q: status = %d, want %d", header, got, want)
		}
	}
}

// A token pasted from a secret file usually ends in a newline; that must not
// make it unmatchable.
func TestMetricsTokenIsTrimmed(t *testing.T) {
	ts := newBareServer(t, auth.ModeProxy, "tok-123\n")
	if got := get(t, ts, "/api/metrics", map[string]string{"Authorization": "Bearer tok-123"}).StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
}
