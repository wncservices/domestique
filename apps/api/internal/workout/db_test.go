package workout

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
	for _, table := range []string{"goals", "rider_profiles", "workouts", "completed_sessions", "session_analyses", "fitness_snapshots"} {
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

			t.Run("ListAllGoals spans every rider and puts undated goals last", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				undated, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
				if err != nil {
					t.Fatalf("create no-date goal: %v", err)
				}
				later, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Local Century", EventDate: "2026-11-01"})
				if err != nil {
					t.Fatalf("create wilant's dated goal: %v", err)
				}
				sooner, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "other", Name: "Gran Fondo", EventDate: "2026-08-01"})
				if err != nil {
					t.Fatalf("create other's dated goal: %v", err)
				}

				all, err := db.ListAllGoals(ctx)
				if err != nil {
					t.Fatalf("ListAllGoals: %v", err)
				}
				var order []string
				for _, g := range all {
					order = append(order, g.ID)
				}
				want := []string{sooner.ID, later.ID, undated.ID}
				if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
					t.Errorf("order = %v, want %v — nearest event first, undated last", order, want)
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

			t.Run("estimated fields round-trip and a rider save clears them", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				p := RiderProfile{Rider: "wilant", MaxHR: 190}
				p.MarkEstimated(FieldMaxHR)
				p.MarkEstimated(FieldAvailableDays)
				p.MarkEstimated(FieldMaxHR) // idempotent
				if _, err := db.SaveProfile(ctx, p); err != nil {
					t.Fatalf("save profile: %v", err)
				}

				fetched, _, err := db.GetProfile(ctx, "wilant")
				if err != nil {
					t.Fatalf("get profile: %v", err)
				}
				if len(fetched.Estimated) != 2 || !fetched.IsEstimated(FieldMaxHR) || !fetched.IsEstimated(FieldAvailableDays) {
					t.Errorf("estimated = %v, want max_hr and available_days once each", fetched.Estimated)
				}
				if fetched.IsEstimated(FieldRestingHR) {
					t.Error("resting_hr was never marked")
				}

				// A save that carries no list — what the rider's form does —
				// confirms every value.
				if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", MaxHR: 190}); err != nil {
					t.Fatalf("save profile: %v", err)
				}
				fetched, _, _ = db.GetProfile(ctx, "wilant")
				if len(fetched.Estimated) != 0 {
					t.Errorf("estimated = %v, want none after a rider save", fetched.Estimated)
				}
			})

			t.Run("push records upsert, list opted-in riders, and vanish with the workout", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", AutoPushWorkouts: true}); err != nil {
					t.Fatalf("save opted-in profile: %v", err)
				}
				if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "other"}); err != nil {
					t.Fatalf("save profile: %v", err)
				}
				riders, err := db.ListAutoPushRiders(ctx)
				if err != nil || len(riders) != 1 || riders[0] != "wilant" {
					t.Fatalf("riders = %v, err = %v, want only wilant", riders, err)
				}

				wk, err := db.CreateWorkout(ctx, CreateWorkoutRequest{
					Rider: "wilant", Sport: "cycling", Name: "Tempo",
					Steps: []WorkoutStep{{Name: "Ride", Duration: DurationOpen, Target: TargetOpen}},
				})
				if err != nil {
					t.Fatalf("create workout: %v", err)
				}
				if _, have, _ := db.GetPush(ctx, wk.ID, "garmin"); have {
					t.Error("a workout never pushed has no record")
				}

				p := Push{WorkoutID: wk.ID, Provider: "garmin", RemoteID: "r1", ContentHash: ContentHash(wk)}
				if err := db.SavePush(ctx, p); err != nil {
					t.Fatalf("save push: %v", err)
				}
				p.ScheduleID, p.ScheduledDate = "e1", "2026-03-20"
				if err := db.SavePush(ctx, p); err != nil {
					t.Fatalf("save push again: %v", err)
				}
				got, have, err := db.GetPush(ctx, wk.ID, "garmin")
				if err != nil || !have || got.ScheduleID != "e1" || got.ScheduledDate != "2026-03-20" || got.RemoteID != "r1" {
					t.Errorf("push = %+v have=%v err=%v, want the second write to have replaced the first", got, have, err)
				}

				renamed := wk
				renamed.Name = "Tempo 2"
				if ContentHash(renamed) == ContentHash(wk) {
					t.Error("renaming must change the content hash")
				}
				moved := wk
				moved.Date = "2026-04-01"
				if ContentHash(moved) != ContentHash(wk) {
					t.Error("moving to another day must not change the content hash: it is the same workout")
				}

				if err := db.DeleteWorkout(ctx, wk.ID); err != nil {
					t.Fatalf("delete workout: %v", err)
				}
				if _, have, _ := db.GetPush(ctx, wk.ID, "garmin"); have {
					t.Error("the push record must go with the workout")
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

			t.Run("session analysis saves, round-trips, upserts, filters by rider and date, and feeds fitness", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				session, err := db.UpsertSession(ctx, UpsertSessionRequest{
					Rider: "wilant", Provider: "garmin", ExternalID: "123", Sport: "cycling",
					Date: "2026-03-01", DurationSeconds: 3600, DistanceM: 30000,
					AvgHR: 150, AvgPowerWatts: 200, TrainingLoad: 60,
				})
				if err != nil {
					t.Fatalf("upsert session: %v", err)
				}

				analysis := SessionAnalysis{
					SessionID: session.ID, Rider: "Wilant", WorkoutID: "threshold-6x3",
					Outcome: "struggled", LoadSource: "power",
					NormalizedPower: 245.5, IntensityFactor: 0.98, TSS: 92.3, DurationRatio: 1.02,
					PowerZoneSeconds: []int{100, 200, 900, 1200, 800, 400},
					HRZoneSeconds:    []int{300, 600, 1500, 900, 300},
					PowerCurve:       map[string]float64{"5": 550, "60": 400, "300": 260, "1200": 230},
					Steps: []AnalysisStep{
						{Index: 0, Name: "Warmup", Target: "open", Actual: 150, Result: "hit"},
						{Index: 1, Name: "On", Target: "power", Low: 280, High: 300, Actual: 275, Result: "under", InTargetPct: 62.5},
					},
				}
				if err := db.SaveAnalysis(ctx, analysis); err != nil {
					t.Fatalf("save analysis: %v", err)
				}

				fetched, ok, err := db.GetAnalysis(ctx, session.ID)
				if err != nil || !ok {
					t.Fatalf("get analysis: ok=%v err=%v", ok, err)
				}
				if fetched.Rider != "wilant" {
					t.Errorf("rider = %q, want normalized wilant", fetched.Rider)
				}
				if fetched.WorkoutID != analysis.WorkoutID || fetched.Outcome != analysis.Outcome ||
					fetched.LoadSource != analysis.LoadSource {
					t.Errorf("fetched = %+v", fetched)
				}
				if fetched.NormalizedPower != analysis.NormalizedPower || fetched.IntensityFactor != analysis.IntensityFactor ||
					fetched.TSS != analysis.TSS || fetched.DurationRatio != analysis.DurationRatio {
					t.Errorf("fetched numbers = %+v, want %+v", fetched, analysis)
				}
				if len(fetched.PowerZoneSeconds) != 6 || fetched.PowerZoneSeconds[2] != 900 {
					t.Errorf("power zone seconds = %v", fetched.PowerZoneSeconds)
				}
				if len(fetched.HRZoneSeconds) != 5 || fetched.HRZoneSeconds[1] != 600 {
					t.Errorf("hr zone seconds = %v", fetched.HRZoneSeconds)
				}
				if fetched.PowerCurve["60"] != 400 {
					t.Errorf("power curve = %v", fetched.PowerCurve)
				}
				if len(fetched.Steps) != 2 || fetched.Steps[1].InTargetPct != 62.5 || fetched.Steps[1].Low != 280 {
					t.Errorf("steps = %+v", fetched.Steps)
				}
				if fetched.AnalysedAt == "" {
					t.Error("analysed_at was not stamped")
				}

				// Upsert replaces rather than accumulating a second row.
				analysis.Outcome = "nailed_it"
				analysis.TSS = 95
				if err := db.SaveAnalysis(ctx, analysis); err != nil {
					t.Fatalf("re-save analysis: %v", err)
				}
				replaced, ok, err := db.GetAnalysis(ctx, session.ID)
				if err != nil || !ok || replaced.Outcome != "nailed_it" || replaced.TSS != 95 {
					t.Fatalf("replaced = %+v, ok=%v, err=%v, want outcome nailed_it tss 95", replaced, ok, err)
				}

				otherSession, err := db.UpsertSession(ctx, UpsertSessionRequest{
					Rider: "other", Provider: "garmin", ExternalID: "999", Sport: "cycling",
					Date: "2026-03-05", DurationSeconds: 1800, TrainingLoad: 40,
				})
				if err != nil {
					t.Fatalf("upsert other rider's session: %v", err)
				}
				if err := db.SaveAnalysis(ctx, SessionAnalysis{
					SessionID: otherSession.ID, Rider: "other", Outcome: "completed",
				}); err != nil {
					t.Fatalf("save other rider's analysis: %v", err)
				}

				list, err := db.ListAnalyses(ctx, "WILANT", "2026-01-01")
				if err != nil {
					t.Fatalf("list analyses: %v", err)
				}
				if len(list) != 1 || list[0].SessionID != session.ID {
					t.Errorf("list = %+v, want only wilant's session", list)
				}

				// sinceDate excludes sessions before it.
				if none, err := db.ListAnalyses(ctx, "wilant", "2026-04-01"); err != nil || len(none) != 0 {
					t.Errorf("list since a future date = %+v, err %v, want none", none, err)
				}

				// GetAnalysis for a session that was never analysed.
				if _, ok, err := db.GetAnalysis(ctx, "garmin:does-not-exist"); err != nil || ok {
					t.Errorf("get missing analysis: ok=%v err=%v", ok, err)
				}

				// ComputeFitness reports a day's CTL/ATL/TSB as of *before* that
				// day's own load is folded in (see its own doc comment), so the
				// effect of raising the session's load only shows up starting
				// the following day.
				sessionDate, err := time.Parse("2006-01-02", session.Date)
				if err != nil {
					t.Fatalf("parse session date: %v", err)
				}
				nextDate := sessionDate.AddDate(0, 0, 1).Format("2006-01-02")

				if err := db.RecomputeFitnessSnapshots(ctx, "wilant"); err != nil {
					t.Fatalf("recompute before SetSessionLoad: %v", err)
				}
				before, err := db.ListFitnessSnapshots(ctx, "wilant")
				if err != nil {
					t.Fatalf("list snapshots: %v", err)
				}
				var beforeCTL float64
				for _, s := range before {
					if s.Date == nextDate {
						beforeCTL = s.CTL
					}
				}

				if err := db.SetSessionLoad(ctx, session.ID, 300); err != nil {
					t.Fatalf("set session load: %v", err)
				}
				if err := db.RecomputeFitnessSnapshots(ctx, "wilant"); err != nil {
					t.Fatalf("recompute after SetSessionLoad: %v", err)
				}
				after, err := db.ListFitnessSnapshots(ctx, "wilant")
				if err != nil {
					t.Fatalf("list snapshots after: %v", err)
				}
				var afterCTL float64
				for _, s := range after {
					if s.Date == nextDate {
						afterCTL = s.CTL
					}
				}
				if afterCTL <= beforeCTL {
					t.Errorf("CTL on %s after raising load = %v, want greater than before (%v)", nextDate, afterCTL, beforeCTL)
				}

				if err := db.SetSessionLoad(ctx, "garmin:does-not-exist", 100); err == nil {
					t.Error("expected an error setting load on a session that does not exist")
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
