package morningsummary

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	if _, err := db.Conn().Exec(`DELETE FROM morning_summaries`); err != nil {
		t.Fatal(err)
	}
	return env{store: store, db: db}
}

func eachEngine(t *testing.T, run func(t *testing.T, e env)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { run(t, openStore(t, filepath.Join(t.TempDir(), "ms.db"))) })
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("DOMESTIQUE_TEST_POSTGRES")
		if dsn == "" {
			t.Skip("set DOMESTIQUE_TEST_POSTGRES to a PostgreSQL DSN to run this")
		}
		run(t, openStore(t, dsn))
	})
}

var when = time.Date(2026, 10, 3, 6, 30, 0, 0, time.UTC)

func TestStoreEachEngine(t *testing.T) {
	ctx := t.Context()

	t.Run("a row's enabled column defaults to false", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if _, err := e.db.Conn().Exec(`INSERT INTO morning_summaries (rider, updated_at) VALUES ('wilant', '2026-10-03T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			p, ok, err := e.store.Get(ctx, "wilant")
			if err != nil || !ok || p.Enabled || p.Email != "" || p.LastSentDate != "" {
				t.Fatalf("Get = %+v %v %v; want a disabled, empty row", p, ok, err)
			}
		})
	})

	t.Run("an unknown rider has no preference", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			_, ok, err := e.store.Get(ctx, "nobody")
			if err != nil || ok {
				t.Fatalf("Get = %v %v", ok, err)
			}
		})
	})

	t.Run("enable stores the address, disable clears it", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if err := e.store.Enable(ctx, " Wilant ", "wilant@example.com", when); err != nil {
				t.Fatal(err)
			}
			p, ok, _ := e.store.Get(ctx, "wilant")
			if !ok || !p.Enabled || p.Email != "wilant@example.com" {
				t.Fatalf("after enable: %+v", p)
			}
			if err := e.store.Disable(ctx, "wilant", when); err != nil {
				t.Fatal(err)
			}
			p, _, _ = e.store.Get(ctx, "wilant")
			if p.Enabled || p.Email != "" {
				t.Fatalf("after disable: %+v; an address kept for a rider who opted out is data kept for nothing", p)
			}
		})
	})

	t.Run("enabling needs an address", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if err := e.store.Enable(ctx, "wilant", "", when); err == nil {
				t.Fatal("enabled with no address")
			}
		})
	})

	t.Run("setting the email only touches an existing row", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if err := e.store.SetEmail(ctx, "ghost", "x@example.com", when); err != nil {
				t.Fatal(err)
			}
			if _, ok, _ := e.store.Get(ctx, "ghost"); ok {
				t.Fatal("SetEmail created a row")
			}
			_ = e.store.Enable(ctx, "wilant", "old@example.com", when)
			if err := e.store.SetEmail(ctx, "wilant", "new@example.com", when); err != nil {
				t.Fatal(err)
			}
			p, _, _ := e.store.Get(ctx, "wilant")
			if p.Email != "new@example.com" || !p.Enabled {
				t.Fatalf("%+v", p)
			}
		})
	})

	t.Run("a day is claimed once", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			_ = e.store.Enable(ctx, "wilant", "w@example.com", when)
			_ = e.store.Enable(ctx, "other", "o@example.com", when)
			claim := func(rider, date string) bool {
				ok, err := e.store.Claim(ctx, rider, date)
				if err != nil {
					t.Fatal(err)
				}
				return ok
			}
			if !claim("wilant", "2026-10-03") {
				t.Fatal("the first claim failed")
			}
			if claim("wilant", "2026-10-03") {
				t.Fatal("the same day was claimed twice")
			}
			if !claim("other", "2026-10-03") {
				t.Fatal("another rider's claim was blocked")
			}
			if !claim("wilant", "2026-10-04") {
				t.Fatal("the next day could not be claimed")
			}
			p, _, _ := e.store.Get(ctx, "wilant")
			if p.LastSentDate != "2026-10-04" {
				t.Fatalf("last sent = %q", p.LastSentDate)
			}
		})
	})

	t.Run("only an enabled rider's day can be claimed", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if ok, err := e.store.Claim(ctx, "ghost", "2026-10-03"); err != nil || ok {
				t.Fatalf("claimed a day for nobody: %v %v", ok, err)
			}
			_ = e.store.Enable(ctx, "wilant", "w@example.com", when)
			_ = e.store.Disable(ctx, "wilant", when)
			if ok, err := e.store.Claim(ctx, "wilant", "2026-10-03"); err != nil || ok {
				t.Fatalf("claimed a day for a rider who opted out: %v %v", ok, err)
			}
		})
	})

	t.Run("concurrent claimers: exactly one wins", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			_ = e.store.Enable(ctx, "wilant", "w@example.com", when)
			var wins, errs atomic.Int32
			var wg sync.WaitGroup
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ok, err := e.store.Claim(ctx, "wilant", "2026-10-03")
					switch {
					case err != nil:
						errs.Add(1)
					case ok:
						wins.Add(1)
					}
				}()
			}
			wg.Wait()
			if errs.Load() != 0 || wins.Load() != 1 {
				t.Fatalf("wins %d errors %d, want exactly one winner", wins.Load(), errs.Load())
			}
		})
	})

	t.Run("list enabled", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			_ = e.store.Enable(ctx, "b", "b@example.com", when)
			_ = e.store.Enable(ctx, "a", "a@example.com", when)
			_ = e.store.Enable(ctx, "c", "c@example.com", when)
			_ = e.store.Disable(ctx, "c", when)
			got, err := e.store.ListEnabled(ctx)
			if err != nil || len(got) != 2 || got[0].Rider != "a" || got[1].Rider != "b" {
				t.Fatalf("ListEnabled = %+v %v", got, err)
			}
		})
	})

	t.Run("delete rider", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			_ = e.store.Enable(ctx, "gone", "g@example.com", when)
			_ = e.store.Enable(ctx, "stays", "s@example.com", when)
			if err := e.store.DeleteRider(ctx, "gone"); err != nil {
				t.Fatal(err)
			}
			if _, ok, _ := e.store.Get(ctx, "gone"); ok {
				t.Error("the departed rider's address is still stored")
			}
			if _, ok, _ := e.store.Get(ctx, "stays"); !ok {
				t.Error("another rider's row went too")
			}
		})
	})

	t.Run("the schema applies twice", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, e env) {
			if _, err := UseDB(e.db.Conn(), e.db.DSN()); err != nil {
				t.Fatal(err)
			}
		})
	})
}
