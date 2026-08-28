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
	if _, err := db.Conn().Exec(`DELETE FROM ride_series`); err != nil {
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

// preSeriesIDStore opens db and puts crew_rides in the table shape it had
// immediately before series_id existed — the shape every real deployment's
// database was actually in when that migration first shipped. Postgres
// tests share a database, so this drops whatever a previous test left
// first, the same way openStore's own DELETE does.
func preSeriesIDStore(t *testing.T, dsn string) *source.DB {
	t.Helper()
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Conn().Exec(`DROP TABLE IF EXISTS crew_rides`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`DROP TABLE IF EXISTS ride_series`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`
CREATE TABLE crew_rides (
    id         TEXT PRIMARY KEY,
    crew_id    TEXT NOT NULL,
    route_slug TEXT NOT NULL,
    date       TEXT NOT NULL,
    time       TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestUseDBMigratesATableThatPredatesSeriesID is a regression test for a
// real production incident: UseDB failed outright — "column series_id does
// not exist" — against every database that already had a crew_rides table
// from before this migration shipped, because the index on series_id used
// to be created inside schema()'s own CREATE TABLE IF NOT EXISTS block,
// before the ALTER TABLE that actually adds the column to a pre-existing
// table ever ran. TestScheduleEachEngine's own openStore always starts
// from UseDB having already succeeded once, so a fresh test database never
// exercised this path — exactly why it shipped unnoticed until it
// crash-looped a real deployment.
func TestUseDBMigratesATableThatPredatesSeriesID(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *source.DB{
		"sqlite": func(t *testing.T) *source.DB {
			return preSeriesIDStore(t, filepath.Join(t.TempDir(), "schedule.db"))
		},
		"postgres": func(t *testing.T) *source.DB {
			dsn := os.Getenv(postgresEnv)
			if dsn == "" {
				t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
			}
			return preSeriesIDStore(t, dsn)
		},
	} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)

			store, err := UseDB(db.Conn(), db.DSN())
			if err != nil {
				t.Fatalf("UseDB against a pre-existing table without series_id: %v", err)
			}

			// The migration has to actually work end to end, not just not
			// error — a series created afterward needs the column and the
			// index both real.
			_, rides, err := store.CreateSeries(t.Context(), "crew:sunday-club", "hill-loop", 1,
				"2026-09-01", "2026-09-08", "", "wilant")
			if err != nil {
				t.Fatalf("CreateSeries after migrating a pre-existing table: %v", err)
			}
			if len(rides) != 2 {
				t.Fatalf("rides = %+v, want 2", rides)
			}
		})
	}
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

			t.Run("ClearCreatedBy blanks authorship without deleting the ride", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				authored, err := store.Create(ctx, "crew:sunday-club", "hill-loop", "2026-09-05", "", "wilant")
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				untouched, err := store.Create(ctx, "crew:sunday-club", "flat-loop", "2026-09-05", "", "tiebe")
				if err != nil {
					t.Fatalf("create: %v", err)
				}

				n, err := store.ClearCreatedBy(ctx, "wilant")
				if err != nil {
					t.Fatalf("ClearCreatedBy: %v", err)
				}
				if n != 1 {
					t.Fatalf("cleared %d rides, want 1", n)
				}

				got, err := store.Get(ctx, authored.ID)
				if err != nil {
					t.Fatalf("get after ClearCreatedBy: %v", err)
				}
				if got.CreatedBy != "" {
					t.Errorf("CreatedBy = %q, want blanked", got.CreatedBy)
				}

				other, err := store.Get(ctx, untouched.ID)
				if err != nil {
					t.Fatalf("get untouched ride: %v", err)
				}
				if other.CreatedBy != "tiebe" {
					t.Errorf("a different rider's ride should be untouched, got CreatedBy = %q", other.CreatedBy)
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

			t.Run("CreateSeries generates one ride per interval, all sharing a series id", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				series, rides, err := store.CreateSeries(ctx, "crew:sunday-club", "hill-loop", 2,
					"2026-09-01", "2026-09-29", "09:30", "wilant")
				if err != nil {
					t.Fatalf("CreateSeries: %v", err)
				}
				// Every 2 weeks from Sep 1 through Sep 29 inclusive: 1, 15, 29 — 3 rides.
				want := []string{"2026-09-01", "2026-09-15", "2026-09-29"}
				if len(rides) != len(want) {
					t.Fatalf("rides = %+v, want %d occurrences", rides, len(want))
				}
				for i, date := range want {
					if rides[i].Date != date {
						t.Errorf("rides[%d].Date = %q, want %q", i, rides[i].Date, date)
					}
					if rides[i].SeriesID != series.ID {
						t.Errorf("rides[%d].SeriesID = %q, want %q", i, rides[i].SeriesID, series.ID)
					}
					if rides[i].Time != "09:30" {
						t.Errorf("rides[%d].Time = %q, want 09:30", i, rides[i].Time)
					}
				}

				listed, err := store.ListForCrew(ctx, "crew:sunday-club")
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(listed) != 3 {
					t.Fatalf("listed = %+v, want the 3 generated rides to show up like any other ride", listed)
				}
			})

			t.Run("CreateSeries caps at 52 occurrences regardless of the requested range", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				// Weekly for 10 years would be ~520 occurrences without a cap.
				_, rides, err := store.CreateSeries(ctx, "crew:sunday-club", "hill-loop", 1,
					"2026-01-01", "2036-01-01", "", "wilant")
				if err != nil {
					t.Fatalf("CreateSeries: %v", err)
				}
				if len(rides) != maxSeriesOccurrences {
					t.Fatalf("len(rides) = %d, want the cap of %d", len(rides), maxSeriesOccurrences)
				}
			})

			t.Run("CreateSeries rejects an unsupported interval and an inverted range", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				if _, _, err := store.CreateSeries(ctx, "crew:sunday-club", "hill-loop", 3,
					"2026-09-01", "2026-09-29", "", "wilant"); err == nil {
					t.Error("expected an error for an unsupported interval")
				}
				if _, _, err := store.CreateSeries(ctx, "crew:sunday-club", "hill-loop", 1,
					"2026-09-29", "2026-09-01", "", "wilant"); err == nil {
					t.Error("expected an error for an end date before the start date")
				}
			})

			t.Run("DeleteSeries removes only future occurrences, leaves history and other series alone", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				series, _, err := store.CreateSeries(ctx, "crew:sunday-club", "hill-loop", 1,
					"2026-09-01", "2026-09-22", "", "wilant")
				if err != nil {
					t.Fatalf("CreateSeries: %v", err)
				}
				other, err := store.Create(ctx, "crew:sunday-club", "flat-loop", "2026-09-08", "", "wilant")
				if err != nil {
					t.Fatalf("create a plain ride: %v", err)
				}

				// "Today" is 2026-09-08: the 09-01 occurrence has already happened.
				n, err := store.DeleteSeries(ctx, series.ID, "2026-09-08")
				if err != nil {
					t.Fatalf("DeleteSeries: %v", err)
				}
				if n != 3 {
					t.Fatalf("deleted %d rides, want 3 (09-08, 09-15, 09-22)", n)
				}

				remaining, err := store.ListForCrew(ctx, "crew:sunday-club")
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(remaining) != 2 {
					t.Fatalf("remaining = %+v, want the 09-01 occurrence and the unrelated plain ride left", remaining)
				}
				var gotPast, gotOther bool
				for _, r := range remaining {
					if r.Date == "2026-09-01" {
						gotPast = true
					}
					if r.ID == other.ID {
						gotOther = true
					}
				}
				if !gotPast {
					t.Error("the already-happened 09-01 occurrence should not have been deleted")
				}
				if !gotOther {
					t.Error("a plain ride outside the series should not have been touched")
				}
			})

			t.Run("DeleteSeries on a series with nothing left in range is not an error", func(t *testing.T) {
				store := open(t)
				n, err := store.DeleteSeries(t.Context(), "no-such-series", "2026-09-08")
				if err != nil {
					t.Fatalf("DeleteSeries: %v", err)
				}
				if n != 0 {
					t.Fatalf("n = %d, want 0", n)
				}
			})
		})
	}
}
