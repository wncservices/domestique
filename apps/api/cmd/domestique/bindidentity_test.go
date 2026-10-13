package main

import (
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/identities"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

const cliIssuer = "https://tenant.example/"

// withKey sets the deployment's encryption key for the command under test, and
// returns the box built from it for the test's own use.
func withKey(t *testing.T) *secrets.Box {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(secrets.EnvKey, key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func openIdentities(t *testing.T, dsn string, box *secrets.Box) (*identities.Store, *source.DB) {
	t.Helper()
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ids, err := identities.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	return ids, db
}

func riderOf(t *testing.T, ids *identities.Store, sub string) (string, bool) {
	t.Helper()
	b, ok, err := ids.Get(t.Context(), cliIssuer, sub)
	if err != nil {
		t.Fatal(err)
	}
	return b.Rider, ok
}

func TestBindIdentityBindsAndEndsTheAccountsSessions(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	box := withKey(t)

	// A login the account already holds, made when a name chose the rider.
	ids, db := openIdentities(t, dsn, box)
	ss, err := sessions.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := ss.Create(auth.Identity{User: "wilant-2", Sub: "auth0|abc"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ids.Bind(t.Context(), cliIssuer, "auth0|abc", "wilant-2", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ss.Create(auth.Identity{User: "sam", Sub: "auth0|sam"}, time.Hour); err != nil {
		t.Fatal(err)
	}

	out := mustRun(t, "bind-identity", cliIssuer, "auth0|abc", "Wilant")
	for _, want := range []string{"wilant-2", "wilant", "moved"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if rider, ok := riderOf(t, ids, "auth0|abc"); !ok || rider != "wilant" {
		t.Errorf("binding = %q, %v; want wilant", rider, ok)
	}
	if _, ok := ss.Lookup(tok); ok {
		t.Error("the account's old session still works")
	}
	if n := countRows(t, db, "sessions"); n != 1 {
		t.Errorf("%d sessions left, want only sam's", n)
	}
}

func TestBindIdentityCreatesABindingAndSaysSo(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	withKey(t)

	out := mustRun(t, "bind-identity", cliIssuer, "google-oauth2|1", "pat")
	if !strings.Contains(out, "bound") || !strings.Contains(out, "pat") {
		t.Errorf("output does not say it bound pat:\n%s", out)
	}
	ids, _ := openIdentities(t, dsn, nil)
	if rider, ok := riderOf(t, ids, "google-oauth2|1"); !ok || rider != "pat" {
		t.Errorf("binding = %q, %v; want pat", rider, ok)
	}
}

func TestBindIdentityDryRunWritesNothing(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	withKey(t)

	// Before the table exists at all: a dry run must not create it (creating
	// it would also end every session).
	out := mustRun(t, "bind-identity", "--dry-run", cliIssuer, "auth0|abc", "wilant")
	if !strings.Contains(out, "dry run") || !strings.Contains(out, "would bind") {
		t.Errorf("output should say what it would do:\n%s", out)
	}
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if exists, _ := identities.Exists(db.Conn(), db.DSN()); exists {
		t.Fatal("a dry run created the bindings table")
	}

	// And with a binding in place.
	ids, _ := openIdentities(t, dsn, nil)
	if _, err := ids.Bind(t.Context(), cliIssuer, "auth0|abc", "wilant-2", ""); err != nil {
		t.Fatal(err)
	}
	out = mustRun(t, "bind-identity", "--dry-run", cliIssuer, "auth0|abc", "wilant")
	if !strings.Contains(out, "would move") {
		t.Errorf("output should say it would move:\n%s", out)
	}
	if rider, _ := riderOf(t, ids, "auth0|abc"); rider != "wilant-2" {
		t.Errorf("a dry run moved the binding to %q", rider)
	}
}

func TestUnbindIdentityRemovesTheBindingAndHonoursDryRun(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	box := withKey(t)
	ids, db := openIdentities(t, dsn, box)
	ss, err := sessions.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := ss.Create(auth.Identity{User: "wilant", Sub: "auth0|abc"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ids.Bind(t.Context(), cliIssuer, "auth0|abc", "wilant", ""); err != nil {
		t.Fatal(err)
	}

	out := mustRun(t, "unbind-identity", "--dry-run", cliIssuer, "auth0|abc")
	if !strings.Contains(out, "dry run") || !strings.Contains(out, "would unbind") {
		t.Errorf("dry run output:\n%s", out)
	}
	if _, ok := riderOf(t, ids, "auth0|abc"); !ok {
		t.Fatal("a dry run removed the binding")
	}
	if _, ok := ss.Lookup(tok); !ok {
		t.Fatal("a dry run ended the session")
	}

	out = mustRun(t, "unbind-identity", cliIssuer, "auth0|abc")
	if !strings.Contains(out, "unbound") {
		t.Errorf("output:\n%s", out)
	}
	if _, ok := riderOf(t, ids, "auth0|abc"); ok {
		t.Error("the binding is still there")
	}
	if _, ok := ss.Lookup(tok); ok {
		t.Error("the session survived the unbinding")
	}

	// Nothing to unbind is reported, not an error.
	out = mustRun(t, "unbind-identity", cliIssuer, "auth0|abc")
	if !strings.Contains(out, "no binding") {
		t.Errorf("output for an account with no binding:\n%s", out)
	}
}

func TestBindIdentityWithoutAKeyStillBindsButSaysSessionsWereNotEnded(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	t.Setenv(secrets.EnvKey, "")

	out := mustRun(t, "bind-identity", cliIssuer, "auth0|abc", "pat")
	if !strings.Contains(out, secrets.EnvKey) {
		t.Errorf("output should say why sessions were not ended:\n%s", out)
	}
	ids, _ := openIdentities(t, dsn, nil)
	if rider, ok := riderOf(t, ids, "auth0|abc"); !ok || rider != "pat" {
		t.Errorf("binding = %q, %v", rider, ok)
	}
}

func TestBindIdentityRejectsBadInput(t *testing.T) {
	workspace(t)
	withKey(t)
	for _, args := range [][]string{
		{"bind-identity"},
		{"bind-identity", cliIssuer, "auth0|abc"},
		{"bind-identity", cliIssuer, "auth0|abc", "pat", "extra"},
		{"bind-identity", cliIssuer, "auth0|abc", "has spaces"},
		{"bind-identity", cliIssuer, "auth0|abc", "   "},
		{"bind-identity", "", "auth0|abc", "pat"},
		{"bind-identity", cliIssuer, "", "pat"},
		{"unbind-identity"},
		{"unbind-identity", cliIssuer},
	} {
		if out, err := capture(t, args...); err == nil {
			t.Errorf("%v: no error, output:\n%s", args, out)
		}
	}
}

func countRows(t *testing.T, db *source.DB, table string) int {
	t.Helper()
	var n int
	// #nosec G701 -- table is a constant in this file.
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
