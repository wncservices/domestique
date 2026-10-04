package workout

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestCrewRideIDRoundTripsOnEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			plain, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Easy Spin"})
			if err != nil {
				t.Fatal(err)
			}
			if plain.CrewRideID != "" {
				t.Errorf("an ordinary workout carries crew ride %q", plain.CrewRideID)
			}

			crew, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Crew ride: Hill Loop", CrewRideID: "ride-1"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := db.GetWorkout(ctx, crew.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.CrewRideID != "ride-1" {
				t.Errorf("crew ride id = %q, want ride-1", got.CrewRideID)
			}

			// An edit must not wipe what makes a workout a crew ride.
			name := "Renamed"
			if _, err := db.UpdateWorkout(ctx, crew.ID, UpdateWorkoutRequest{Name: &name}); err != nil {
				t.Fatal(err)
			}
			got, _ = db.GetWorkout(ctx, crew.ID)
			if got.CrewRideID != "ride-1" {
				t.Errorf("crew ride id after an update = %q, want ride-1", got.CrewRideID)
			}

			list, err := db.ListWorkouts(ctx, "wilant")
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, w := range list {
				if w.ID == crew.ID && w.CrewRideID == "ride-1" {
					found = true
				}
			}
			if !found {
				t.Errorf("ListWorkouts lost the crew ride id: %+v", list)
			}
		})
	}
}

func TestCrewRideIsUniquePerRiderAndRide(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			if _, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Crew ride: A", CrewRideID: "ride-1"}); err != nil {
				t.Fatal(err)
			}
			_, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Crew ride: B", CrewRideID: "ride-1"})
			if !errors.Is(err, ErrCrewRideExists) {
				t.Errorf("a second row for the same rider and ride: err = %v, want ErrCrewRideExists", err)
			}
			// Another rider may hold the same ride; so may the same rider hold another.
			if _, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "sam", Name: "Crew ride: A", CrewRideID: "ride-1"}); err != nil {
				t.Errorf("another rider on the same ride: %v", err)
			}
			if _, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Crew ride: C", CrewRideID: "ride-2"}); err != nil {
				t.Errorf("the same rider on another ride: %v", err)
			}
			// Ordinary workouts have no crew ride and are never constrained.
			for range 2 {
				if _, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Easy Spin"}); err != nil {
					t.Errorf("ordinary workouts collide: %v", err)
				}
			}
		})
	}
}

func TestCrewRideColumnMigrationIsIdempotent(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "crewride.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	for range 2 {
		if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
			t.Fatalf("UseDB: %v", err)
		}
	}
	var n int
	if err := src.Conn().QueryRow(`SELECT COUNT(*) FROM workouts WHERE crew_ride_id = ''`).Scan(&n); err != nil {
		t.Fatalf("the column is not there after UseDB: %v", err)
	}
}
