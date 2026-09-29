package workout

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestFTPTestWorkoutColumnsRoundTripOnEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			plain, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Easy Spin"})
			if err != nil {
				t.Fatal(err)
			}
			if plain.TestProtocol != "" || plain.TestResultWatts != 0 {
				t.Errorf("an ordinary workout carries protocol %q result %v", plain.TestProtocol, plain.TestResultWatts)
			}

			test, err := db.CreateWorkout(ctx, CreateWorkoutRequest{
				Rider: "wilant", Name: "FTP Test (ramp)", TestProtocol: "ramp",
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := db.GetWorkout(ctx, test.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.TestProtocol != "ramp" || got.TestResultWatts != 0 {
				t.Errorf("got protocol %q result %v, want ramp/0", got.TestProtocol, got.TestResultWatts)
			}

			// An edit must not wipe what marks a workout as a test.
			newName := "Renamed"
			if _, err := db.UpdateWorkout(ctx, test.ID, UpdateWorkoutRequest{Name: &newName}); err != nil {
				t.Fatal(err)
			}
			got, _ = db.GetWorkout(ctx, test.ID)
			if got.TestProtocol != "ramp" {
				t.Errorf("protocol after an update = %q, want ramp", got.TestProtocol)
			}

			list, err := db.ListWorkouts(ctx, "wilant")
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, w := range list {
				if w.ID == test.ID && w.TestProtocol == "ramp" {
					found = true
				}
			}
			if !found {
				t.Errorf("ListWorkouts lost the protocol: %+v", list)
			}
		})
	}
}

func TestWorkoutsTableGainsTestColumns(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	if _, err := src.Conn().Exec(`
CREATE TABLE workouts (
    id TEXT PRIMARY KEY, rider TEXT NOT NULL, sport TEXT NOT NULL DEFAULT 'cycling',
    name TEXT NOT NULL, goal_id TEXT NOT NULL DEFAULT '', date TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '', steps BLOB NOT NULL,
    zone TEXT NOT NULL DEFAULT '', level DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL, updated_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Conn().Exec(`
INSERT INTO workouts (id, rider, name, steps, created_at, updated_at)
VALUES ('old', 'wilant', 'Old', '[]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	db, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("UseDB (migrate): %v", err)
	}
	old, err := db.GetWorkout(t.Context(), "old")
	if err != nil {
		t.Fatal(err)
	}
	if old.TestProtocol != "" || old.TestResultWatts != 0 {
		t.Errorf("legacy row = %q/%v, want empty/0", old.TestProtocol, old.TestResultWatts)
	}
	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Errorf("second UseDB: %v", err)
	}
}
