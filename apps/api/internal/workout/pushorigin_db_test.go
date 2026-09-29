package workout

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestPushOriginEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}
	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			mine, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Mine", Steps: exampleSteps()})
			if err != nil {
				t.Fatal(err)
			}
			auto, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Auto", Steps: exampleSteps()})
			if err != nil {
				t.Fatal(err)
			}
			theirs, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "other", Name: "Theirs", Steps: exampleSteps()})
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []Push{
				{WorkoutID: mine.ID, Provider: "garmin", RemoteID: "r1", ContentHash: "h", Origin: PushOriginManual},
				{WorkoutID: auto.ID, Provider: "garmin", RemoteID: "r2", ContentHash: "h", Origin: PushOriginAuto},
				{WorkoutID: theirs.ID, Provider: "garmin", RemoteID: "r3", ContentHash: "h", Origin: PushOriginAuto},
			} {
				if err := db.SavePush(ctx, p); err != nil {
					t.Fatal(err)
				}
			}

			got, err := db.ListPushes(ctx, "wilant", "garmin")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[mine.ID].Origin != PushOriginManual || got[auto.ID].Origin != PushOriginAuto {
				t.Errorf("pushes = %+v, want wilant's two with their origins and none of other's", got)
			}
			one, have, _ := db.GetPush(ctx, auto.ID, "garmin")
			if !have || one.Origin != PushOriginAuto {
				t.Errorf("GetPush = %+v, want origin auto", one)
			}

			// A save with no origin (older code) reads back as manual, the
			// protected value: it must never be withdrawn by accident.
			if err := db.SavePush(ctx, Push{WorkoutID: mine.ID, Provider: "garmin", RemoteID: "r1", ContentHash: "h2"}); err != nil {
				t.Fatal(err)
			}
			if one, _, _ := db.GetPush(ctx, mine.ID, "garmin"); one.Origin != PushOriginManual {
				t.Errorf("origin of an unstamped save = %q, want manual", one.Origin)
			}
		})
	}
}

// Pushes from before the column existed are sorted once, when it appears: a
// plan-made workout of a rider with auto-push on was made by the automatic
// pass; anything else was the rider's own button.
func TestPushOriginColumnBackfillsOnlyOnce(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	db, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "opted", AutoPushWorkouts: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "manualonly"}); err != nil {
		t.Fatal(err)
	}
	mk := func(rider, name, goal string) string {
		w, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: rider, Name: name, GoalID: goal, Steps: exampleSteps()})
		if err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	planMade := mk("opted", "Plan made", "goal")
	handBuilt := mk("opted", "Hand built", "")
	notOptedIn := mk("manualonly", "Plan made elsewhere", "goal")

	// Put the table back the way it was before the column existed.
	if _, err := src.Conn().Exec(`DROP TABLE workout_pushes`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Conn().Exec(`CREATE TABLE workout_pushes (
        workout_id TEXT NOT NULL, provider TEXT NOT NULL, remote_id TEXT NOT NULL,
        schedule_id TEXT NOT NULL DEFAULT '', scheduled_date TEXT NOT NULL DEFAULT '',
        content_hash TEXT NOT NULL, pushed_at TEXT NOT NULL, PRIMARY KEY (workout_id, provider))`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{planMade, handBuilt, notOptedIn} {
		if _, err := src.Conn().Exec(`INSERT INTO workout_pushes VALUES (?, 'garmin', 'r-' || ?, '', '', 'h', '2026-01-01T00:00:00Z')`, id, id); err != nil {
			t.Fatal(err)
		}
	}

	reopened, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{planMade: PushOriginAuto, handBuilt: PushOriginManual, notOptedIn: PushOriginManual} {
		if p, _, _ := reopened.GetPush(ctx, id, "garmin"); p.Origin != want {
			t.Errorf("origin of %s = %q, want %q", id, p.Origin, want)
		}
	}

	// A rider's later manual push of a plan-made workout must survive a restart.
	if err := reopened.SavePush(ctx, Push{WorkoutID: planMade, Provider: "garmin", RemoteID: "r", ContentHash: "h", Origin: PushOriginManual}); err != nil {
		t.Fatal(err)
	}
	again, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if p, _, _ := again.GetPush(ctx, planMade, "garmin"); p.Origin != PushOriginManual {
		t.Errorf("restart re-stamped a manual push as %q", p.Origin)
	}
}
