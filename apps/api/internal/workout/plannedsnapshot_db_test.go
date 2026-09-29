package workout

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestPlannedSnapshotRoundTripsOnEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			w, err := db.CreateWorkout(ctx, CreateWorkoutRequest{
				Rider: "wilant", Sport: model.SportCycling, Name: "Threshold 3x12", Description: "Generated.",
				Zone: ZoneThreshold, Level: 5, Steps: nestedSteps(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if w.PlannedSnapshot != nil {
				t.Fatalf("a new workout has snapshot %+v, want none", w.PlannedSnapshot)
			}

			outdoor := nestedSteps()
			first := &PlannedSnapshot{
				Sport: w.Sport, Name: w.Name, Zone: w.Zone, Level: w.Level, Description: w.Description, Steps: w.Steps,
				Indoor: true, OutdoorSteps: &outdoor,
			}
			newName, newZone, newLevel := "Threshold 4x12", ZoneThreshold, 6.0
			got, err := db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{
				Name: &newName, Zone: &newZone, Level: &newLevel, PlannedSnapshot: first,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.PlannedSnapshot == nil || !reflect.DeepEqual(*got.PlannedSnapshot, *first) {
				t.Fatalf("snapshot after the first swap = %+v, want %+v", got.PlannedSnapshot, first)
			}
			listed, err := db.ListWorkouts(ctx, "wilant")
			if err != nil || len(listed) != 1 || listed[0].PlannedSnapshot == nil ||
				(*listed[0].PlannedSnapshot.OutdoorSteps)[1].Steps[0].Repeat != 3 {
				t.Fatalf("listed snapshot = %+v (err %v), want the nested outdoor steps intact", listed, err)
			}

			// A second swap passes its own snapshot; the first is kept.
			second := &PlannedSnapshot{Name: "Threshold 4x12", Zone: ZoneThreshold, Level: 6, Description: "swapped", Steps: nil}
			other := "Threshold 5x12"
			got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Name: &other, PlannedSnapshot: second})
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != other || got.PlannedSnapshot == nil || !reflect.DeepEqual(*got.PlannedSnapshot, *first) {
				t.Fatalf("after a second swap snapshot = %+v, want the first kept", got.PlannedSnapshot)
			}

			// An ordinary update leaves it alone.
			desc := "edited"
			got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Description: &desc})
			if err != nil || got.PlannedSnapshot == nil {
				t.Fatalf("an unrelated update dropped the snapshot: %+v (err %v)", got.PlannedSnapshot, err)
			}

			got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{ClearPlannedSnapshot: true})
			if err != nil {
				t.Fatal(err)
			}
			if got.PlannedSnapshot != nil {
				t.Errorf("cleared snapshot = %+v, want none", got.PlannedSnapshot)
			}
		})
	}
}

func TestWorkoutsGainThePlannedSnapshotColumn(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	if _, err := src.Conn().Exec(`
CREATE TABLE workouts (
    id TEXT PRIMARY KEY, rider TEXT NOT NULL, sport TEXT NOT NULL DEFAULT 'cycling', name TEXT NOT NULL,
    goal_id TEXT NOT NULL DEFAULT '', date TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
    steps BLOB NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Conn().Exec(`INSERT INTO workouts (id, rider, name, steps, created_at, updated_at)
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
	if old.PlannedSnapshot != nil {
		t.Errorf("pre-existing row has snapshot %+v", old.PlannedSnapshot)
	}
	name := "Swapped"
	if _, err := db.UpdateWorkout(t.Context(), "old", UpdateWorkoutRequest{Name: &name, PlannedSnapshot: &PlannedSnapshot{Name: "Old"}}); err != nil {
		t.Errorf("writing a snapshot to a migrated row: %v", err)
	}
	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Errorf("second UseDB: %v", err)
	}
}
