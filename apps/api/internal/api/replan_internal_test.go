package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteReplanLockedReturnsConflict exercises handleReplan's
// lock-not-acquired branch directly, in package api (not api_test), since
// writeReplanLocked is unexported and there is no way to make
// pg_try_advisory_xact_lock actually fail on a second holder without a
// real PostgreSQL connection — see backgroundlock_test.go's
// TestWithDBLockSerializesConcurrentHoldersOfTheSameKey (skipped unless
// DOMESTIQUE_TEST_POSTGRES is set) for that path, and its own
// TestWithDBLockRunsUnprotectedOnANonPostgresEngine for the guarantee that
// every non-Postgres deployment (SQLite, the laptop path) can never reach
// this branch at all — withDBLock fails open there, unconditionally.
//
// What's actually worth testing here, without a database at all, is that
// this branch — once reached — produces the right HTTP contract: 409, the
// exact message the frontend keys its neutral toast off, and nothing that
// looks like a completed (if empty) replan.
func TestWriteReplanLockedReturnsConflict(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()

	s.writeReplanLocked(rec, "wilant")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != replanLockedMessage {
		t.Errorf("error = %q, want %q", body["error"], replanLockedMessage)
	}
	// Guards against a future edit accidentally shipping "removed"/"created"
	// fields alongside the error, which would make this look like a real
	// (if empty) replan happened rather than one that never ran.
	if _, ok := body["removed"]; ok {
		t.Error("locked response carries a removed count — it must look nothing like a real replan result")
	}
}
