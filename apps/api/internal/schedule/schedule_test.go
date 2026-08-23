package schedule

import (
	"errors"
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
	if _, err := db.Conn().Exec(`DELETE FROM crew_rides`); err != nil {
		t.Fatal(err)
	}
	return store
}

func sqliteStore(t *testing.T) *Store {
	return openStore(t, filepath.Join(t.TempDir(), "schedule.db"))
}

func postgresStore(t *testing.T) *Store {
	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return openStore(t, dsn)
}

func TestScheduleEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *Store{
		"sqlite":   sqliteStore,
		"postgres": postgresStore,
	} {
		t.Run(engine, func(t *testing.T) {
			t.Run("create then list, soonest first", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				if _, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "wilant"); err != nil {
					t.Fatalf("create later: %v", err)
				}
				earlier, err := store.Create(ctx, "crew:sunday-club", "flat-loop", "2026-09-01", "wilant")
				if err != nil {
					t.Fatalf("create earlier: %v", err)
				}

				rides, err := store.ListForCrew(ctx, "crew:sunday-club")
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(rides) != 2 {
					t.Fatalf("len(rides) = %d, want 2", len(rides))
				}
				if rides[0].ID != earlier.ID {
					t.Fatalf("rides[0] = %+v, want the earlier-dated ride first", rides[0])
				}
			})

			t.Run("list is scoped to one crew", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				if _, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "wilant"); err != nil {
					t.Fatalf("create: %v", err)
				}
				if _, err := store.Create(ctx, "crew:other-crew", "flat-loop", "2026-09-05", "wilant"); err != nil {
					t.Fatalf("create: %v", err)
				}

				rides, err := store.ListForCrew(ctx, "crew:sunday-club")
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(rides) != 1 || rides[0].Slug != "hill-loop" {
					t.Fatalf("rides = %+v, want just hill-loop", rides)
				}
			})

			t.Run("delete removes it and nothing else", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				ride, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "wilant")
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				other, err := store.Create(ctx, "crew:sunday-club", "flat-loop", "2026-09-06", "wilant")
				if err != nil {
					t.Fatalf("create: %v", err)
				}

				if err := store.Delete(ctx, ride.ID); err != nil {
					t.Fatalf("delete: %v", err)
				}
				if _, err := store.Get(ctx, ride.ID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("get after delete: err = %v, want ErrNotFound", err)
				}
				if _, err := store.Get(ctx, other.ID); err != nil {
					t.Fatalf("the other ride should be untouched: %v", err)
				}
			})

			t.Run("delete of an unknown id is ErrNotFound", func(t *testing.T) {
				store := open(t)
				if err := store.Delete(t.Context(), "nope"); !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
			})

			t.Run("rejects a malformed date", func(t *testing.T) {
				store := open(t)
				if _, err := store.Create(t.Context(), "crew:sunday-club", "hill-loop", "not-a-date", "wilant"); err == nil {
					t.Fatal("expected an error for a malformed date")
				}
			})

			t.Run("requires a crew, a route and a rider", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()
				if _, err := store.Create(ctx, "", "hill-loop", "2026-09-05", "wilant"); err == nil {
					t.Fatal("expected an error for a missing crew id")
				}
				if _, err := store.Create(ctx, "crew:sunday-club", "", "2026-09-05", "wilant"); err == nil {
					t.Fatal("expected an error for a missing slug")
				}
				if _, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", ""); err == nil {
					t.Fatal("expected an error for a missing rider")
				}
			})
		})
	}
}
