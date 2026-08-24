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

				if _, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "", "wilant"); err != nil {
					t.Fatalf("create later: %v", err)
				}
				earlier, err := store.Create(ctx, "crew:sunday-club", "flat-loop", "2026-09-01", "", "wilant")
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

				if _, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "", "wilant"); err != nil {
					t.Fatalf("create: %v", err)
				}
				if _, err := store.Create(ctx, "crew:other-crew", "flat-loop", "2026-09-05", "", "wilant"); err != nil {
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

				ride, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "", "wilant")
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				other, err := store.Create(ctx, "crew:sunday-club", "flat-loop", "2026-09-06", "", "wilant")
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

			t.Run("DeleteForCrew removes only that crew's rides", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				gone, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "", "wilant")
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				untouched, err := store.Create(ctx, "crew:other-crew", "flat-loop", "2026-09-05", "", "wilant")
				if err != nil {
					t.Fatalf("create: %v", err)
				}

				if err := store.DeleteForCrew(ctx, "crew:sunday-club"); err != nil {
					t.Fatalf("DeleteForCrew: %v", err)
				}
				if _, err := store.Get(ctx, gone.ID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("get after DeleteForCrew: err = %v, want ErrNotFound", err)
				}
				if _, err := store.Get(ctx, untouched.ID); err != nil {
					t.Fatalf("a different crew's ride should be untouched: %v", err)
				}
			})

			t.Run("DeleteForCrew on a crew with no rides is not an error", func(t *testing.T) {
				store := open(t)
				if err := store.DeleteForCrew(t.Context(), "crew:nothing-scheduled"); err != nil {
					t.Fatalf("DeleteForCrew: %v", err)
				}
			})

			t.Run("rejects a malformed date", func(t *testing.T) {
				store := open(t)
				for _, bad := range []string{
					"not-a-date",
					"2026/09/05", // wrong separators
					"20260905",   // wrong shape entirely
					"2026-13-05", // no month 13
					"2026-02-30", // February has no 30th
				} {
					if _, err := store.Create(t.Context(), "crew:sunday-club", "hill-loop", bad, "", "wilant"); err == nil {
						t.Errorf("%q: expected an error, got none", bad)
					}
				}
			})

			t.Run("rejects a malformed time but accepts an empty one", func(t *testing.T) {
				store := open(t)
				for _, bad := range []string{
					"not-a-time",
					"25:00", // no hour 25
					"09:60", // no minute 60
				} {
					if _, err := store.Create(t.Context(), "crew:sunday-club", "hill-loop", "2026-09-05", bad, "wilant"); err == nil {
						t.Errorf("%q: expected an error, got none", bad)
					}
				}
				if _, err := store.Create(t.Context(), "crew:sunday-club", "hill-loop", "2026-09-05", "", "wilant"); err != nil {
					t.Errorf("empty time: expected no error, got %v", err)
				}
			})

			t.Run("time round-trips through create and list", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				created, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "09:30", "wilant")
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if created.Time != "09:30" {
					t.Fatalf("created.Time = %q, want 09:30", created.Time)
				}

				rides, err := store.ListForCrew(ctx, "crew:sunday-club")
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(rides) != 1 || rides[0].Time != "09:30" {
					t.Fatalf("rides = %+v, want one ride at 09:30", rides)
				}

				got, err := store.Get(ctx, created.ID)
				if err != nil {
					t.Fatalf("get: %v", err)
				}
				if got.Time != "09:30" {
					t.Fatalf("got.Time = %q, want 09:30", got.Time)
				}
			})

			t.Run("ListUpcoming spans every crew, filters by date, ignores nothing", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				past, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-01", "", "wilant")
				if err != nil {
					t.Fatalf("create past: %v", err)
				}
				todayRide, err := store.Create(ctx, "crew:other-crew", "flat-loop", "2026-09-05", "", "wilant")
				if err != nil {
					t.Fatalf("create today: %v", err)
				}
				future, err := store.Create(ctx, "crew:sunday-club", "flat-loop", "2026-09-10", "", "wilant")
				if err != nil {
					t.Fatalf("create future: %v", err)
				}

				rides, err := store.ListUpcoming(ctx, "2026-09-05")
				if err != nil {
					t.Fatalf("ListUpcoming: %v", err)
				}
				if len(rides) != 2 {
					t.Fatalf("rides = %+v, want exactly the from-date and future rides, not the past one (%v)", rides, past.ID)
				}
				if rides[0].ID != todayRide.ID || rides[1].ID != future.ID {
					t.Fatalf("rides = %+v, want today's ride first, then the future one, across both crews", rides)
				}
			})

			t.Run("requires a crew, a route and a rider", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()
				if _, err := store.Create(ctx, "", "hill-loop", "2026-09-05", "", "wilant"); err == nil {
					t.Fatal("expected an error for a missing crew id")
				}
				if _, err := store.Create(ctx, "crew:sunday-club", "", "2026-09-05", "", "wilant"); err == nil {
					t.Fatal("expected an error for a missing slug")
				}
				if _, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "", ""); err == nil {
					t.Fatal("expected an error for a missing rider")
				}
			})
		})
	}
}
