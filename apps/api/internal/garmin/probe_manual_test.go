package garmin

import (
	"errors"
	"os"
	"testing"
)

// TestProbeLive asks Garmin what a rejection looks like today, using an
// address that cannot exist and a password that is not one.
//
// Skipped unless GARMIN_PROBE=1. It talks to a live third party on an
// undocumented endpoint: run it when the sign-in starts failing and you need
// to know whether the flow still behaves the way this package assumes, not on
// every CI run. Repeated runs will eventually earn a Cloudflare block, which
// is itself worth knowing but not worth provoking.
func TestProbeLive(t *testing.T) {
	if os.Getenv("GARMIN_PROBE") != "1" {
		t.Skip("set GARMIN_PROBE=1 to probe the live endpoint")
	}
	_, err := New().Login(t.Context(), "domestique-probe-does-not-exist@example.com", "not-a-real-password")
	t.Logf("err = %v", err)
	t.Logf("credentials=%v mfa=%v blocked=%v",
		errors.Is(err, ErrBadCredentials), errors.Is(err, ErrMFARequired), errors.Is(err, ErrBlocked))
}
