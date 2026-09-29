package workout

import "testing"

func TestScheduledWeeksEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}
	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("a week is unrecorded until it is marked, per goal", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				if done, err := db.WeekScheduled(ctx, "race", "2026-10-05"); err != nil || done {
					t.Fatalf("before marking: done=%v err=%v, want false, nil", done, err)
				}
				if err := db.MarkWeekScheduled(ctx, "race", "2026-10-05"); err != nil {
					t.Fatal(err)
				}
				// Idempotent: filling a week twice must not be an error.
				if err := db.MarkWeekScheduled(ctx, "race", "2026-10-05"); err != nil {
					t.Fatalf("marking twice: %v", err)
				}
				if done, _ := db.WeekScheduled(ctx, "race", "2026-10-05"); !done {
					t.Error("marked week reads as unrecorded")
				}
				if done, _ := db.WeekScheduled(ctx, "race", "2026-10-12"); done {
					t.Error("a different week reads as recorded")
				}
				if done, _ := db.WeekScheduled(ctx, "other-goal", "2026-10-05"); done {
					t.Error("a different goal reads as recorded")
				}
			})

			t.Run("deleting a goal forgets its weeks", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				g, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Race"})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.MarkWeekScheduled(ctx, g.ID, "2026-10-05"); err != nil {
					t.Fatal(err)
				}
				if err := db.MarkWeekScheduled(ctx, "keep", "2026-10-05"); err != nil {
					t.Fatal(err)
				}
				if err := db.DeleteGoal(ctx, g.ID); err != nil {
					t.Fatal(err)
				}
				if done, _ := db.WeekScheduled(ctx, g.ID, "2026-10-05"); done {
					t.Error("a deleted goal's week is still recorded")
				}
				if done, _ := db.WeekScheduled(ctx, "keep", "2026-10-05"); !done {
					t.Error("deleting one goal forgot another goal's week")
				}
			})
		})
	}
}
