package applehealthlink

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/pgtest"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

type env struct {
	store *Store
	db    *source.DB
}

func openStore(t *testing.T, dsn string) env {
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
	if _, err := db.Conn().Exec(`DELETE FROM apple_health_links`); err != nil {
		t.Fatal(err)
	}
	return env{store: store, db: db}
}

func sqliteEnv(t *testing.T) env {
	return openStore(t, filepath.Join(t.TempDir(), "applehealth.db"))
}

func postgresEnv(t *testing.T) env {
	return openStore(t, pgtest.DSN(t))
}

func eachEngine(t *testing.T, run func(t *testing.T, e env)) {
	t.Helper()
	for engine, open := range map[string]func(*testing.T) env{"sqlite": sqliteEnv, "postgres": postgresEnv} {
		t.Run(engine, func(t *testing.T) { run(t, open(t)) })
	}
}

func TestStoreEachEngine(t *testing.T) {
	ctx := t.Context()

	t.Run("regenerating kills the old token", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			first, err := e.store.Regenerate(ctx, "wilant")
			if err != nil {
				t.Fatal(err)
			}
			second, err := e.store.Regenerate(ctx, "wilant")
			if err != nil {
				t.Fatal(err)
			}
			if first == second {
				t.Fatal("regenerate returned the same token")
			}
			if rider, err := e.store.Lookup(ctx, first); err != nil || rider != "" {
				t.Fatalf("the old token still resolves: %q, %v", rider, err)
			}
			if rider, err := e.store.Lookup(ctx, second); err != nil || rider != "wilant" {
				t.Fatalf("the new token: %q, %v; want wilant", rider, err)
			}
			var n int
			if err := e.db.Conn().QueryRow(`SELECT COUNT(*) FROM apple_health_links`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("rows = %d, %v; want 1", n, err)
			}
		})
	})

	t.Run("lookup of an unknown token is empty and not an error", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if _, err := e.store.Regenerate(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			for _, token := range []string{strings.Repeat("A", 43), ""} {
				rider, err := e.store.Lookup(ctx, token)
				if err != nil || rider != "" {
					t.Fatalf("Lookup(%q) = %q, %v; want empty and nil", token, rider, err)
				}
			}
		})
	})

	t.Run("the token is stored hashed", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			token, err := e.store.Regenerate(ctx, "wilant")
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(token))
			var hash, rider, createdAt, received string
			if err := e.db.Conn().QueryRow(`SELECT rider, token_hash, created_at, last_received_at FROM apple_health_links`).
				Scan(&rider, &hash, &createdAt, &received); err != nil {
				t.Fatal(err)
			}
			if hash == token {
				t.Fatal("token_hash is the raw token")
			}
			if hash != hex.EncodeToString(sum[:]) {
				t.Fatalf("token_hash = %q, want sha256 hex of the token", hash)
			}
			if strings.Contains(rider+hash+createdAt+received, token) {
				t.Fatal("the raw token appears in a column")
			}
		})
	})

	t.Run("lookup finds the owner, normalised", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			token, err := e.store.Regenerate(ctx, " Wilant ")
			if err != nil {
				t.Fatal(err)
			}
			rider, err := e.store.Lookup(ctx, token)
			if err != nil || rider != "wilant" {
				t.Fatalf("Lookup = %q, %v; want wilant", rider, err)
			}
		})
	})

	t.Run("revoke unlinks", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			token, err := e.store.Regenerate(ctx, "wilant")
			if err != nil {
				t.Fatal(err)
			}
			if err := e.store.Revoke(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			linked, err := e.store.Linked(ctx, "wilant")
			if err != nil || linked {
				t.Fatalf("Linked after revoke = %v, %v; want false", linked, err)
			}
			if rider, err := e.store.Lookup(ctx, token); err != nil || rider != "" {
				t.Fatalf("Lookup after revoke = %q, %v; want empty", rider, err)
			}
			if err := e.store.Revoke(ctx, "wilant"); err != nil {
				t.Fatalf("revoking twice: %v", err)
			}
		})
	})

	t.Run("status and linked before and after a link", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			st, err := e.store.Status(ctx, "wilant")
			if err != nil || st.Linked {
				t.Fatalf("no link yet: %+v, %v", st, err)
			}
			if _, err := e.store.Regenerate(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			st, err = e.store.Status(ctx, "wilant")
			if err != nil || !st.Linked || st.CreatedAt == "" || st.LastReceivedAt != "" {
				t.Fatalf("a fresh link: %+v, %v", st, err)
			}
			if linked, err := e.store.Linked(ctx, "wilant"); err != nil || !linked {
				t.Fatalf("Linked = %v, %v; want true", linked, err)
			}
		})
	})

	t.Run("touch sets last received", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if _, err := e.store.Regenerate(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			t0 := time.Date(2026, 11, 5, 6, 30, 0, 0, time.UTC)
			if err := e.store.Touch(ctx, "wilant", t0); err != nil {
				t.Fatal(err)
			}
			st, err := e.store.Status(ctx, "wilant")
			if err != nil || st.LastReceivedAt != t0.Format(time.RFC3339) {
				t.Fatalf("last received = %q, %v; want %s", st.LastReceivedAt, err, t0.Format(time.RFC3339))
			}
			// Every accepted post writes; a later one moves the time on.
			t1 := t0.Add(5 * time.Minute)
			if err := e.store.Touch(ctx, "wilant", t1); err != nil {
				t.Fatal(err)
			}
			if st, _ = e.store.Status(ctx, "wilant"); st.LastReceivedAt != t1.Format(time.RFC3339) {
				t.Fatalf("second touch: last received = %q, want %s", st.LastReceivedAt, t1.Format(time.RFC3339))
			}
		})
	})

	t.Run("touching a rider with no link is not an error", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if err := e.store.Touch(ctx, "nobody", time.Now()); err != nil {
				t.Fatal(err)
			}
		})
	})

	t.Run("regenerating resets last received", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if _, err := e.store.Regenerate(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			if err := e.store.Touch(ctx, "wilant", time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			if _, err := e.store.Regenerate(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			st, err := e.store.Status(ctx, "wilant")
			if err != nil || st.LastReceivedAt != "" {
				t.Fatalf("a fresh link has received nothing: %+v, %v", st, err)
			}
		})
	})

	t.Run("delete rider removes the link", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			token, _ := e.store.Regenerate(ctx, "gone")
			other, _ := e.store.Regenerate(ctx, "stays")
			if err := e.store.DeleteRider(ctx, "gone"); err != nil {
				t.Fatal(err)
			}
			if rider, _ := e.store.Lookup(ctx, token); rider != "" {
				t.Fatalf("the departed rider's shortcut still resolves to %q", rider)
			}
			if rider, _ := e.store.Lookup(ctx, other); rider != "stays" {
				t.Fatalf("another rider's shortcut was removed: %q", rider)
			}
		})
	})

	t.Run("the schema applies twice", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if _, err := UseDB(e.db.Conn(), e.db.DSN()); err != nil {
				t.Fatalf("second UseDB: %v", err)
			}
		})
	})
}
