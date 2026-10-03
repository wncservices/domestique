package calendarfeed

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

const postgresEnv = "DOMESTIQUE_TEST_POSTGRES"

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
	if _, err := db.Conn().Exec(`DELETE FROM calendar_feeds`); err != nil {
		t.Fatal(err)
	}
	return env{store: store, db: db}
}

func sqliteEnv(t *testing.T) env {
	return openStore(t, filepath.Join(t.TempDir(), "calendar.db"))
}

func postgresEnvStore(t *testing.T) env {
	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return openStore(t, dsn)
}

func eachEngine(t *testing.T, run func(t *testing.T, e env)) {
	t.Helper()
	for engine, open := range map[string]func(*testing.T) env{"sqlite": sqliteEnv, "postgres": postgresEnvStore} {
		t.Run(engine, func(t *testing.T) { run(t, open(t)) })
	}
}

func TestStoreEachEngine(t *testing.T) {
	ctx := t.Context()

	t.Run("only the sha256 of the token is stored", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			token, err := e.store.Regenerate(ctx, "wilant")
			if err != nil {
				t.Fatal(err)
			}
			if len(token) != 43 {
				t.Fatalf("token is %d characters, want 43 (32 bytes, base64url)", len(token))
			}
			sum := sha256.Sum256([]byte(token))
			var hash, rider, createdAt, lastFetched string
			if err := e.db.Conn().QueryRow(`SELECT rider, token_hash, created_at, last_fetched_at FROM calendar_feeds`).
				Scan(&rider, &hash, &createdAt, &lastFetched); err != nil {
				t.Fatal(err)
			}
			if hash != hex.EncodeToString(sum[:]) {
				t.Fatalf("token_hash = %q, want sha256 hex of the token", hash)
			}
			if strings.Contains(rider+hash+createdAt+lastFetched, token) {
				t.Fatal("the raw token appears in a column")
			}
			if lastFetched != "" {
				t.Fatalf("a new feed has been fetched by nobody, got %q", lastFetched)
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

	t.Run("regenerating invalidates the previous token and leaves one row", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			first, _ := e.store.Regenerate(ctx, "wilant")
			second, err := e.store.Regenerate(ctx, "wilant")
			if err != nil {
				t.Fatal(err)
			}
			if first == second {
				t.Fatal("regenerate returned the same token")
			}
			if _, err := e.store.Lookup(ctx, first); !errors.Is(err, ErrNotFound) {
				t.Fatalf("the old token still works: %v", err)
			}
			if rider, err := e.store.Lookup(ctx, second); err != nil || rider != "wilant" {
				t.Fatalf("the new token: %q, %v", rider, err)
			}
			var n int
			if err := e.db.Conn().QueryRow(`SELECT COUNT(*) FROM calendar_feeds`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("rows = %d, %v; want 1", n, err)
			}
		})
	})

	t.Run("regenerating resets the last fetched time", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if _, err := e.store.Regenerate(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			if err := e.store.Touch(ctx, "wilant", time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			if _, err := e.store.Regenerate(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			st, err := e.store.Status(ctx, "wilant")
			if err != nil || !st.LastFetchedAt.IsZero() {
				t.Fatalf("status = %+v, %v; a fresh link has not been fetched", st, err)
			}
		})
	})

	t.Run("a revoked, a never issued and a malformed token fail identically", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			token, _ := e.store.Regenerate(ctx, "wilant")
			if err := e.store.Revoke(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			_, revoked := e.store.Lookup(ctx, token)
			_, never := e.store.Lookup(ctx, strings.Repeat("A", 43))
			_, empty := e.store.Lookup(ctx, "")
			for name, err := range map[string]error{"revoked": revoked, "never issued": never, "empty": empty} {
				if !errors.Is(err, ErrNotFound) {
					t.Errorf("%s: %v, want ErrNotFound", name, err)
				}
			}
			if revoked.Error() != never.Error() {
				t.Errorf("revoked %q and unknown %q differ", revoked, never)
			}
		})
	})

	t.Run("revoking twice, or what was never there, is not an error", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if err := e.store.Revoke(ctx, "nobody"); err != nil {
				t.Fatal(err)
			}
			_, _ = e.store.Regenerate(ctx, "wilant")
			for range 2 {
				if err := e.store.Revoke(ctx, "wilant"); err != nil {
					t.Fatal(err)
				}
			}
		})
	})

	t.Run("status", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			st, err := e.store.Status(ctx, "wilant")
			if err != nil || st.Active {
				t.Fatalf("no feed yet: %+v, %v", st, err)
			}
			if _, err := e.store.Regenerate(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			st, err = e.store.Status(ctx, "wilant")
			if err != nil || !st.Active || st.CreatedAt.IsZero() || !st.LastFetchedAt.IsZero() {
				t.Fatalf("a fresh feed: %+v, %v", st, err)
			}
			_ = e.store.Revoke(ctx, "wilant")
			if st, _ = e.store.Status(ctx, "wilant"); st.Active {
				t.Fatalf("a revoked feed is not active: %+v", st)
			}
		})
	})

	t.Run("touch writes at most once an hour", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if _, err := e.store.Regenerate(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
			fetched := func() time.Time {
				st, err := e.store.Status(ctx, "wilant")
				if err != nil {
					t.Fatal(err)
				}
				return st.LastFetchedAt
			}
			if err := e.store.Touch(ctx, "wilant", t0); err != nil {
				t.Fatal(err)
			}
			if got := fetched(); !got.Equal(t0) {
				t.Fatalf("first touch wrote %v, want %v", got, t0)
			}
			if err := e.store.Touch(ctx, "wilant", t0.Add(59*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if got := fetched(); !got.Equal(t0) {
				t.Fatalf("a touch inside the hour wrote %v", got)
			}
			t2 := t0.Add(61 * time.Minute)
			if err := e.store.Touch(ctx, "wilant", t2); err != nil {
				t.Fatal(err)
			}
			if got := fetched(); !got.Equal(t2) {
				t.Fatalf("a touch after the hour wrote %v, want %v", got, t2)
			}
		})
	})

	t.Run("touching a feed that does not exist is not an error", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if err := e.store.Touch(ctx, "nobody", time.Now()); err != nil {
				t.Fatal(err)
			}
		})
	})

	t.Run("delete rider removes the feed", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			token, _ := e.store.Regenerate(ctx, "gone")
			other, _ := e.store.Regenerate(ctx, "stays")
			if err := e.store.DeleteRider(ctx, "gone"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.store.Lookup(ctx, token); !errors.Is(err, ErrNotFound) {
				t.Fatalf("the departed rider's link still works: %v", err)
			}
			if _, err := e.store.Lookup(ctx, other); err != nil {
				t.Fatalf("another rider's link was removed: %v", err)
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
