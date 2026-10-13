package api_test

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
)

// The sign-in state cookie is sealed, so it cannot be forged, but a captured
// one stays valid for as long as the server is willing to open it. Its own
// IssuedAt is what bounds that, whatever the browser does with Max-Age.
func TestSSOCallbackRefusesAStaleStateCookie(t *testing.T) {
	var skew atomic.Int64 // seconds the server clock is ahead of the real one
	h := newSSOHarness(t, func(s *api.Server) {
		s.Clock = func() time.Time { return time.Now().Add(time.Duration(skew.Load()) * time.Second) }
	})

	// Within the window the sign-in completes.
	fresh := h.get("/sso/login?return_to=/settings")
	loc, err := fresh.Location()
	if err != nil {
		t.Fatal(err)
	}
	h.issuer.setClaims(loc.Query().Get("nonce"), nil)
	skew.Store(9 * 60)
	if got := h.get("/sso/callback?code=x&state=" + loc.Query().Get("state")); got.StatusCode != http.StatusFound {
		t.Fatalf("a 9-minute-old sign-in: status = %d, want the redirect that completes it", got.StatusCode)
	}

	// Past it, the same flow is refused as an expired sign-in.
	skew.Store(0)
	stale := h.get("/sso/login?return_to=/settings")
	loc, err = stale.Location()
	if err != nil {
		t.Fatal(err)
	}
	h.issuer.setClaims(loc.Query().Get("nonce"), nil)
	skew.Store(11 * 60)
	got := h.get("/sso/callback?code=x&state=" + loc.Query().Get("state"))
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("an 11-minute-old sign-in: status = %d, want 400", got.StatusCode)
	}
}
