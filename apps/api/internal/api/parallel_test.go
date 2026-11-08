package api_test

import (
	"os"
	"sync"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
)

// Harness constructors call parallel(t) so the suite's tests run side by side:
// each test owns its database (testdb.Path, pgtest.DSN) and its own server, so
// nothing is shared between them. A test that touches process-wide state (an
// environment variable, a package-level seam set through a ForTest function, the
// shared metrics registry read before and after) calls serialOnly(t) first and
// stays in the sequential phase, which finishes before any parallel test resumes.
var (
	parallelMu sync.Mutex
	parallelOf = map[*testing.T]bool{} // true: already parallel, false: opted out
)

func parallel(t *testing.T) {
	t.Helper()
	parallelMu.Lock()
	state, seen := parallelOf[t]
	if !seen {
		parallelOf[t] = true
	}
	parallelMu.Unlock()
	if seen {
		_ = state // opted out, or already parallel: nothing to do
		return
	}
	t.Cleanup(func() {
		parallelMu.Lock()
		delete(parallelOf, t)
		parallelMu.Unlock()
	})
	t.Parallel()
}

// serialOnly keeps t out of the parallel phase. Call it before any harness
// constructor.
func serialOnly(t *testing.T) {
	t.Helper()
	parallelMu.Lock()
	parallelOf[t] = false
	parallelMu.Unlock()
}

// TestMain puts a consumer pair in the environment once, so the default harness
// is a deployment where Garmin sign-in is available. Set here rather than inherited
// (whether the tests pass must not depend on real Garmin credentials exported on the
// machine) and rather than with t.Setenv, which a parallel test cannot use. Tests
// that need it unset or different set it themselves and run serially.
func TestMain(m *testing.M) {
	os.Setenv(garmin.EnvConsumerKey, "test-consumer-key")
	os.Setenv(garmin.EnvConsumerSecret, "test-consumer-secret")
	os.Exit(m.Run())
}
