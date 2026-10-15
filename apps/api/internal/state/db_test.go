package state

import (
	"github.com/wncservices/domestique/apps/api/internal/pgtest"
	"path/filepath"
	"testing"
)

func openSQLiteStore(t *testing.T) Store {
	t.Helper()
	store, err := OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open sqlite state: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func openPostgresStore(t *testing.T) Store {
	t.Helper()

	dsn := pgtest.DSN(t)

	store, err := OpenDB(dsn)
	if err != nil {
		t.Fatalf("open postgres state: %v", err)
	}
	// Tests share one database, so start from a clean table.
	if _, err := store.db.Exec(`DROP TABLE IF EXISTS sync_state`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := store.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	t.Cleanup(func() { store.Close() })
	return store
}

// The same behaviour the file store guarantees, on every engine. Sync state is
// the one thing that must not be wrong: an empty read means "push everything
// again", and a stale one means a route silently never updates.
func TestDBStoreEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) Store{
		"sqlite":   openSQLiteStore,
		"postgres": openPostgresStore,
	}

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("empty to begin with", func(t *testing.T) {
				if entries := mustAll(t, open(t)); len(entries) != 0 {
					t.Errorf("fresh store has %d entries", len(entries))
				}
			})

			t.Run("record and read back", func(t *testing.T) {
				store := open(t)

				if err := store.Record(t.Context(), Entry{
					AccountID: "garmin:one", Slug: "kemmelberg-loop",
					RemoteID: "remote-1", ContentHash: "abc", Name: "Kemmelberg Loop",
				}); err != nil {
					t.Fatal(err)
				}

				entries := mustAll(t, store)
				if len(entries) != 1 {
					t.Fatalf("got %d entries, want 1", len(entries))
				}
				got := entries[0]
				if got.RemoteID != "remote-1" || got.ContentHash != "abc" || got.Name != "Kemmelberg Loop" {
					t.Errorf("entry came back wrong: %+v", got)
				}
				if got.UpdatedAt == "" {
					t.Error("no timestamp recorded")
				}
			})

			// Recording twice is an update, not a duplicate. Postgres and
			// SQLite spell the upsert the same way, but only just.
			t.Run("record is an upsert", func(t *testing.T) {
				store := open(t)

				base := Entry{AccountID: "garmin:one", Slug: "loop", RemoteID: "r1", ContentHash: "v1"}
				if err := store.Record(t.Context(), base); err != nil {
					t.Fatal(err)
				}
				updated := base
				updated.ContentHash = "v2"
				updated.RemoteID = "r2"
				if err := store.Record(t.Context(), updated); err != nil {
					t.Fatal(err)
				}

				entries := mustAll(t, store)
				if len(entries) != 1 {
					t.Fatalf("got %d entries, want the row replaced not duplicated", len(entries))
				}
				if entries[0].ContentHash != "v2" || entries[0].RemoteID != "r2" {
					t.Errorf("upsert did not overwrite: %+v", entries[0])
				}
			})

			// The same route on two accounts is two rows: one provider failing
			// must not mark the route synced everywhere.
			t.Run("accounts are independent", func(t *testing.T) {
				store := open(t)

				for _, account := range []string{"garmin:one", "wahoo:two"} {
					if err := store.Record(t.Context(), Entry{
						AccountID: account, Slug: "loop",
						RemoteID: "remote-" + account, ContentHash: "abc",
					}); err != nil {
						t.Fatal(err)
					}
				}

				garmin := mustForAccount(t, store, "garmin:one")
				if len(garmin) != 1 || garmin["loop"].RemoteID != "remote-garmin:one" {
					t.Errorf("garmin view = %+v", garmin)
				}

				if err := store.Forget(t.Context(), "garmin:one", "loop"); err != nil {
					t.Fatal(err)
				}
				if len(mustForAccount(t, store, "garmin:one")) != 0 {
					t.Error("forget did not remove the entry")
				}
				if len(mustForAccount(t, store, "wahoo:two")) != 1 {
					t.Error("forget removed the other account's entry too")
				}
			})

			t.Run("forget is harmless when nothing matches", func(t *testing.T) {
				if err := open(t).Forget(t.Context(), "garmin:one", "never-existed"); err != nil {
					t.Errorf("forget on a missing entry returned %v", err)
				}
			})

			t.Run("unknown account reads empty", func(t *testing.T) {
				if got := mustForAccount(t, open(t), "nobody"); len(got) != 0 {
					t.Errorf("unknown account returned %d entries", len(got))
				}
			})

			t.Run("all is sorted", func(t *testing.T) {
				store := open(t)

				for _, e := range []Entry{
					{AccountID: "wahoo:two", Slug: "zebra"},
					{AccountID: "garmin:one", Slug: "beta"},
					{AccountID: "garmin:one", Slug: "alpha"},
				} {
					if err := store.Record(t.Context(), e); err != nil {
						t.Fatal(err)
					}
				}

				want := []string{"garmin:one/alpha", "garmin:one/beta", "wahoo:two/zebra"}
				for i, entry := range mustAll(t, store) {
					if got := entry.AccountID + "/" + entry.Slug; got != want[i] {
						t.Errorf("entry %d = %q, want %q", i, got, want[i])
					}
				}
			})
		})
	}
}

// State has to outlive the process, which is the entire reason it is stored at
// all. With a database that means no volume — the point of moving it here.
func TestDBStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	first, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Record(t.Context(), Entry{
		AccountID: "garmin:one", Slug: "loop", RemoteID: "remote-1", ContentHash: "abc",
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	entries := mustAll(t, second)
	if len(entries) != 1 || entries[0].RemoteID != "remote-1" {
		t.Fatalf("state did not survive: %+v", entries)
	}
}

// Migrating an existing table must not wipe it, or an upgrade re-pushes every
// route to every device.
func TestMigrateIsIdempotent(t *testing.T) {
	store := openSQLiteStore(t).(*DBStore)

	if err := store.Record(t.Context(), Entry{AccountID: "a", Slug: "b", RemoteID: "r", ContentHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if entries := mustAll(t, store); len(entries) != 1 {
		t.Errorf("migrating again lost the state: %d entries", len(entries))
	}
}
