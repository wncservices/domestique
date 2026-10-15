package settings

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

func TestSetThenGet(t *testing.T) {
	store, _ := newStore(t, newBox(t))

	if err := store.Set("garmin.key", "value-1", "Wilant"); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get("garmin.key")
	if err != nil {
		t.Fatal(err)
	}
	if got != "value-1" {
		t.Errorf("got %q, want value-1", got)
	}

	meta, err := store.Describe("garmin.key")
	if err != nil {
		t.Fatal(err)
	}
	if meta.UpdatedBy != "wilant" {
		t.Errorf("updatedBy = %q, want it lowercased", meta.UpdatedBy)
	}
	if meta.UpdatedAt.IsZero() {
		t.Error("updatedAt is zero")
	}
}

// The reason this package exists rather than a plain table: what it holds is
// a credential.
func TestValuesAreNotStoredInClear(t *testing.T) {
	store, db := newStore(t, newBox(t))
	if err := store.Set("garmin.key", "super-secret-consumer", ""); err != nil {
		t.Fatal(err)
	}

	var stored []byte
	if err := db.Conn().QueryRow(`SELECT value FROM settings WHERE name = 'garmin.key'`).
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("super-secret-consumer")) {
		t.Fatal("the value is in the database in clear")
	}
}

func TestSetReplaces(t *testing.T) {
	store, db := newStore(t, newBox(t))
	if err := store.Set("k", "one", "a"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("k", "two", "b"); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("rows = %d, want 1", count)
	}
	if got, _ := store.Get("k"); got != "two" {
		t.Errorf("got %q, want the replacement", got)
	}
}

func TestWithoutAKeyNothingIsStored(t *testing.T) {
	store, db := newStore(t, nil)

	if store.CanStore() {
		t.Error("CanStore is true without a key")
	}
	if err := store.Set("k", "v", ""); !errors.Is(err, secrets.ErrNoKey) {
		t.Errorf("Set error = %v, want ErrNoKey", err)
	}

	var count int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("rows = %d, want 0", count)
	}
}

// A rotated encryption key must say so rather than hand back nonsense.
func TestWrongKeyReportsAndSaysWhatToDo(t *testing.T) {
	store, db := newStore(t, newBox(t))
	if err := store.Set("k", "v", ""); err != nil {
		t.Fatal(err)
	}

	rotated, err := UseDB(db.Conn(), db.DSN(), newBox(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = rotated.Get("k")
	if err == nil {
		t.Fatal("a value opened with the wrong key")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("set it again")) {
		t.Errorf("error = %q, want it to say what to do", err)
	}

	// Describe still works, so the UI can show that something is there.
	if _, err := rotated.Describe("k"); err != nil {
		t.Errorf("Describe failed with the wrong key: %v", err)
	}
}

// A nil store is a valid configuration; handlers ask it questions anyway.
func TestNilStoreIsUsable(t *testing.T) {
	var store *Store

	if store.CanStore() {
		t.Error("a nil store says it can store")
	}
	if _, err := store.Describe("k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Describe = %v, want ErrNotFound", err)
	}
	if _, err := store.DescribeFlag("k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DescribeFlag = %v, want ErrNotFound", err)
	}
	if err := store.Delete("k"); err != nil {
		t.Errorf("Delete = %v, want nil", err)
	}
}

func TestGetUnknownAndDelete(t *testing.T) {
	store, _ := newStore(t, newBox(t))

	if _, err := store.Get("nothing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound", err)
	}
	if err := store.Delete("nothing"); err != nil {
		t.Errorf("Delete of an absent setting errored: %v", err)
	}

	if err := store.Set("k", "v", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestSetRejectsEmptyNameOrValue(t *testing.T) {
	store, _ := newStore(t, newBox(t))

	if err := store.Set("  ", "v", ""); err == nil {
		t.Error("a setting was stored with no name")
	}
	// An empty value would read back as "configured" while signing nothing.
	if err := store.Set("k", "", ""); err == nil {
		t.Error("an empty value was stored")
	}
}

// PostgreSQL differs in the blob column type and the upsert.
func TestPostgres(t *testing.T) {
	dsn := pgtest.DSN(t)

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Conn().Exec(`DROP TABLE IF EXISTS settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`DROP TABLE IF EXISTS flags`); err != nil {
		t.Fatal(err)
	}

	store, err := UseDB(db.Conn(), db.DSN(), newBox(t))
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Set("k", "one", "wilant"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("k", "two", "wilant"); err != nil {
		t.Fatalf("upsert failed on postgres: %v", err)
	}
	if got, _ := store.Get("k"); got != "two" {
		t.Errorf("got %q, want two", got)
	}
}

// Flags are the one thing this package holds that is not a credential
// (auto-sync, so far) — they must work with no encryption key at all,
// unlike Set/Get, which fail outright without one.
func TestFlagRoundTripsWithoutAnEncryptionKey(t *testing.T) {
	store, _ := newStore(t, nil)

	if enabled, err := store.Flag("auto_sync"); err != nil || enabled {
		t.Fatalf("Flag before ever set = (%v, %v), want (false, nil)", enabled, err)
	}

	if err := store.SetFlag("auto_sync", true, "Wilant"); err != nil {
		t.Fatal(err)
	}
	enabled, err := store.Flag("auto_sync")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("Flag = false after SetFlag(true)")
	}

	meta, err := store.DescribeFlag("auto_sync")
	if err != nil {
		t.Fatal(err)
	}
	if meta.UpdatedBy != "wilant" {
		t.Errorf("updatedBy = %q, want it lowercased", meta.UpdatedBy)
	}
}

func TestFlagUpsertReplaces(t *testing.T) {
	store, _ := newStore(t, nil)

	if err := store.SetFlag("auto_sync", true, "wilant"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFlag("auto_sync", false, "wilant"); err != nil {
		t.Fatal(err)
	}
	if enabled, err := store.Flag("auto_sync"); err != nil || enabled {
		t.Fatalf("Flag after flipping back off = (%v, %v), want (false, nil)", enabled, err)
	}
}

func TestSetFlagRejectsEmptyName(t *testing.T) {
	store, _ := newStore(t, nil)
	if err := store.SetFlag("  ", true, "wilant"); err == nil {
		t.Error("a flag was stored with no name")
	}
}

// A nil Store (a Server built without one, most non-settings-focused tests)
// reads every flag as off rather than panicking — the same nil-safety
// Describe/Delete already have.
func TestNilStoreFlagIsSafe(t *testing.T) {
	var store *Store
	if enabled, err := store.Flag("auto_sync"); err != nil || enabled {
		t.Errorf("Flag on a nil store = (%v, %v), want (false, nil)", enabled, err)
	}
}
