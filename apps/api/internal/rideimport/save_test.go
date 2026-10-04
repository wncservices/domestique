package rideimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const postgresEnv = "DOMESTIQUE_TEST_POSTGRES"

// engines opens the same store on SQLite and on PostgreSQL, so a query that
// only one of them understands cannot pass. Each test works under its own
// rider name and cleans up after itself rather than emptying the shared
// PostgreSQL database other packages test against.
func eachEngine(t *testing.T, run func(t *testing.T, db *workout.DB, rider string)) {
	t.Helper()
	open := map[string]func(*testing.T) string{
		"sqlite": func(t *testing.T) string { return filepath.Join(t.TempDir(), "test.db") },
		"postgres": func(t *testing.T) string {
			dsn := os.Getenv(postgresEnv)
			if dsn == "" {
				t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
			}
			return dsn
		},
	}
	for engine, dsn := range open {
		t.Run(engine, func(t *testing.T) {
			src, err := source.OpenDB(dsn(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { src.Close() })
			db, err := workout.UseDB(src.Conn(), src.DSN())
			if err != nil {
				t.Fatal(err)
			}
			rider := "imp-" + engine + "-" + strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()[:min(len(t.Name()), 40)]))
			cleanup := func() {
				for _, q := range []string{
					`DELETE FROM session_analyses WHERE rider LIKE 'imp-%'`,
					`DELETE FROM completed_sessions WHERE rider LIKE 'imp-%'`,
					`DELETE FROM progression_levels WHERE rider LIKE 'imp-%'`,
					`DELETE FROM workouts WHERE rider LIKE 'imp-%'`,
					`DELETE FROM rider_profiles WHERE rider LIKE 'imp-%'`,
				} {
					if _, err := src.Conn().Exec(q); err != nil {
						t.Fatal(err)
					}
				}
			}
			cleanup()
			t.Cleanup(cleanup)
			run(t, db, rider)
		})
	}
}

func parsed(t *testing.T, spec rideSpec) Ride {
	t.Helper()
	r, err := Parse(spec.build(t), profile)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSaveWritesTheSessionAndItsAnalysis(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *workout.DB, rider string) {
		ctx := t.Context()
		// 300 days before the fixed "today" of 2026-10-04: far outside the 42-day
		// window sync analyses, and the whole point of importing.
		old := parsed(t, rideSpec{start: at(2025, 12, 8, 9, 0), seconds: 2400, watts: 210, hr: 145, distanceM: 20000})

		out, err := Save(ctx, db, rider, old)
		if err != nil || out != Added {
			t.Fatalf("Save = %v, %v, want Added", out, err)
		}

		sessions, _ := db.ListSessions(ctx, rider)
		if len(sessions) != 1 {
			t.Fatalf("sessions = %+v", sessions)
		}
		s := sessions[0]
		if s.Provider != "import" || s.Sport != "cycling" || s.Date != "2025-12-08" || s.DurationSeconds != 2400 ||
			s.AvgPowerWatts != 210 || s.AvgHR != 145 || s.DistanceM != 20000 {
			t.Errorf("session = %+v", s)
		}
		if s.ExternalID != ExternalID(rider, old.StartUnix) {
			t.Errorf("external id = %q, want %q", s.ExternalID, ExternalID(rider, old.StartUnix))
		}
		if s.TrainingLoad <= 0 || s.TrainingLoad != old.Analysis.Load {
			t.Errorf("training load = %v, want the analysis's %v: the same final load a synced ride ends with", s.TrainingLoad, old.Analysis.Load)
		}

		analyses, err := db.ListAnalyses(ctx, rider, "2000-01-01")
		if err != nil || len(analyses) != 1 {
			t.Fatalf("analyses = %+v, %v: a ride 300 days old still gets one, detection's 90-day rule reads older curves", analyses, err)
		}
		a := analyses[0]
		if a.SessionID != s.ID || a.WorkoutID != "" || a.Outcome != "unplanned" || a.NormalizedPower < 209 || len(a.PowerCurve) == 0 || a.MaxHR != 145 {
			t.Errorf("analysis = %+v", a)
		}
	})
}

func TestSaveTwiceIsANoOp(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *workout.DB, rider string) {
		ctx := t.Context()
		r := parsed(t, rideSpec{start: at(2026, 2, 1, 9, 0), seconds: 3000, watts: 200})

		if out, err := Save(ctx, db, rider, r); err != nil || out != Added {
			t.Fatalf("first = %v, %v", out, err)
		}
		if out, err := Save(ctx, db, rider, r); err != nil || out != AlreadyHere {
			t.Fatalf("second = %v, %v, want AlreadyHere", out, err)
		}
		if sessions, _ := db.ListSessions(ctx, rider); len(sessions) != 1 {
			t.Errorf("%d sessions after a re-import, want 1", len(sessions))
		}
		if analyses, _ := db.ListAnalyses(ctx, rider, "2000-01-01"); len(analyses) != 1 {
			t.Errorf("%d analyses after a re-import, want 1", len(analyses))
		}
	})
}

func TestSaveSkipsARideAlreadySyncedFromAProviderAndLeavesItsRowAlone(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *workout.DB, rider string) {
		ctx := t.Context()
		// The provider knows this ride by its own id, to the day, with slightly
		// different numbers (it rounds, and keeps its own duration).
		synced, err := db.UpsertSession(ctx, workout.UpsertSessionRequest{
			Rider: rider, Provider: "garmin", ExternalID: "9001", Sport: "cycling", Date: "2026-02-01",
			DurationSeconds: 3010, AvgPowerWatts: 202, AvgHR: 140, TrainingLoad: 55,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := parsed(t, rideSpec{start: at(2026, 2, 1, 9, 0), seconds: 3000, watts: 200})

		out, err := Save(ctx, db, rider, r)
		if err != nil || out != AlreadyHere {
			t.Fatalf("Save = %v, %v, want AlreadyHere", out, err)
		}
		sessions, _ := db.ListSessions(ctx, rider)
		if len(sessions) != 1 || sessions[0] != synced {
			t.Errorf("sessions = %+v: the provider's row (better summary numbers) must be untouched and nothing added", sessions)
		}
		if analyses, _ := db.ListAnalyses(ctx, rider, "2000-01-01"); len(analyses) != 0 {
			t.Errorf("a skipped ride must not leave an analysis behind: %+v", analyses)
		}
	})
}

func TestSaveFindsTheProviderRideOnTheNeighbouringDay(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *workout.DB, rider string) {
		ctx := t.Context()
		// A ride just after local midnight: the file says one day, a provider
		// that stores the UTC start says the day before.
		if _, err := db.UpsertSession(ctx, workout.UpsertSessionRequest{
			Rider: rider, Provider: "wahoo", ExternalID: "7", Sport: "cycling", Date: "2026-07-01",
			DurationSeconds: 3000, AvgPowerWatts: 200,
		}); err != nil {
			t.Fatal(err)
		}
		r := parsed(t, rideSpec{start: at(2026, 7, 1, 22, 30), seconds: 3000, watts: 200, withActivity: true, offset: 2 * time.Hour})
		if r.Date != "2026-07-02" {
			t.Fatalf("fixture date = %s", r.Date)
		}
		if out, err := Save(ctx, db, rider, r); err != nil || out != AlreadyHere {
			t.Errorf("Save = %v, %v, want AlreadyHere across the day boundary", out, err)
		}
	})
}

func TestSaveKeepsRidersApart(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *workout.DB, rider string) {
		ctx := t.Context()
		// Two riders on one group ride, both head units started on the same
		// second: the same start, the same file shape. Neither may take the
		// other's row.
		r := parsed(t, rideSpec{start: at(2026, 2, 1, 9, 0), seconds: 3000, watts: 200})
		other := rider + "-b"
		if out, err := Save(ctx, db, rider, r); err != nil || out != Added {
			t.Fatalf("first rider = %v, %v", out, err)
		}
		if out, err := Save(ctx, db, other, r); err != nil || out != Added {
			t.Fatalf("second rider = %v, %v, want their own Added", out, err)
		}
		for _, who := range []string{rider, other} {
			if sessions, _ := db.ListSessions(ctx, who); len(sessions) != 1 || sessions[0].Rider != who {
				t.Errorf("%s sessions = %+v", who, sessions)
			}
		}
	})
}

func TestSaveNeverMatchesAPlanOrMovesALevel(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *workout.DB, rider string) {
		ctx := t.Context()
		// A planned threshold session on the very day of the imported ride, which
		// the ride matches well enough that a synced ride would be scored by it.
		if _, err := db.CreateWorkout(ctx, workout.CreateWorkoutRequest{
			Rider: rider, Sport: model.SportCycling, Name: "Threshold", Date: "2026-02-01",
			Zone: workout.ZoneThreshold, Level: 4,
			Steps: []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
				Seconds: 3000, Target: workout.TargetPower, TargetLow: 190, TargetHigh: 210}},
		}); err != nil {
			t.Fatal(err)
		}
		r := parsed(t, rideSpec{start: at(2026, 2, 1, 9, 0), seconds: 3000, watts: 200})
		if _, err := Save(ctx, db, rider, r); err != nil {
			t.Fatal(err)
		}
		analyses, _ := db.ListAnalyses(ctx, rider, "2000-01-01")
		if len(analyses) != 1 || analyses[0].WorkoutID != "" || analyses[0].Outcome != "unplanned" || len(analyses[0].Steps) != 0 {
			t.Errorf("analysis = %+v, want no plan match", analyses)
		}
		levels, _ := db.ListLevels(ctx, rider)
		if len(levels) != 0 {
			t.Errorf("levels = %+v: an import must never move a progression level", levels)
		}
	})
}

func TestExternalIDIsStableAndPerRider(t *testing.T) {
	a := ExternalID("wilant", 1700000000)
	if a != ExternalID("wilant", 1700000000) {
		t.Error("not deterministic")
	}
	if a == ExternalID("other", 1700000000) || a == ExternalID("wilant", 1700000001) {
		t.Error("must differ by rider and by start")
	}
	if strings.Contains(a, "wilant") {
		t.Errorf("the id %q carries the rider's name", a)
	}
	if ExternalID("Wilant", 1) != ExternalID(" wilant ", 1) {
		t.Error("the rider is normalised the way the store normalises it")
	}
}
