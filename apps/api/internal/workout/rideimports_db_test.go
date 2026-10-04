package workout

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

// importClock is a fixed instant with an explicit zone, so the tests mean the
// same under TZ=UTC and TZ=Europe/Brussels.
var importClock = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)

func TestRideImportsEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			const rider = "imp-store-" + "rider"
			t.Cleanup(func() {
				_, _ = db.db.Exec(`DELETE FROM ride_imports WHERE rider LIKE 'imp-store-%'`)
			})
			_, _ = db.db.Exec(`DELETE FROM ride_imports WHERE rider LIKE 'imp-store-%'`)

			if _, ok, err := db.LatestRideImport(ctx, rider, importClock); err != nil || ok {
				t.Fatalf("a rider with no import: %v %v", ok, err)
			}

			if err := db.StartRideImport(ctx, rider, "job-1", importClock); err != nil {
				t.Fatal(err)
			}
			got, ok, err := db.LatestRideImport(ctx, rider, importClock)
			if err != nil || !ok {
				t.Fatalf("latest: %v %v", ok, err)
			}
			if got.ID != "job-1" || got.State != ImportRunning || got.Phase != ImportReading || got.Rider != rider ||
				got.StartedAt != "2026-10-04T09:00:00Z" || got.UpdatedAt != "2026-10-04T09:00:00Z" {
				t.Errorf("fresh job = %+v", got)
			}

			// Counts and phase upsert as the job goes.
			counts := RideImportCounts{Added: 12, Duplicate: 3, SkippedSport: 1, Unsupported: 4, Unreadable: 2}
			if err := db.UpdateRideImport(ctx, "job-1", ImportAnalysing, counts, importClock.Add(10*time.Second)); err != nil {
				t.Fatal(err)
			}
			got, _, _ = db.LatestRideImport(ctx, rider, importClock.Add(20*time.Second))
			if got.Phase != ImportAnalysing || got.RideImportCounts != counts || got.UpdatedAt != "2026-10-04T09:00:10Z" || got.State != ImportRunning {
				t.Errorf("after update = %+v", got)
			}

			// One active import per rider, and only that rider's.
			if err := db.StartRideImport(ctx, rider, "job-2", importClock.Add(30*time.Second)); !errors.Is(err, ErrImportRunning) {
				t.Errorf("second start = %v, want ErrImportRunning", err)
			}
			if err := db.StartRideImport(ctx, "imp-store-other", "job-other", importClock.Add(30*time.Second)); err != nil {
				t.Errorf("another rider's import was refused: %v", err)
			}
			if _, ok, _ := db.LatestRideImport(ctx, "imp-store-third", importClock); ok {
				t.Error("a rider saw somebody else's import")
			}

			// A running job nobody has touched for two minutes was killed by a restart.
			got, _, _ = db.LatestRideImport(ctx, rider, importClock.Add(10*time.Second+ImportStaleAfter+time.Second))
			if got.State != ImportInterrupted {
				t.Errorf("a stale running job reads %q, want interrupted", got.State)
			}
			got, _, _ = db.LatestRideImport(ctx, rider, importClock.Add(10*time.Second+ImportStaleAfter-time.Second))
			if got.State != ImportRunning {
				t.Errorf("a job two minutes minus a second old reads %q, want running", got.State)
			}

			if err := db.FinishRideImport(ctx, "job-1", ImportDone, "", counts, importClock.Add(40*time.Second)); err != nil {
				t.Fatal(err)
			}
			got, _, _ = db.LatestRideImport(ctx, rider, importClock.Add(41*time.Second))
			if got.State != ImportDone || got.Error != "" || got.RideImportCounts != counts || got.UpdatedAt != "2026-10-04T09:00:40Z" {
				t.Errorf("finished = %+v", got)
			}
			// A finished job never reads as interrupted, however old.
			got, _, _ = db.LatestRideImport(ctx, rider, importClock.Add(72*time.Hour))
			if got.State != ImportDone {
				t.Errorf("an old finished job reads %q", got.State)
			}

			// The next one can start, becomes the latest, and a failure keeps its class.
			if err := db.StartRideImport(ctx, rider, "job-3", importClock.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := db.FinishRideImport(ctx, "job-3", ImportFailed, "too large", RideImportCounts{Added: 5}, importClock.Add(time.Hour+time.Minute)); err != nil {
				t.Fatal(err)
			}
			got, _, _ = db.LatestRideImport(ctx, rider, importClock.Add(2*time.Hour))
			if got.ID != "job-3" || got.State != ImportFailed || got.Error != "too large" || got.Added != 5 {
				t.Errorf("latest = %+v, want the failed job-3 with its class and the rides it kept", got)
			}
		})
	}
}

func TestStartRideImportTakesOverAJobAStaleRestartLeft(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			const rider = "imp-store-stale"
			t.Cleanup(func() { _, _ = db.db.Exec(`DELETE FROM ride_imports WHERE rider LIKE 'imp-store-%'`) })
			_, _ = db.db.Exec(`DELETE FROM ride_imports WHERE rider LIKE 'imp-store-%'`)

			if err := db.StartRideImport(ctx, rider, "old", importClock); err != nil {
				t.Fatal(err)
			}
			// The pod died. Without this the rider could never import again.
			later := importClock.Add(ImportStaleAfter + time.Minute)
			if err := db.StartRideImport(ctx, rider, "new", later); err != nil {
				t.Fatalf("a stale running job blocked a new one: %v", err)
			}
			got, _, _ := db.LatestRideImport(ctx, rider, later)
			if got.ID != "new" || got.State != ImportRunning {
				t.Errorf("latest = %+v", got)
			}
			var oldState, oldError string
			if err := db.db.QueryRow(db.query(`SELECT state, error FROM ride_imports WHERE id = ?`), "old").Scan(&oldState, &oldError); err != nil {
				t.Fatal(err)
			}
			if oldState != ImportFailed || oldError != "interrupted" {
				t.Errorf("the stale job is %q/%q, want failed/interrupted", oldState, oldError)
			}
		})
	}
}

func TestRideImportSchemaAppliesTwice(t *testing.T) {
	for engine, dsn := range map[string]func(*testing.T) string{
		"sqlite":   func(t *testing.T) string { return t.TempDir() + "/twice.db" },
		"postgres": func(t *testing.T) string { return postgresDSN(t) },
	} {
		t.Run(engine, func(t *testing.T) {
			src, err := source.OpenDB(dsn(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { src.Close() })
			for i := 0; i < 2; i++ {
				if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
					t.Fatalf("UseDB #%d: %v", i+1, err)
				}
			}
		})
	}
}

func TestEarliestSessionDateEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			if d, err := db.EarliestSessionDate(ctx, "wilant"); err != nil || d != "" {
				t.Fatalf("no sessions: %q %v", d, err)
			}
			for i, date := range []string{"2026-03-02", "2021-03-05", "2024-01-01"} {
				if _, err := db.UpsertSession(ctx, UpsertSessionRequest{Rider: "wilant", Provider: "import", ExternalID: string(rune('a' + i)),
					Sport: "cycling", Date: date, DurationSeconds: 60}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.UpsertSession(ctx, UpsertSessionRequest{Rider: "other", Provider: "import", ExternalID: "z", Sport: "cycling", Date: "2010-01-01"}); err != nil {
				t.Fatal(err)
			}
			if d, err := db.EarliestSessionDate(ctx, "Wilant"); err != nil || d != "2021-03-05" {
				t.Errorf("earliest = %q %v, want 2021-03-05", d, err)
			}
		})
	}
}

func postgresDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return dsn
}
