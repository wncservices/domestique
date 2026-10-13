package identities

import (
	"context"
	"path/filepath"
	"testing"
)

// A dry run must be able to look at a database without changing it: UseDB
// creates the table and ends every session, Attach does neither.
func TestAttachReadsWithoutCreatingOrMigrating(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "attach.db"))

	exists, err := Exists(db.Conn(), db.DSN())
	if err != nil || exists {
		t.Fatalf("Exists on a bare database = %v, %v; want false", exists, err)
	}
	if _, err := Attach(db.Conn(), db.DSN(), nil); err == nil {
		t.Error("Attach on a database without the table succeeded")
	}
	if exists, _ := Exists(db.Conn(), db.DSN()); exists {
		t.Error("Attach created the table")
	}

	store, err := UseDB(db.Conn(), db.DSN(), newBox(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bind(context.Background(), issuer, "g|1", "wilant", ""); err != nil {
		t.Fatal(err)
	}
	if exists, _ := Exists(db.Conn(), db.DSN()); !exists {
		t.Fatal("Exists is false after UseDB")
	}
	attached, err := Attach(db.Conn(), db.DSN(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, ok, err := attached.Get(context.Background(), issuer, "g|1"); err != nil || !ok || b.Rider != "wilant" {
		t.Errorf("Get through Attach = %+v, %v, %v", b, ok, err)
	}
}
