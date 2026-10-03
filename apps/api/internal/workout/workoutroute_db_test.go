package workout

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestWorkoutRouteLinkRoundTripsOnEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			w, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Sport: model.SportCycling, Name: "Endurance", Steps: exampleSteps()})
			if err != nil {
				t.Fatal(err)
			}
			if w.RouteSlug != "" || w.RouteSeconds != 0 {
				t.Fatalf("a new workout links %q / %v, want nothing", w.RouteSlug, w.RouteSeconds)
			}

			slug, secs := "endurance-loop-62-km", 7260.0
			got, err := db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs})
			if err != nil {
				t.Fatal(err)
			}
			if got.RouteSlug != slug || got.RouteSeconds != secs {
				t.Fatalf("after linking: %q / %v", got.RouteSlug, got.RouteSeconds)
			}
			listed, err := db.ListWorkouts(ctx, "wilant")
			if err != nil || len(listed) != 1 || listed[0].RouteSlug != slug || listed[0].RouteSeconds != secs {
				t.Fatalf("listed = %+v (err %v), want the link kept", listed, err)
			}

			// An update that does not mention the route keeps it.
			desc := "edited"
			got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Description: &desc})
			if err != nil || got.RouteSlug != slug || got.RouteSeconds != secs {
				t.Fatalf("an unrelated update changed the link: %q / %v (err %v)", got.RouteSlug, got.RouteSeconds, err)
			}

			// Empty clears both.
			empty, zero := "", 0.0
			got, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{RouteSlug: &empty, RouteSeconds: &zero})
			if err != nil || got.RouteSlug != "" || got.RouteSeconds != 0 {
				t.Fatalf("after clearing: %q / %v (err %v)", got.RouteSlug, got.RouteSeconds, err)
			}
		})
	}
}

func TestUnlinkRouteClearsEveryWorkoutThatLinksIt(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			mk := func(rider, name, slug string) Workout {
				w, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: rider, Sport: model.SportCycling, Name: name, Steps: exampleSteps()})
				if err != nil {
					t.Fatal(err)
				}
				secs := 3600.0
				if slug != "" {
					if w, err = db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs}); err != nil {
						t.Fatal(err)
					}
				}
				return w
			}
			a := mk("wilant", "A", "shared-loop")
			b := mk("marie", "B", "shared-loop")
			c := mk("wilant", "C", "another-loop")
			d := mk("wilant", "D", "")

			n, err := db.UnlinkRoute(ctx, "shared-loop")
			if err != nil || n != 2 {
				t.Fatalf("UnlinkRoute = %d, %v, want 2 workouts across riders", n, err)
			}
			for id, want := range map[string]string{a.ID: "", b.ID: "", c.ID: "another-loop", d.ID: ""} {
				got, _ := db.GetWorkout(ctx, id)
				if got.RouteSlug != want {
					t.Errorf("workout %s links %q, want %q", id, got.RouteSlug, want)
				}
			}
			if got, _ := db.GetWorkout(ctx, a.ID); got.RouteSeconds != 0 {
				t.Errorf("an unlinked workout still carries %v route seconds", got.RouteSeconds)
			}

			// An empty slug matches every unrouted workout; it must match none.
			if n, err := db.UnlinkRoute(ctx, ""); err != nil || n != 0 {
				t.Errorf("UnlinkRoute(\"\") = %d, %v, want a no-op", n, err)
			}
			if got, _ := db.GetWorkout(ctx, c.ID); got.RouteSlug != "another-loop" {
				t.Errorf("an empty slug unlinked someone else's route: %q", got.RouteSlug)
			}
		})
	}
}

func TestWorkoutsGainTheRouteLinkColumns(t *testing.T) {
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
	if old.RouteSlug != "" || old.RouteSeconds != 0 {
		t.Errorf("pre-existing row links %q / %v, want nothing", old.RouteSlug, old.RouteSeconds)
	}
	slug, secs := "a-loop", 1800.0
	if _, err := db.UpdateWorkout(t.Context(), "old", UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs}); err != nil {
		t.Errorf("linking a migrated row: %v", err)
	}
	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Errorf("second UseDB: %v", err)
	}
}
