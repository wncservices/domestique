package routeshare

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

// A table made before the binding columns existed gets them, keeps its rows,
// and a second start-up changes nothing.
func TestUseDBMigratesATablePredatingTheBinding(t *testing.T) {
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	conn := db.Conn()
	for _, stmt := range []string{
		`CREATE TABLE route_shares (id TEXT PRIMARY KEY, route_slug TEXT NOT NULL, created_by TEXT NOT NULL,
            created_at TEXT NOT NULL, expires_at TEXT NOT NULL, revoked_at TEXT)`,
		`INSERT INTO route_shares VALUES ('old', 'hill-loop', 'alice', '2026-01-01T00:00:00Z', '2099-01-01T00:00:00Z', NULL)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		store, err := UseDB(conn, db.DSN())
		if err != nil {
			t.Fatalf("UseDB: %v", err)
		}
		got, err := store.Get(t.Context(), "old")
		if err != nil {
			t.Fatalf("legacy share unreadable: %v", err)
		}
		if got.RouteOwner != "" || got.RouteCreatedAt != "" {
			t.Fatalf("legacy binding = %q, %q; want empty", got.RouteOwner, got.RouteCreatedAt)
		}
	}
}
