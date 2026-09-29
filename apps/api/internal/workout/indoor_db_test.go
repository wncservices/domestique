package workout

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

func nestedSteps() []WorkoutStep {
	return []WorkoutStep{
		{Name: "Warmup", Intensity: IntensityWarmup, Duration: DurationOpen},
		{Name: "Outer", Repeat: 2, Steps: []WorkoutStep{
			{Name: "Inner", Repeat: 3, Steps: []WorkoutStep{
				{Name: "On", Intensity: IntensityInterval, Duration: DurationDistance, Meters: 1000, Target: TargetPower, TargetLow: 250, TargetHigh: 260},
			}},
			{Name: "Off", Intensity: IntensityRecovery, Duration: DurationTime, Seconds: 60},
		}},
	}
}

func TestIndoorColumnsRoundTripOnEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			w, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Sport: model.SportCycling, Name: "Long ride", Steps: nestedSteps()})
			if err != nil {
				t.Fatal(err)
			}
			if w.Indoor || w.OutdoorSteps != nil {
				t.Fatalf("a new workout is indoor=%v outdoor=%v, want false/nil", w.Indoor, w.OutdoorSteps)
			}

			indoor := true
			original := nestedSteps()
			indoorSteps := []WorkoutStep{{Name: "Ride", Intensity: IntensityActive, Duration: DurationTime, Seconds: 600}}
			got, err := db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Indoor: &indoor, OutdoorSteps: &original, Steps: &indoorSteps})
			if err != nil {
				t.Fatal(err)
			}
			if !got.Indoor || got.OutdoorSteps == nil || !reflect.DeepEqual(*got.OutdoorSteps, nestedSteps()) {
				t.Fatalf("after indoor update: indoor=%v outdoor=%+v", got.Indoor, got.OutdoorSteps)
			}
			listed, err := db.ListWorkouts(ctx, "wilant")
			if err != nil || len(listed) != 1 || !listed[0].Indoor || listed[0].OutdoorSteps == nil ||
				(*listed[0].OutdoorSteps)[1].Steps[0].Repeat != 3 {
				t.Fatalf("listed = %+v err %v", listed, err)
			}

			// An unrelated edit leaves both alone.
			name := "Renamed"
			got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Name: &name})
			if err != nil {
				t.Fatal(err)
			}
			if !got.Indoor || got.OutdoorSteps == nil {
				t.Errorf("a rename wiped indoor state: %+v", got)
			}

			// A pointer to a nil slice clears back to NULL.
			off := false
			var none []WorkoutStep
			got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Indoor: &off, OutdoorSteps: &none})
			if err != nil {
				t.Fatal(err)
			}
			if got.Indoor || got.OutdoorSteps != nil {
				t.Errorf("cleared workout is indoor=%v outdoor=%+v, want false/nil", got.Indoor, got.OutdoorSteps)
			}

			// A stored original with no steps stays distinct from nothing stored.
			empty := []WorkoutStep{}
			got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{OutdoorSteps: &empty})
			if err != nil {
				t.Fatal(err)
			}
			if got.OutdoorSteps == nil || len(*got.OutdoorSteps) != 0 {
				t.Errorf("empty original = %+v, want a non-nil empty slice", got.OutdoorSteps)
			}
		})
	}
}

func TestSmartTrainerRoundTripsOnEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			p, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 250})
			if err != nil {
				t.Fatal(err)
			}
			if p.SmartTrainer {
				t.Errorf("smart trainer defaults to false")
			}
			p, err = db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 250, SmartTrainer: true})
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := db.GetProfile(ctx, "wilant")
			if err != nil || !got.SmartTrainer || !p.SmartTrainer {
				t.Errorf("saved smart trainer = %v (%v), err %v", got.SmartTrainer, p.SmartTrainer, err)
			}
		})
	}
}

// TestWorkoutsAndProfilesGainIndoorColumns migrates a database created before
// the indoor columns existed, twice.
func TestWorkoutsAndProfilesGainIndoorColumns(t *testing.T) {
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
	if _, err := src.Conn().Exec(`
CREATE TABLE rider_profiles (
    rider                      TEXT PRIMARY KEY,
    ftp_watts                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    ftp_estimated              BOOLEAN NOT NULL DEFAULT FALSE,
    estimated_fields           TEXT NOT NULL DEFAULT '',
    auto_push_workouts         BOOLEAN NOT NULL DEFAULT FALSE,
    threshold_pace_sec_per_km  DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_hr                     INTEGER NOT NULL DEFAULT 0,
    resting_hr                 INTEGER NOT NULL DEFAULT 0,
    available_days             TEXT NOT NULL DEFAULT '',
    hours_per_available_day    DOUBLE PRECISION NOT NULL DEFAULT 0,
    experience_level           TEXT NOT NULL DEFAULT '',
    updated_at                 TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Conn().Exec(`INSERT INTO rider_profiles (rider, ftp_watts, updated_at) VALUES ('wilant', 250, '2026-01-01T00:00:00Z')`); err != nil {
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
	if old.Indoor || old.OutdoorSteps != nil {
		t.Errorf("pre-existing row = indoor %v outdoor %v", old.Indoor, old.OutdoorSteps)
	}
	p, ok, err := db.GetProfile(t.Context(), "wilant")
	if err != nil || !ok || p.SmartTrainer {
		t.Errorf("pre-existing profile = %+v ok=%v err=%v", p, ok, err)
	}
	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Errorf("second UseDB: %v", err)
	}
}
