package providerlink

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/pgtest"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

func newStore(t *testing.T, box *secrets.Box) (*Store, *source.DB) {
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
	return store, db
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

func conn(email, name, id, secret string) Connection {
	return Connection{Email: email, DisplayName: name, ExternalID: id, Secret: secret}
}

func TestSaveThenGetAndSecret(t *testing.T) {
	store, _ := newStore(t, newBox(t))

	link, err := store.Save("komoot", "Wilant", conn("rider@example.com", "Wilant N", "user-1", "tok-abc"))
	if err != nil {
		t.Fatal(err)
	}
	// The rider is half the key, so it is normalised on the way in —
	// otherwise "Wilant" and "wilant" become two connections for one person.
	if link.Rider != "wilant" {
		t.Errorf("rider = %q, want it lowercased", link.Rider)
	}

	got, err := store.Get("komoot", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "rider@example.com" || got.DisplayName != "Wilant N" {
		t.Errorf("got %+v, want the saved details", got)
	}

	userID, token, err := store.Secret("KOMOOT", "WILANT")
	if err != nil {
		t.Fatal(err)
	}
	if userID != "user-1" || token != "tok-abc" {
		t.Errorf("secret = %q/%q, want user-1/tok-abc", userID, token)
	}
}

// The reason the provider is part of the key rather than a second table: one
// rider signs in to both, and neither connection may stand in for the other.
func TestProvidersAreSeparateForTheSameRider(t *testing.T) {
	store, _ := newStore(t, newBox(t))

	if _, err := store.Save("komoot", "wilant", conn("k@example.com", "", "k-1", "komoot-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("garmin", "wilant", conn("g@example.com", "", "", "garmin-session")); err != nil {
		t.Fatal(err)
	}

	if _, secret, err := store.Secret("komoot", "wilant"); err != nil || secret != "komoot-token" {
		t.Errorf("komoot secret = %q (%v), want komoot-token", secret, err)
	}
	if _, secret, err := store.Secret("garmin", "wilant"); err != nil || secret != "garmin-session" {
		t.Errorf("garmin secret = %q (%v), want garmin-session", secret, err)
	}

	// Disconnecting one leaves the other alone, which is the failure this
	// test exists to catch: a DELETE keyed on the rider only.
	if err := store.Delete("garmin", "wilant"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("komoot", "wilant"); err != nil {
		t.Errorf("deleting the garmin connection took the komoot one with it: %v", err)
	}
}

// ListRiders is what the auto-import poller uses to find who to ask —
// scoped to the one provider given, in a stable order, and untouched by a
// rider's connection to some other provider.
func TestListRiders(t *testing.T) {
	store, _ := newStore(t, newBox(t))

	if _, err := store.Save("komoot", "wilant", conn("w@example.com", "", "", "tok-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("komoot", "friend", conn("f@example.com", "", "", "tok-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("garmin", "wilant", conn("w@example.com", "", "", "session-1")); err != nil {
		t.Fatal(err)
	}

	riders, err := store.ListRiders("komoot")
	if err != nil {
		t.Fatal(err)
	}
	if len(riders) != 2 || riders[0] != "friend" || riders[1] != "wilant" {
		t.Errorf("komoot riders = %v, want [friend wilant]", riders)
	}

	riders, err = store.ListRiders("wahoo")
	if err != nil {
		t.Fatal(err)
	}
	if len(riders) != 0 {
		t.Errorf("wahoo riders = %v, want none — nobody connected it", riders)
	}
}

// The point of the package. A session in the database has to be unreadable
// without the key, or encrypting it achieved nothing.
func TestSecretIsNotStoredInClear(t *testing.T) {
	store, db := newStore(t, newBox(t))

	if _, err := store.Save("komoot", "wilant", conn("r@example.com", "", "user-1", "super-secret-token")); err != nil {
		t.Fatal(err)
	}

	var stored []byte
	if err := db.Conn().QueryRow(`SELECT secret FROM provider_links WHERE rider = 'wilant'`).
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("super-secret-token")) {
		t.Fatal("the session is in the database in clear")
	}
}

func TestReconnectReplacesRatherThanDuplicates(t *testing.T) {
	store, db := newStore(t, newBox(t))

	if _, err := store.Save("komoot", "wilant", conn("old@example.com", "", "user-1", "old-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("komoot", "wilant", conn("new@example.com", "", "user-2", "new-token")); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM provider_links`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("rows = %d, want 1: reconnecting replaces", count)
	}

	_, token, err := store.Secret("komoot", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if token != "new-token" {
		t.Errorf("token = %q, want the reconnected one", token)
	}
}

func TestWithoutAKeyNothingIsStored(t *testing.T) {
	store, db := newStore(t, nil)

	if store.CanStore() {
		t.Error("CanStore is true without a key")
	}
	_, err := store.Save("komoot", "wilant", conn("r@example.com", "", "user-1", "tok"))
	if !errors.Is(err, secrets.ErrNoKey) {
		t.Errorf("Save error = %v, want ErrNoKey", err)
	}

	// Nothing partial was written on the way to refusing.
	var count int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM provider_links`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("rows = %d, want 0", count)
	}
}

// Removing the key must not break reading what is already there — the rider
// should see their connection and be told to reconnect, not meet a crash.
func TestKeyRemovedStillReadsForDisplay(t *testing.T) {
	store, db := newStore(t, newBox(t))
	if _, err := store.Save("komoot", "wilant", conn("r@example.com", "Name", "user-1", "tok")); err != nil {
		t.Fatal(err)
	}

	keyless, err := UseDB(db.Conn(), db.DSN(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keyless.Get("komoot", "wilant"); err != nil {
		t.Errorf("Get failed without a key: %v", err)
	}
	if _, _, err := keyless.Secret("komoot", "wilant"); !errors.Is(err, secrets.ErrNoKey) {
		t.Errorf("Secret error = %v, want ErrNoKey", err)
	}
}

// A key that is not the one the session was sealed with is a real operational
// case (a rotated Secret). It must say so rather than hand back nonsense.
func TestWrongKeyReportsAndSuggestsReconnecting(t *testing.T) {
	store, db := newStore(t, newBox(t))
	if _, err := store.Save("komoot", "wilant", conn("r@example.com", "", "user-1", "tok")); err != nil {
		t.Fatal(err)
	}

	rotated, err := UseDB(db.Conn(), db.DSN(), newBox(t))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = rotated.Secret("komoot", "wilant")
	if err == nil {
		t.Fatal("a session opened with the wrong key")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("reconnect")) {
		t.Errorf("error = %q, want it to say what to do", err)
	}
}

func TestGetAndDeleteForUnknownRider(t *testing.T) {
	store, _ := newStore(t, newBox(t))

	if _, err := store.Get("komoot", "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get error = %v, want ErrNotFound", err)
	}
	// Deleting what is not there is what the caller asked for.
	if err := store.Delete("komoot", "nobody"); err != nil {
		t.Errorf("Delete of an absent rider errored: %v", err)
	}
}

func TestDelete(t *testing.T) {
	store, _ := newStore(t, newBox(t))
	if _, err := store.Save("komoot", "wilant", conn("r@example.com", "", "user-1", "tok")); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("komoot", "wilant"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("komoot", "wilant"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestSaveRejectsAnEmptyRiderOrProvider(t *testing.T) {
	store, _ := newStore(t, newBox(t))
	if _, err := store.Save("komoot", "  ", conn("r@example.com", "", "user-1", "tok")); err == nil {
		t.Error("a connection was saved with no owner")
	}
	if _, err := store.Save("  ", "wilant", conn("r@example.com", "", "user-1", "tok")); err == nil {
		t.Error("a connection was saved with no provider")
	}
}

func TestSaveRejectsAnEmptySecret(t *testing.T) {
	store, _ := newStore(t, newBox(t))
	if _, err := store.Save("garmin", "wilant", conn("r@example.com", "", "", "")); err == nil {
		t.Error("a connection was saved with nothing to resume it")
	}
}

// --- migration from the Komoot-only table -------------------------------

// legacyKomootTable is the schema this package replaced, recreated verbatim so
// the migration is tested against what is actually in a running deployment.
const legacyKomootTable = `
CREATE TABLE IF NOT EXISTS komoot_links (
    rider        TEXT PRIMARY KEY,
    email        TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    user_id      TEXT NOT NULL,
    token        BLOB NOT NULL,
    updated_at   TEXT NOT NULL
);`

// A rider who connected Komoot before this release must not have to sign in
// again. The sealed token moves across as bytes, so the same key still opens
// it — which is what makes copying rather than re-encrypting safe.
func TestKomootConnectionsAreAdopted(t *testing.T) {
	box := newBox(t)
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Conn().Exec(legacyKomootTable); err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("legacy-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Conn().Exec(
		`INSERT INTO komoot_links (rider, email, display_name, user_id, token, updated_at)
		 VALUES ('wilant', 'old@example.com', 'Old Name', 'user-9', ?, '2026-01-01T00:00:00Z')`,
		sealed)
	if err != nil {
		t.Fatal(err)
	}

	store, err := UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}

	link, err := store.Get("komoot", "wilant")
	if err != nil {
		t.Fatalf("the pre-existing connection did not survive: %v", err)
	}
	if link.Email != "old@example.com" || link.DisplayName != "Old Name" {
		t.Errorf("got %+v, want the details from the old table", link)
	}

	userID, token, err := store.Secret("komoot", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if userID != "user-9" || token != "legacy-token" {
		t.Errorf("secret = %q/%q, want user-9/legacy-token", userID, token)
	}
}

// Starting twice must not resurrect a connection the rider replaced, which is
// what an unguarded copy would do on every boot.
func TestAdoptingDoesNotOverwriteANewerConnection(t *testing.T) {
	box := newBox(t)
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Conn().Exec(legacyKomootTable); err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("stale-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(
		`INSERT INTO komoot_links (rider, email, display_name, user_id, token, updated_at)
		 VALUES ('wilant', 'old@example.com', '', 'user-9', ?, '2026-01-01T00:00:00Z')`,
		sealed); err != nil {
		t.Fatal(err)
	}

	store, err := UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("komoot", "wilant", conn("new@example.com", "", "user-10", "fresh-token")); err != nil {
		t.Fatal(err)
	}

	// Second boot.
	again, err := UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := again.Secret("komoot", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if token != "fresh-token" {
		t.Errorf("token = %q, want the reconnected one: the old table overwrote it", token)
	}
}

// The common case from here on: no old table at all.
func TestStartsCleanlyWithoutTheOldTable(t *testing.T) {
	store, _ := newStore(t, newBox(t))
	if _, err := store.Get("komoot", "wilant"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get error = %v, want ErrNotFound", err)
	}
}

// PostgreSQL differs from SQLite in the places this package touches: the blob
// column type, the composite-key upsert, and how a table's existence is
// checked. All are covered above; run them again for real when a server is
// available.
func TestPostgres(t *testing.T) {
	dsn := pgtest.DSN(t)

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS provider_links`,
		`DROP TABLE IF EXISTS komoot_links`,
	} {
		if _, err := db.Conn().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	box := newBox(t)

	// A pre-existing Komoot table, so the migration runs against the engine
	// the deployment actually uses.
	if _, err := db.Conn().Exec(`
CREATE TABLE komoot_links (
    rider        TEXT PRIMARY KEY,
    email        TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    user_id      TEXT NOT NULL,
    token        BYTEA NOT NULL,
    updated_at   TEXT NOT NULL
);`); err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("legacy-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(
		`INSERT INTO komoot_links (rider, email, display_name, user_id, token, updated_at)
		 VALUES ('wilant', 'old@example.com', '', 'user-9', $1, '2026-01-01T00:00:00Z')`,
		sealed); err != nil {
		t.Fatal(err)
	}

	store, err := UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}

	if _, token, err := store.Secret("komoot", "wilant"); err != nil || token != "legacy-token" {
		t.Errorf("adopted token = %q (%v), want legacy-token", token, err)
	}

	if _, err := store.Save("garmin", "wilant", conn("r@example.com", "Name", "", "sess-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("garmin", "wilant", conn("r@example.com", "Name", "", "sess-2")); err != nil {
		t.Fatalf("upsert failed on postgres: %v", err)
	}

	_, secret, err := store.Secret("garmin", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if secret != "sess-2" {
		t.Errorf("secret = %q, want sess-2", secret)
	}
}
