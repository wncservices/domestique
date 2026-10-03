package pacingpush

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
	if _, err := db.Conn().Exec(`DELETE FROM pacing_pushes`); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestPacingPushesEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *Store{
		"sqlite": func(t *testing.T) *Store { return openStore(t, filepath.Join(t.TempDir(), "p.db")) },
		"postgres": func(t *testing.T) *Store {
			dsn := os.Getenv(postgresEnv)
			if dsn == "" {
				t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
			}
			return openStore(t, dsn)
		},
	} {
		t.Run(engine, func(t *testing.T) {
			t.Run("record, read back and replace", func(t *testing.T) {
				s := open(t)
				ctx := t.Context()
				if _, ok, err := s.Get(ctx, "wilant", "garmin", "pacing:fondo"); err != nil || ok {
					t.Fatalf("before any push: ok=%v err=%v", ok, err)
				}
				if err := s.Put(ctx, "Wilant", "garmin", "pacing:fondo", "course-1"); err != nil {
					t.Fatal(err)
				}
				got, ok, err := s.Get(ctx, "wilant", "garmin", "pacing:fondo")
				if err != nil || !ok || got != "course-1" {
					t.Fatalf("Get = %q ok=%v err=%v, want course-1", got, ok, err)
				}
				// A second push replaces the remote id: one row, not two.
				if err := s.Put(ctx, "wilant", "garmin", "pacing:fondo", "course-2"); err != nil {
					t.Fatal(err)
				}
				if got, _, _ := s.Get(ctx, "wilant", "garmin", "pacing:fondo"); got != "course-2" {
					t.Errorf("after the second push remote id = %q, want course-2", got)
				}
				var n int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM pacing_pushes`).Scan(&n); err != nil || n != 1 {
					t.Errorf("rows = %d (err %v), want 1", n, err)
				}
			})

			t.Run("keyed on rider, provider and key", func(t *testing.T) {
				s := open(t)
				ctx := t.Context()
				for _, c := range [][3]string{
					{"wilant", "garmin", "pacing:a"}, {"wilant", "wahoo", "pacing:a"},
					{"wilant", "garmin", "pacing:b"}, {"other", "garmin", "pacing:a"},
				} {
					if err := s.Put(ctx, c[0], c[1], c[2], c[0]+"/"+c[1]+"/"+c[2]); err != nil {
						t.Fatal(err)
					}
				}
				if got, _, _ := s.Get(ctx, "other", "garmin", "pacing:a"); got != "other/garmin/pacing:a" {
					t.Errorf("another rider's row = %q", got)
				}
				if _, ok, _ := s.Get(ctx, "other", "wahoo", "pacing:a"); ok {
					t.Error("a row for another provider appeared")
				}
			})

			t.Run("forget one and delete a rider's", func(t *testing.T) {
				s := open(t)
				ctx := t.Context()
				_ = s.Put(ctx, "wilant", "garmin", "pacing:a", "1")
				_ = s.Put(ctx, "wilant", "wahoo", "pacing:a", "2")
				_ = s.Put(ctx, "other", "garmin", "pacing:a", "3")
				if err := s.Forget(ctx, "wilant", "garmin", "pacing:a"); err != nil {
					t.Fatal(err)
				}
				if _, ok, _ := s.Get(ctx, "wilant", "garmin", "pacing:a"); ok {
					t.Error("Forget left the row")
				}
				if err := s.Forget(ctx, "wilant", "garmin", "pacing:a"); err != nil {
					t.Errorf("forgetting what is gone is not an error: %v", err)
				}
				n, err := s.DeleteRider(ctx, "WILANT")
				if err != nil || n != 1 {
					t.Errorf("DeleteRider = %d, %v; want the one remaining row", n, err)
				}
				if _, ok, _ := s.Get(ctx, "other", "garmin", "pacing:a"); !ok {
					t.Error("DeleteRider removed another rider's row")
				}
			})
		})
	}
}

// UseDB is safe to run twice, on a table that already exists.
func TestSchemaIsIdempotent(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	first, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Put(t.Context(), "wilant", "garmin", "pacing:a", "x"); err != nil {
		t.Fatal(err)
	}
	second, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("second UseDB: %v", err)
	}
	if got, ok, _ := second.Get(t.Context(), "wilant", "garmin", "pacing:a"); !ok || got != "x" {
		t.Errorf("a second UseDB lost the row: %q %v", got, ok)
	}
}
