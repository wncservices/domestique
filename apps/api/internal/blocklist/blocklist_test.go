package blocklist

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

const postgresEnv = "DOMESTIQUE_TEST_POSTGRES"

func openStore(t *testing.T, dsn string) *Store {
	t.Helper()

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	// Postgres tests share a database; start clean.
	if _, err := db.Conn().Exec(`DELETE FROM blocked_emails`); err != nil {
		t.Fatal(err)
	}
	return store
}

func sqliteStore(t *testing.T) *Store {
	return openStore(t, filepath.Join(t.TempDir(), "blocklist.db"))
}

func postgresStore(t *testing.T) *Store {
	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return openStore(t, dsn)
}

func TestBlocklistEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *Store{
		"sqlite":   sqliteStore,
		"postgres": postgresStore,
	} {
		t.Run(engine, func(t *testing.T) {
			t.Run("block then is blocked", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				blocked, err := store.IsBlocked(ctx, "rider@example.com")
				if err != nil {
					t.Fatalf("is blocked: %v", err)
				}
				if blocked {
					t.Fatal("blocked = true before Block was ever called")
				}

				if err := store.Block(ctx, "Rider@Example.com", "admin", "spamming crews"); err != nil {
					t.Fatalf("block: %v", err)
				}

				blocked, err = store.IsBlocked(ctx, "rider@example.com")
				if err != nil {
					t.Fatalf("is blocked: %v", err)
				}
				if !blocked {
					t.Fatal("blocked = false after Block")
				}
			})

			t.Run("unblock is idempotent", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				if err := store.Unblock(ctx, "never-blocked@example.com"); err != nil {
					t.Fatalf("unblock never-blocked: %v", err)
				}

				if err := store.Block(ctx, "rider@example.com", "admin", ""); err != nil {
					t.Fatalf("block: %v", err)
				}
				if err := store.Unblock(ctx, "rider@example.com"); err != nil {
					t.Fatalf("unblock: %v", err)
				}
				if err := store.Unblock(ctx, "rider@example.com"); err != nil {
					t.Fatalf("second unblock: %v", err)
				}

				blocked, err := store.IsBlocked(ctx, "rider@example.com")
				if err != nil {
					t.Fatalf("is blocked: %v", err)
				}
				if blocked {
					t.Fatal("still blocked after Unblock")
				}
			})

			t.Run("block upsert replaces the previous entry", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				if err := store.Block(ctx, "rider@example.com", "admin-one", "first reason"); err != nil {
					t.Fatalf("first block: %v", err)
				}
				if err := store.Block(ctx, "rider@example.com", "admin-two", "second reason"); err != nil {
					t.Fatalf("second block: %v", err)
				}

				entries, err := store.List(ctx)
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(entries) != 1 {
					t.Fatalf("entries = %+v, want exactly 1 (replaced, not duplicated)", entries)
				}
				if entries[0].BlockedBy != "admin-two" || entries[0].Reason != "second reason" {
					t.Errorf("entry = %+v, want the second block's own details", entries[0])
				}
			})

			t.Run("email is case-insensitive", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				if err := store.Block(ctx, "  Rider@Example.com  ", "admin", ""); err != nil {
					t.Fatalf("block: %v", err)
				}
				blocked, err := store.IsBlocked(ctx, "rider@example.com")
				if err != nil {
					t.Fatalf("is blocked: %v", err)
				}
				if !blocked {
					t.Fatal("blocked = false, want true regardless of casing")
				}
			})
		})
	}
}
