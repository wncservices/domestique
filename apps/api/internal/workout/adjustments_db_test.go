package workout

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/why"
)

func recordOf(rule why.Rule, text string, in any) why.Record { return why.NewRecord(rule, text, in) }

// TestAdjustmentsEachEngine covers the adjustments table on SQLite and
// PostgreSQL: they differ in placeholders and upsert dialect, so passing on
// one says nothing about the other.
func TestAdjustmentsEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}
	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("a record round-trips with its inputs, text and day", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				rec := recordOf(why.MissedMoved, "moved from 2026-03-24", why.MissedMovedInputs{From: "2026-03-24", To: "2026-03-27", ReplacedEasy: true})
				if err := db.RecordAdjustment(ctx, "Wilant", SubjectWorkout, "w1", rec, "2026-03-26"); err != nil {
					t.Fatal(err)
				}
				got, err := db.LatestAdjustments(ctx, "wilant", SubjectWorkout, []string{"w1"})
				if err != nil {
					t.Fatal(err)
				}
				a, ok := got["w1"]
				if !ok {
					t.Fatalf("no adjustment for w1: %+v", got)
				}
				if a.Rule != why.MissedMoved || a.Text != "moved from 2026-03-24" || a.Day != "2026-03-26" || a.Rider != "wilant" || a.ID == "" {
					t.Errorf("adjustment = %+v", a)
				}
				if a.Inputs["to"] != "2026-03-27" || a.Inputs["replacedEasy"] != true {
					t.Errorf("inputs = %v", a.Inputs)
				}
			})

			t.Run("recording the same rule on the same day again replaces the row", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				first := recordOf(why.ReadinessCaution, "first", why.ReadinessInputs{Verdict: "caution"})
				second := recordOf(why.ReadinessCaution, "second", why.ReadinessInputs{Verdict: "caution"})
				for _, r := range []why.Record{first, second} {
					if err := db.RecordAdjustment(ctx, "wilant", SubjectWorkout, "w1", r, "2026-03-26"); err != nil {
						t.Fatal(err)
					}
				}
				var n int
				if err := db.db.QueryRowContext(ctx, db.query(`SELECT COUNT(1) FROM adjustments WHERE subject_id = ?`), "w1").Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Errorf("rows = %d, want the retried write to be idempotent", n)
				}
				got, _ := db.LatestAdjustments(ctx, "wilant", SubjectWorkout, []string{"w1"})
				if got["w1"].Text != "second" {
					t.Errorf("text = %q, want the latest write", got["w1"].Text)
				}
			})

			t.Run("the latest row by time wins across rules and days", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				steps := []struct {
					rule why.Rule
					text string
					day  string
				}{
					{why.ReadinessCaution, "older", "2026-03-24"},
					{why.FatigueOverload, "newer", "2026-03-25"},
				}
				for _, s := range steps {
					if err := db.RecordAdjustment(ctx, "wilant", SubjectWorkout, "w1", recordOf(s.rule, s.text, why.FatigueOverloadInputs{}), s.day); err != nil {
						t.Fatal(err)
					}
					time.Sleep(3 * time.Millisecond)
				}
				got, _ := db.LatestAdjustments(ctx, "wilant", SubjectWorkout, []string{"w1"})
				if got["w1"].Text != "newer" {
					t.Errorf("latest = %q, want newer", got["w1"].Text)
				}
			})

			t.Run("reads are filtered by rider and kind", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				rec := recordOf(why.MissedMoved, "mine", why.MissedMovedInputs{})
				if err := db.RecordAdjustment(ctx, "wilant", SubjectWorkout, "w1", rec, "2026-03-26"); err != nil {
					t.Fatal(err)
				}
				if got, _ := db.LatestAdjustments(ctx, "other", SubjectWorkout, []string{"w1"}); len(got) != 0 {
					t.Errorf("another rider read %+v", got)
				}
				if got, _ := db.LatestAdjustments(ctx, "wilant", SubjectLevel, []string{"w1"}); len(got) != 0 {
					t.Errorf("a level read returned a workout row: %+v", got)
				}
			})

			t.Run("many ids are read in one batch", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				var ids []string
				for i := 0; i < 30; i++ {
					id := fmt.Sprintf("w%d", i)
					ids = append(ids, id)
					if i%2 == 0 {
						if err := db.RecordAdjustment(ctx, "wilant", SubjectWorkout, id, recordOf(why.MissedMoved, id, why.MissedMovedInputs{}), "2026-03-26"); err != nil {
							t.Fatal(err)
						}
					}
				}
				got, err := db.LatestAdjustments(ctx, "wilant", SubjectWorkout, ids)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != 15 || got["w4"].Text != "w4" || got["w5"].ID != "" {
					t.Errorf("got %d rows: %+v", len(got), got)
				}
				if got, err := db.LatestAdjustments(ctx, "wilant", SubjectWorkout, nil); err != nil || len(got) != 0 {
					t.Errorf("no ids = %+v, %v; want an empty result", got, err)
				}
			})

			t.Run("two riders' levels with the same subject do not collide", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				for _, rider := range []string{"wilant", "other"} {
					if err := db.RecordAdjustment(ctx, rider, SubjectLevel, "cycling:threshold", recordOf(why.LevelRecalibration, "for "+rider, why.LevelRecalibrationInputs{}), "2026-03-26"); err != nil {
						t.Fatal(err)
					}
				}
				for _, rider := range []string{"wilant", "other"} {
					got, _ := db.LatestAdjustments(ctx, rider, SubjectLevel, []string{"cycling:threshold"})
					if got["cycling:threshold"].Text != "for "+rider {
						t.Errorf("%s reads %q", rider, got["cycling:threshold"].Text)
					}
				}
			})

			t.Run("deleting a workout removes its adjustments and only its own", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				a, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Sport: model.SportCycling, Name: "A", Date: "2026-03-26", Steps: exampleSteps()})
				if err != nil {
					t.Fatal(err)
				}
				b, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Sport: model.SportCycling, Name: "B", Date: "2026-03-27", Steps: exampleSteps()})
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{a.ID, b.ID} {
					if err := db.RecordAdjustment(ctx, "wilant", SubjectWorkout, id, recordOf(why.MissedMoved, id, why.MissedMovedInputs{}), "2026-03-26"); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.DeleteWorkout(ctx, a.ID); err != nil {
					t.Fatal(err)
				}
				got, _ := db.LatestAdjustments(ctx, "wilant", SubjectWorkout, []string{a.ID, b.ID})
				if _, ok := got[a.ID]; ok {
					t.Error("the deleted workout's adjustment survived it")
				}
				if _, ok := got[b.ID]; !ok {
					t.Error("another workout's adjustment was removed")
				}
			})

			t.Run("a level subject keeps only its latest five", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				for i := 1; i <= 7; i++ {
					day := fmt.Sprintf("2026-03-%02d", i)
					if err := db.RecordAdjustment(ctx, "wilant", SubjectLevel, "cycling:threshold", recordOf(why.LevelRecalibration, day, why.LevelRecalibrationInputs{}), day); err != nil {
						t.Fatal(err)
					}
					time.Sleep(3 * time.Millisecond)
				}
				var n int
				if err := db.db.QueryRowContext(ctx, db.query(`SELECT COUNT(1) FROM adjustments WHERE subject_kind = ? AND subject_id = ?`), SubjectLevel, "cycling:threshold").Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 5 {
					t.Errorf("rows = %d, want 5", n)
				}
				got, _ := db.LatestAdjustments(ctx, "wilant", SubjectLevel, []string{"cycling:threshold"})
				if got["cycling:threshold"].Text != "2026-03-07" {
					t.Errorf("latest = %q, want the newest kept", got["cycling:threshold"].Text)
				}
				var oldest int
				if err := db.db.QueryRowContext(ctx, db.query(`SELECT COUNT(1) FROM adjustments WHERE text = ?`), "2026-03-01").Scan(&oldest); err != nil {
					t.Fatal(err)
				}
				if oldest != 0 {
					t.Error("the oldest level row was kept instead of the newest")
				}
			})

			t.Run("a rider's adjustments can all be removed", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				for _, rider := range []string{"wilant", "other"} {
					for _, id := range []string{"w1", "w2"} {
						if err := db.RecordAdjustment(ctx, rider, SubjectWorkout, rider+id, recordOf(why.MissedMoved, "x", why.MissedMovedInputs{}), "2026-03-26"); err != nil {
							t.Fatal(err)
						}
					}
				}
				n, err := db.DeleteRiderAdjustments(ctx, "Wilant")
				if err != nil || n != 2 {
					t.Fatalf("deleted %d, %v; want 2", n, err)
				}
				if got, _ := db.LatestAdjustments(ctx, "other", SubjectWorkout, []string{"otherw1"}); len(got) != 1 {
					t.Error("another rider's rows were removed")
				}
			})
		})
	}
}

// TestAdjustmentsTableIsCreatedOnADatabaseThatPredatesIt is the migration
// case: UseDB has to add the table to an existing database, twice over
// without complaint.
func TestAdjustmentsTableIsCreatedOnADatabaseThatPredatesIt(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "old.db")
	db := openStore(t, dsn)
	ctx := t.Context()
	if _, err := db.db.ExecContext(ctx, `DROP TABLE adjustments`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := UseDB(db.db, dsn); err != nil {
		t.Fatalf("migrating an old database: %v", err)
	}
	if _, err := UseDB(db.db, dsn); err != nil {
		t.Fatalf("migrating twice: %v", err)
	}
	rec := recordOf(why.MissedMoved, "x", why.MissedMovedInputs{})
	if err := db.RecordAdjustment(ctx, "wilant", SubjectWorkout, "w1", rec, "2026-03-26"); err != nil {
		t.Fatalf("recording after the migration: %v", err)
	}
}
