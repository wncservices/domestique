package workout

import "testing"

func TestCreateGoalDefaultsPriorityByDate(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		dated, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Race", EventDate: "2027-04-14"})
		if err != nil {
			t.Fatal(err)
		}
		undated, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Keep going"})
		if err != nil {
			t.Fatal(err)
		}
		if dated.Priority != PriorityA || undated.Priority != PriorityB {
			t.Errorf("dated %q, undated %q; want A and B", dated.Priority, undated.Priority)
		}
	})
}

// Goals stored as B before dated goals defaulted to A become A, once: a rider
// who later picks B for a dated goal keeps it.
func TestDatedBGoalsMoveToAOnce(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		for _, g := range []CreateGoalRequest{
			{Rider: "wilant", Name: "Dated", EventDate: "2027-04-14", Priority: PriorityB},
			{Rider: "wilant", Name: "Undated", Priority: PriorityB},
			{Rider: "wilant", Name: "Kept C", EventDate: "2027-05-01", Priority: PriorityC},
		} {
			if _, err := db.CreateGoal(ctx, g); err != nil {
				t.Fatal(err)
			}
		}
		// The store's own start already ran the migration on an empty table.
		// Forget it, as on a deployment that predates it.
		if _, err := db.db.Exec(`DELETE FROM workout_migrations`); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := db.migrateDatedGoalPriority(); err != nil {
				t.Fatal(err)
			}
		}
		prio := func(id string) Priority {
			g, err := db.GetGoal(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			return g.Priority
		}
		if got := prio("dated"); got != PriorityA {
			t.Errorf("dated = %q, want A", got)
		}
		if got := prio("undated"); got != PriorityB {
			t.Errorf("undated = %q, want B", got)
		}
		if got := prio("kept-c"); got != PriorityC {
			t.Errorf("kept C = %q", got)
		}
		// A later B choice survives a restart.
		b := PriorityB
		if _, err := db.UpdateGoal(ctx, "dated", UpdateGoalRequest{Priority: &b}); err != nil {
			t.Fatal(err)
		}
		if err := db.migrateDatedGoalPriority(); err != nil {
			t.Fatal(err)
		}
		if got := prio("dated"); got != PriorityB {
			t.Errorf("dated after a restart = %q, want the rider's B", got)
		}
	})
}
