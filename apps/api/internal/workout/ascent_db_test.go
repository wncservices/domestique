package workout

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestSessionAscentRoundTripsOnEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			s, err := db.UpsertSession(ctx, UpsertSessionRequest{
				Rider: "wilant", Provider: "garmin", ExternalID: "1", Sport: "cycling", Date: "2026-05-01",
				DurationSeconds: 3600, DistanceM: 30000, AscentM: 412.5,
			})
			if err != nil {
				t.Fatal(err)
			}
			if s.AscentM != 412.5 {
				t.Errorf("upserted ascent = %v", s.AscentM)
			}
			list, _ := db.ListSessions(ctx, "wilant")
			if len(list) != 1 || list[0].AscentM != 412.5 {
				t.Errorf("listed = %+v", list)
			}
			// A re-pull in which the provider reports none must not erase what was
			// stored: 0 means "not reported", never "flat".
			if _, err := db.UpsertSession(ctx, UpsertSessionRequest{
				Rider: "wilant", Provider: "garmin", ExternalID: "1", Sport: "cycling", Date: "2026-05-01", DurationSeconds: 3600,
			}); err != nil {
				t.Fatal(err)
			}
			list, _ = db.ListSessions(ctx, "wilant")
			if list[0].AscentM != 412.5 {
				t.Errorf("a re-pull without ascent erased it: %v", list[0].AscentM)
			}
			if _, err := db.UpsertSession(ctx, UpsertSessionRequest{
				Rider: "wilant", Provider: "garmin", ExternalID: "1", Sport: "cycling", Date: "2026-05-01", DurationSeconds: 3600, AscentM: 430,
			}); err != nil {
				t.Fatal(err)
			}
			list, _ = db.ListSessions(ctx, "wilant")
			if list[0].AscentM != 430 {
				t.Errorf("a corrected ascent was not stored: %v", list[0].AscentM)
			}
		})
	}
}

func TestAnExistingDatabaseGainsTheAscentColumn(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	if _, err := src.Conn().Exec(`CREATE TABLE completed_sessions (
        id TEXT PRIMARY KEY, rider TEXT NOT NULL, provider TEXT NOT NULL, external_id TEXT NOT NULL,
        sport TEXT NOT NULL DEFAULT 'cycling', date TEXT NOT NULL,
        duration_seconds DOUBLE PRECISION NOT NULL DEFAULT 0, distance_m DOUBLE PRECISION NOT NULL DEFAULT 0,
        avg_hr INTEGER NOT NULL DEFAULT 0, avg_power_watts DOUBLE PRECISION NOT NULL DEFAULT 0,
        training_load DOUBLE PRECISION NOT NULL DEFAULT 0, created_at TEXT NOT NULL, UNIQUE (provider, external_id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Conn().Exec(`INSERT INTO completed_sessions (id, rider, provider, external_id, date, created_at)
        VALUES ('garmin:1', 'wilant', 'garmin', '1', '2026-01-01', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
			t.Fatalf("migration: %v", err)
		}
	}
	db, _ := UseDB(src.Conn(), src.DSN())
	list, err := db.ListSessions(t.Context(), "wilant")
	if err != nil || len(list) != 1 || list[0].AscentM != 0 {
		t.Errorf("after the migration = %+v, %v; want the old row with no ascent", list, err)
	}
}
