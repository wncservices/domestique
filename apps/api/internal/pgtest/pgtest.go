// Package pgtest gives each PostgreSQL test a schema of its own.
//
// Every test package used to open the one shared database named by
// DOMESTIQUE_TEST_POSTGRES. Several tests DROP TABLE to force an error path,
// so two packages running at once could pull a table out from under each
// other's migration ("relation routes does not exist"), which is why CI had to
// run packages one at a time. A schema per test removes the sharing: tables,
// rows and DROPs are all private, and packages can run in parallel.
//
// This package is imported only from _test.go files.
package pgtest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	// The driver the app itself opens PostgreSQL with.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Env is the variable that points the suite at a PostgreSQL server.
const Env = "DOMESTIQUE_TEST_POSTGRES"

// RequireEnv, when set to 1, turns "no PostgreSQL configured" from a skip into
// a failure. CI sets it: a skipped engine reads green while covering nothing,
// so in CI a missing or unreachable server must be loud in every package, not
// just the one a separate step happens to check.
const RequireEnv = "DOMESTIQUE_REQUIRE_POSTGRES"

// Enabled reports whether a PostgreSQL server was configured. Under RequireEnv
// an unconfigured server panics instead of quietly dropping the PostgreSQL
// half of a test.
func Enabled() bool {
	if os.Getenv(Env) != "" {
		return true
	}
	if os.Getenv(RequireEnv) == "1" {
		panic(Env + " is not set but " + RequireEnv + "=1: the PostgreSQL tests would be skipped")
	}
	return false
}

// DSN skips the test when no server is configured; otherwise it creates a
// uniquely named schema, returns the configured DSN with search_path pointing
// at it, and drops the schema (CASCADE) when the test ends.
func DSN(t testing.TB) string {
	t.Helper()
	if !Enabled() {
		t.Skipf("set %s to a PostgreSQL DSN to run this", Env)
	}
	base := os.Getenv(Env)
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "t_" + hex.EncodeToString(b[:])

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + name); err != nil {
		_ = admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + name + ` CASCADE`)
		_ = admin.Close()
	})
	return withSearchPath(t, base, name)
}

func withSearchPath(t testing.TB, base, schema string) string {
	t.Helper()
	if !strings.Contains(base, "://") {
		return base + " search_path=" + schema // key/value form
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %s: %v", Env, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
