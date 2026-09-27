package workout

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// TestProgressionLevelsEachEngine mirrors TestEachEngine's own
// SQLite-and-PostgreSQL structure (see db_test.go) for the progression_levels
// table and the session_analyses feel/level_delta columns — both are new in
// this change, so they get their own top-level test rather than growing
// db_test.go's already-large TestEachEngine further.
func TestProgressionLevelsEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("level saves, round-trips, upserts, and lists owner-only", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				err := db.SaveLevel(ctx, ProgressionLevel{
					Rider: "Wilant", Sport: model.SportCycling, Zone: ZoneThreshold,
					Level: 4.6, Reason: "Nailed Threshold 3×12 (5.0) — threshold 4.3 → 4.6",
				})
				if err != nil {
					t.Fatalf("save level: %v", err)
				}
				err = db.SaveLevel(ctx, ProgressionLevel{
					Rider: "wilant", Sport: model.SportCycling, Zone: ZoneSweetSpot, Level: 3.0,
				})
				if err != nil {
					t.Fatalf("save second level: %v", err)
				}
				if err := db.SaveLevel(ctx, ProgressionLevel{
					Rider: "other", Sport: model.SportCycling, Zone: ZoneThreshold, Level: 9.9,
				}); err != nil {
					t.Fatalf("save other rider's level: %v", err)
				}

				levels, err := db.ListLevels(ctx, "WILANT")
				if err != nil {
					t.Fatalf("list levels: %v", err)
				}
				if len(levels) != 2 {
					t.Fatalf("list = %+v, want 2 levels for wilant only", levels)
				}
				byZone := map[Zone]ProgressionLevel{}
				for _, l := range levels {
					if l.Rider != "wilant" {
						t.Errorf("level %+v: rider not normalized", l)
					}
					byZone[l.Zone] = l
				}
				threshold, ok := byZone[ZoneThreshold]
				if !ok {
					t.Fatalf("missing threshold level in %+v", levels)
				}
				if threshold.Level != 4.6 || threshold.Reason != "Nailed Threshold 3×12 (5.0) — threshold 4.3 → 4.6" {
					t.Errorf("threshold level = %+v", threshold)
				}
				if threshold.Sport != model.SportCycling {
					t.Errorf("sport = %q", threshold.Sport)
				}
				if threshold.UpdatedAt == "" {
					t.Error("updated_at was not stamped")
				}

				// Re-saving the same (rider, sport, zone) upserts rather
				// than accumulating a second row.
				if err := db.SaveLevel(ctx, ProgressionLevel{
					Rider: "wilant", Sport: model.SportCycling, Zone: ZoneThreshold,
					Level: 5.3, Reason: "Nailed Threshold 3×15 (5.3) — threshold 4.6 → 5.3",
				}); err != nil {
					t.Fatalf("re-save level: %v", err)
				}
				levels, err = db.ListLevels(ctx, "wilant")
				if err != nil {
					t.Fatalf("list levels after upsert: %v", err)
				}
				if len(levels) != 2 {
					t.Fatalf("list after upsert = %+v, want still 2 rows (upsert, not insert)", levels)
				}
				for _, l := range levels {
					if l.Zone == ZoneThreshold && l.Level != 5.3 {
						t.Errorf("threshold level after upsert = %v, want 5.3", l.Level)
					}
				}

				// A rider with no saved levels gets an empty list, not an
				// error — ListLevels never invents rows.
				none, err := db.ListLevels(ctx, "nobody")
				if err != nil || len(none) != 0 {
					t.Errorf("list for unknown rider = %+v, err %v, want empty", none, err)
				}
			})

			t.Run("saving a level without a rider, sport or zone fails", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				if err := db.SaveLevel(ctx, ProgressionLevel{Sport: model.SportCycling, Zone: ZoneTempo, Level: 3}); err == nil {
					t.Error("expected an error for a level with no rider")
				}
				if err := db.SaveLevel(ctx, ProgressionLevel{Rider: "wilant", Zone: ZoneTempo, Level: 3}); err == nil {
					t.Error("expected an error for a level with no sport")
				}
				if err := db.SaveLevel(ctx, ProgressionLevel{Rider: "wilant", Sport: model.SportCycling, Level: 3}); err == nil {
					t.Error("expected an error for a level with no zone")
				}
			})

			t.Run("SetAnalysisFeel round-trips feel and level_delta, and a re-rate replaces rather than stacks", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				session, err := db.UpsertSession(ctx, UpsertSessionRequest{
					Rider: "wilant", Provider: "garmin", ExternalID: "feel-1", Sport: "cycling",
					Date: "2026-03-02", DurationSeconds: 3600, TrainingLoad: 60,
				})
				if err != nil {
					t.Fatalf("upsert session: %v", err)
				}
				if err := db.SaveAnalysis(ctx, SessionAnalysis{
					SessionID: session.ID, Rider: "wilant", WorkoutID: "threshold-3x12", Outcome: "nailed",
				}); err != nil {
					t.Fatalf("save analysis: %v", err)
				}

				if err := db.SetAnalysisFeel(ctx, session.ID, 3, 0.3); err != nil {
					t.Fatalf("set analysis feel: %v", err)
				}
				a, ok, err := db.GetAnalysis(ctx, session.ID)
				if err != nil || !ok {
					t.Fatalf("get analysis: ok=%v err=%v", ok, err)
				}
				if a.Feel != 3 || a.LevelDelta != 0.3 {
					t.Fatalf("feel/delta = %d/%v, want 3/0.3", a.Feel, a.LevelDelta)
				}

				// Re-rating replaces the stored delta rather than adding
				// to it — the caller is responsible for undoing the old
				// delta against the level itself; this just holds whatever
				// the caller last recorded.
				if err := db.SetAnalysisFeel(ctx, session.ID, 5, 0.1); err != nil {
					t.Fatalf("re-rate: %v", err)
				}
				a, ok, err = db.GetAnalysis(ctx, session.ID)
				if err != nil || !ok || a.Feel != 5 || a.LevelDelta != 0.1 {
					t.Fatalf("after re-rate: feel/delta = %+v, ok=%v, err=%v, want 5/0.1", a, ok, err)
				}

				if err := db.SetAnalysisFeel(ctx, "does-not-exist", 3, 0.1); err == nil {
					t.Error("expected an error rating a session that does not exist")
				}
			})
		})
	}
}

// TestSessionAnalysesTableGainsFeelAndLevelDeltaColumns mirrors
// TestWorkoutsTableGainsZoneAndLevelColumns's own migration shape (see
// db_test.go): a pre-existing session_analyses table without feel/
// level_delta must migrate cleanly, defaulting existing rows to 0/0, and
// UseDB must stay idempotent against an already-migrated database.
func TestSessionAnalysesTableGainsFeelAndLevelDeltaColumns(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	// The pre-migration shape of session_analyses, minus feel/level_delta.
	if _, err := src.Conn().Exec(`
CREATE TABLE session_analyses (
    session_id          TEXT PRIMARY KEY,
    rider                TEXT NOT NULL,
    workout_id           TEXT NOT NULL DEFAULT '',
    outcome              TEXT NOT NULL DEFAULT '',
    load_source          TEXT NOT NULL DEFAULT '',
    normalized_power     DOUBLE PRECISION NOT NULL DEFAULT 0,
    intensity_factor     DOUBLE PRECISION NOT NULL DEFAULT 0,
    tss                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration_ratio       DOUBLE PRECISION NOT NULL DEFAULT 0,
    power_zone_seconds   TEXT NOT NULL DEFAULT '',
    hr_zone_seconds      TEXT NOT NULL DEFAULT '',
    power_curve          TEXT NOT NULL DEFAULT '',
    steps                TEXT NOT NULL DEFAULT '',
    analysed_at          TEXT NOT NULL
)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := src.Conn().Exec(`
INSERT INTO session_analyses (session_id, rider, workout_id, outcome, load_source, analysed_at)
VALUES ('old-session', 'wilant', '', 'completed', '', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	db, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("UseDB (migrate): %v", err)
	}

	old, ok, err := db.GetAnalysis(t.Context(), "old-session")
	if err != nil || !ok {
		t.Fatalf("get pre-existing row after migration: ok=%v err=%v", ok, err)
	}
	if old.Feel != 0 || old.LevelDelta != 0 {
		t.Errorf("pre-existing row feel/level_delta = %d/%v, want 0/0", old.Feel, old.LevelDelta)
	}

	if err := db.SetAnalysisFeel(t.Context(), "old-session", 4, -0.1); err != nil {
		t.Fatalf("set feel after migration: %v", err)
	}
	updated, ok, err := db.GetAnalysis(t.Context(), "old-session")
	if err != nil || !ok || updated.Feel != 4 || updated.LevelDelta != -0.1 {
		t.Fatalf("after set feel: %+v, ok=%v, err=%v", updated, ok, err)
	}

	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Errorf("second UseDB call: %v", err)
	}
}
