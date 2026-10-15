package sessions

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/pgtest"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

func newStore(t *testing.T, box *secrets.Box) *Store {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newBox(t *testing.T) *secrets.Box {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestCreateThenLookup(t *testing.T) {
	s := newStore(t, newBox(t))
	id := auth.Identity{User: "wilant", Name: "Wilant", Email: "wilant@example.com",
		Groups: []string{"cyclists"}}

	token, expiresAt, err := s.Create(id, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Errorf("expiresAt = %v, want it in the future", expiresAt)
	}

	got, ok := s.Lookup(token)
	if !ok {
		t.Fatal("session not found")
	}
	if got.User != "wilant" || got.Name != "Wilant" || got.Email != "wilant@example.com" {
		t.Errorf("identity = %+v", got)
	}
	if len(got.Groups) != 1 || got.Groups[0] != "cyclists" {
		t.Errorf("groups = %v", got.Groups)
	}
	// Role is never trusted from storage — it is not even stored.
	if got.Role != "" {
		t.Errorf("role = %q, want empty: it is recomputed by Identify, not stored", got.Role)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t, newBox(t))
	token, _, err := s.Create(auth.Identity{User: "wilant"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(token); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(token); ok {
		t.Error("deleted session was still found")
	}

	// Deleting again, or a token that never existed, is not an error.
	if err := s.Delete(token); err != nil {
		t.Errorf("deleting an already-deleted session: %v", err)
	}
	if err := s.Delete("never-existed"); err != nil {
		t.Errorf("deleting an unknown token: %v", err)
	}
}

func TestLookupOfUnknownTokenIsFalseNotError(t *testing.T) {
	s := newStore(t, newBox(t))
	if _, ok := s.Lookup("does-not-exist"); ok {
		t.Error("unknown token was found")
	}
}

// A session past its expiry must stop answering, and the row should not
// accumulate forever — Lookup cleans it up itself rather than waiting for
// the next Create's opportunistic prune.
func TestExpiredSessionIsGoneAfterLookup(t *testing.T) {
	s := newStore(t, newBox(t))
	token, _, err := s.Create(auth.Identity{User: "wilant"}, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := s.Lookup(token); ok {
		t.Fatal("expired session was found")
	}

	var count int
	if err := s.db.QueryRow(s.dialect.Rebind(`SELECT COUNT(1) FROM sessions WHERE token = ?`), token).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("expired row was not cleaned up by Lookup")
	}
}

// Create's own opportunistic prune removes other riders' expired sessions
// too, not just the one being looked up.
func TestCreatePrunesExpiredRows(t *testing.T) {
	s := newStore(t, newBox(t))
	stale, _, err := s.Create(auth.Identity{User: "friend"}, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Create(auth.Identity{User: "wilant"}, time.Hour); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := s.db.QueryRow(s.dialect.Rebind(`SELECT COUNT(1) FROM sessions WHERE token = ?`), stale).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("a later Create did not prune an already-expired session")
	}
}

// The security-critical case: the token column must hold sha256(the real
// cookie value), never the value itself. Anyone with read access to a DB
// backup, a replica, or a future SQL-injection bug elsewhere in the stack
// must not be able to lift a live session straight out of a row — the
// encryption key on `identity` does not help with that, since the server
// holds that key too.
func TestTokenIsStoredHashedNotInThePlain(t *testing.T) {
	s := newStore(t, newBox(t))
	token, _, err := s.Create(auth.Identity{User: "wilant"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := s.db.QueryRow(`SELECT token FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("the raw token is stored in the token column")
	}
	if stored != hashToken(token) {
		t.Errorf("stored token = %q, want sha256(token) = %q", stored, hashToken(token))
	}

	// Still has to resolve, obviously — hashing at rest must be transparent
	// to every caller of Lookup/UpdateName/Delete.
	if _, ok := s.Lookup(token); !ok {
		t.Error("Lookup could not find a session it just created")
	}
}

// Tampering with the ciphertext must read as "no session", not a panic or a
// propagated decrypt error.
func TestTamperedSessionIsFalseNotError(t *testing.T) {
	s := newStore(t, newBox(t))
	token, _, err := s.Create(auth.Identity{User: "wilant"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.db.Exec(s.dialect.Rebind(
		`UPDATE sessions SET identity = ? WHERE token = ?`), []byte("not sealed data"), hashToken(token)); err != nil {
		t.Fatal(err)
	}

	if _, ok := s.Lookup(token); ok {
		t.Error("a tampered session was accepted")
	}
}

// CanStore's nil-safety, same family as providerlink and settings' handler
// survival tests: a *Store that is nil, or has no box, must not panic — it
// is the "OIDC not configured" state, not a bug.
func TestSessionsSurviveNoStore(t *testing.T) {
	var nilStore *Store
	if nilStore.CanStore() {
		t.Error("nil store reports CanStore")
	}
	if _, _, err := nilStore.Create(auth.Identity{User: "wilant"}, time.Hour); err == nil {
		t.Error("nil store created a session")
	}
	if _, ok := nilStore.Lookup("anything"); ok {
		t.Error("nil store found a session")
	}
	if err := nilStore.Delete("anything"); err != nil {
		t.Errorf("nil store Delete: %v", err)
	}

	noBox := newStore(t, nil)
	if noBox.CanStore() {
		t.Error("store without a box reports CanStore")
	}
	if _, _, err := noBox.Create(auth.Identity{User: "wilant"}, time.Hour); err == nil {
		t.Error("store without a box created a session")
	}
}

func TestRefusesASessionForNobody(t *testing.T) {
	s := newStore(t, newBox(t))
	if _, _, err := s.Create(auth.Identity{}, time.Hour); err == nil {
		t.Error("a session was created for an anonymous identity")
	}
}

func TestPostgres(t *testing.T) {
	dsn := pgtest.DSN(t)

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Conn().Exec(`DROP TABLE IF EXISTS sessions`); err != nil {
		t.Fatal(err)
	}

	box := newBox(t)
	store, err := UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}

	token, _, err := store.Create(auth.Identity{User: "wilant", Groups: []string{"cyclists"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := store.Lookup(token)
	if !ok || got.User != "wilant" {
		t.Fatalf("lookup = %+v, %v", got, ok)
	}
	if err := store.Delete(token); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Lookup(token); ok {
		t.Error("deleted session still found on postgres")
	}
}
