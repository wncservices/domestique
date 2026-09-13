package workout

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// openStore mirrors internal/crew's own test helper exactly: UseDB shares a
// connection rather than opening one, so getting one to share, even in a
// unit test, goes through source.OpenDB the same way runServe does.
func openStore(t *testing.T, dsn string) *DB {
	t.Helper()

	src, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { src.Close() })

	db, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	// Postgres tests share a database; start clean.
	for _, table := range []string{"goals", "rider_profiles", "workouts"} {
		if _, err := src.Conn().Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	return openStore(t, filepath.Join(t.TempDir(), "test.db"))
}

// postgresEnv mirrors internal/source's own copy — see that package's
// db_test.go for why this needs its own real PostgreSQL, not just SQLite.
const postgresEnv = "DOMESTIQUE_TEST_POSTGRES"

func openTestPostgres(t *testing.T) *DB {
	t.Helper()

	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return openStore(t, dsn)
}

func exampleSteps() []WorkoutStep {
	return []WorkoutStep{
		{Name: "Warmup", Intensity: IntensityWarmup, Duration: DurationTime, Seconds: 600, Target: TargetOpen},
		{
			Name: "Intervals", Repeat: 6,
			Steps: []WorkoutStep{
				{Name: "On", Intensity: IntensityInterval, Duration: DurationTime, Seconds: 180,
					Target: TargetPower, TargetLow: 280, TargetHigh: 300},
				{Name: "Off", Intensity: IntensityRecovery, Duration: DurationTime, Seconds: 120,
					Target: TargetPower, TargetLow: 100, TargetHigh: 150},
			},
		},
		{Name: "Cooldown", Intensity: IntensityCooldown, Duration: DurationTime, Seconds: 300, Target: TargetOpen},
	}
}

func TestEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("goal create, read, update, delete", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				goal, err := db.CreateGoal(ctx, CreateGoalRequest{
					Rider: "Wilant", Name: "Local Gran Fondo", Sport: model.SportCycling,
					EventDate: "2026-06-01", Priority: PriorityA,
					TargetDistanceM: 180_000, TargetElevationM: 2400,
				})
				if err != nil {
					t.Fatalf("create goal: %v", err)
				}
				if goal.ID != "local-gran-fondo" {
					t.Errorf("id = %q", goal.ID)
				}
				if goal.Rider != "wilant" {
					t.Errorf("rider = %q, want normalized wilant", goal.Rider)
				}
				if goal.Priority != PriorityA {
					t.Errorf("priority = %q", goal.Priority)
				}

				fetched, err := db.GetGoal(ctx, goal.ID)
				if err != nil {
					t.Fatalf("get goal: %v", err)
				}
				if fetched != goal {
					t.Errorf("get returned %+v, want %+v", fetched, goal)
				}

				list, err := db.ListGoals(ctx, "WILANT")
				if err != nil {
					t.Fatalf("list goals: %v", err)
				}
				if len(list) != 1 || list[0].ID != goal.ID {
					t.Errorf("list = %+v", list)
				}
				if empty, err := db.ListGoals(ctx, "someone-else"); err != nil || len(empty) != 0 {
					t.Errorf("another rider's list = %+v, err %v", empty, err)
				}

				newName := "Renamed Fondo"
				updated, err := db.UpdateGoal(ctx, goal.ID, UpdateGoalRequest{Name: &newName})
				if err != nil {
					t.Fatalf("update goal: %v", err)
				}
				if updated.Name != newName {
					t.Errorf("name = %q", updated.Name)
				}
				// The id does not change on rename — matches route slugs,
				// which are also stable once assigned.
				if updated.ID != goal.ID {
					t.Errorf("id changed to %q on rename", updated.ID)
				}

				if err := db.DeleteGoal(ctx, goal.ID); err != nil {
					t.Fatalf("delete goal: %v", err)
				}
				if _, err := db.GetGoal(ctx, goal.ID); err != ErrGoalNotFound {
					t.Errorf("get after delete: %v", err)
				}
				if err := db.DeleteGoal(ctx, goal.ID); err != ErrGoalNotFound {
					t.Errorf("delete again: %v", err)
				}
			})

			t.Run("two goals with the same name get distinct ids", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				a, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Race Day"})
				if err != nil {
					t.Fatalf("create a: %v", err)
				}
				b, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Race Day"})
				if err != nil {
					t.Fatalf("create b: %v", err)
				}
				if a.ID == b.ID {
					t.Errorf("both got id %q", a.ID)
				}
			})

			t.Run("rider profile is get-or-empty then upsert", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				_, ok, err := db.GetProfile(ctx, "wilant")
				if err != nil {
					t.Fatalf("get profile: %v", err)
				}
				if ok {
					t.Errorf("expected no profile yet")
				}

				saved, err := db.SaveProfile(ctx, RiderProfile{
					Rider: "Wilant", FTPWatts: 280, MaxHR: 185, RestingHR: 48,
					AvailableDays: []string{"tue", "thu", "sat", "sun"}, HoursPerAvailableDay: 1.5,
					ExperienceLevel: "intermediate",
				})
				if err != nil {
					t.Fatalf("save profile: %v", err)
				}
				if saved.Rider != "wilant" || saved.FTPWatts != 280 {
					t.Errorf("saved = %+v", saved)
				}
				if len(saved.AvailableDays) != 4 {
					t.Errorf("available days = %v", saved.AvailableDays)
				}

				// Saving again edits the same row rather than creating a second one.
				updated, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 295})
				if err != nil {
					t.Fatalf("re-save profile: %v", err)
				}
				if updated.FTPWatts != 295 {
					t.Errorf("ftp = %v, want 295", updated.FTPWatts)
				}

				fetched, ok, err := db.GetProfile(ctx, "WILANT")
				if err != nil || !ok {
					t.Fatalf("get profile: ok=%v err=%v", ok, err)
				}
				if fetched.FTPWatts != 295 {
					t.Errorf("fetched ftp = %v", fetched.FTPWatts)
				}
			})

			t.Run("ftp_estimated defaults false and round-trips", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				saved, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 250})
				if err != nil {
					t.Fatalf("save profile: %v", err)
				}
				if saved.FTPEstimated {
					t.Error("expected FTPEstimated=false by default")
				}

				estimated, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 260, FTPEstimated: true})
				if err != nil {
					t.Fatalf("save profile: %v", err)
				}
				if !estimated.FTPEstimated {
					t.Error("expected FTPEstimated=true to round-trip")
				}

				fetched, _, err := db.GetProfile(ctx, "wilant")
				if err != nil {
					t.Fatalf("get profile: %v", err)
				}
				if !fetched.FTPEstimated || fetched.FTPWatts != 260 {
					t.Errorf("fetched = %+v, want FTPWatts=260 FTPEstimated=true", fetched)
				}
			})

			t.Run("workout create, read, update, delete with nested steps", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				w, err := db.CreateWorkout(ctx, CreateWorkoutRequest{
					Rider: "wilant", Sport: model.SportCycling, Name: "Threshold 6x3",
					Date: "2026-03-02", Steps: exampleSteps(),
				})
				if err != nil {
					t.Fatalf("create workout: %v", err)
				}
				if len(w.Steps) != 3 {
					t.Fatalf("steps = %+v", w.Steps)
				}
				if w.Steps[1].Repeat != 6 || len(w.Steps[1].Steps) != 2 {
					t.Fatalf("repeat block = %+v", w.Steps[1])
				}
				if w.Steps[1].Steps[0].TargetLow != 280 {
					t.Errorf("nested target = %+v", w.Steps[1].Steps[0])
				}

				fetched, err := db.GetWorkout(ctx, w.ID)
				if err != nil {
					t.Fatalf("get workout: %v", err)
				}
				if len(fetched.Steps) != 3 || fetched.Steps[1].Steps[1].Name != "Off" {
					t.Errorf("round trip steps = %+v", fetched.Steps)
				}

				list, err := db.ListWorkouts(ctx, "wilant")
				if err != nil || len(list) != 1 {
					t.Fatalf("list = %+v, err %v", list, err)
				}

				newDate := "2026-03-09"
				updated, err := db.UpdateWorkout(ctx, w.ID, UpdateWorkoutRequest{Date: &newDate})
				if err != nil {
					t.Fatalf("update workout: %v", err)
				}
				if updated.Date != newDate {
					t.Errorf("date = %q", updated.Date)
				}
				if len(updated.Steps) != 3 {
					t.Errorf("steps not preserved by a date-only update: %+v", updated.Steps)
				}

				if err := db.DeleteWorkout(ctx, w.ID); err != nil {
					t.Fatalf("delete workout: %v", err)
				}
				if _, err := db.GetWorkout(ctx, w.ID); err != ErrWorkoutNotFound {
					t.Errorf("get after delete: %v", err)
				}
			})

			t.Run("deleting a goal unlinks its workouts rather than deleting them", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				goal, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Target Race"})
				if err != nil {
					t.Fatalf("create goal: %v", err)
				}
				w, err := db.CreateWorkout(ctx, CreateWorkoutRequest{
					Rider: "wilant", Name: "Long Ride", GoalID: goal.ID, Steps: exampleSteps(),
				})
				if err != nil {
					t.Fatalf("create workout: %v", err)
				}

				if err := db.DeleteGoal(ctx, goal.ID); err != nil {
					t.Fatalf("delete goal: %v", err)
				}

				fetched, err := db.GetWorkout(ctx, w.ID)
				if err != nil {
					t.Fatalf("get workout after goal delete: %v", err)
				}
				if fetched.GoalID != "" {
					t.Errorf("goal id = %q, want unlinked", fetched.GoalID)
				}
			})

			t.Run("a repeat block with no steps is rejected", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				_, err := db.CreateWorkout(ctx, CreateWorkoutRequest{
					Rider: "wilant", Name: "Bad Workout",
					Steps: []WorkoutStep{{Name: "Intervals", Repeat: 4}},
				})
				if err == nil {
					t.Fatalf("expected an error for an empty repeat block")
				}
			})
		})
	}
}

func TestValidateStepDepth(t *testing.T) {
	nest := func(depth int) []WorkoutStep {
		steps := []WorkoutStep{{Name: "leaf", Duration: DurationOpen, Target: TargetOpen}}
		for i := 0; i < depth; i++ {
			steps = []WorkoutStep{{Name: "wrap", Repeat: 2, Steps: steps}}
		}
		return steps
	}

	if err := validateSteps(nest(maxStepDepth - 1)); err != nil {
		t.Errorf("depth %d should be allowed: %v", maxStepDepth-1, err)
	}
	if err := validateSteps(nest(maxStepDepth + 1)); err == nil {
		t.Errorf("depth %d should be rejected", maxStepDepth+1)
	}
}
