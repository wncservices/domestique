package workout

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestScheduledWeekRefreshEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}
	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("a filled week is unrefreshed until marked, once", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				if err := db.MarkWeekScheduled(ctx, "g", "2027-03-01"); err != nil {
					t.Fatal(err)
				}
				if err := db.MarkWeekScheduled(ctx, "g", "2027-03-08"); err != nil {
					t.Fatal(err)
				}
				weeks, err := db.ScheduledWeeks(ctx, "g")
				if err != nil {
					t.Fatal(err)
				}
				if len(weeks) != 2 || weeks["2027-03-01"] || weeks["2027-03-08"] {
					t.Fatalf("weeks = %v, want two unrefreshed weeks", weeks)
				}
				if err := db.MarkWeekRefreshed(ctx, "g", "2027-03-01"); err != nil {
					t.Fatal(err)
				}
				weeks, _ = db.ScheduledWeeks(ctx, "g")
				if !weeks["2027-03-01"] || weeks["2027-03-08"] {
					t.Errorf("weeks = %v, want only 03-01 refreshed", weeks)
				}
				other, _ := db.ScheduledWeeks(ctx, "someone-else")
				if len(other) != 0 {
					t.Errorf("another goal sees weeks: %v", other)
				}
			})

			t.Run("refreshing a week that was never filled does not record it", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				if err := db.MarkWeekRefreshed(ctx, "g", "2027-03-01"); err != nil {
					t.Fatal(err)
				}
				if done, _ := db.WeekScheduled(ctx, "g", "2027-03-01"); done {
					t.Error("MarkWeekRefreshed recorded an unfilled week")
				}
			})
		})
	}
}

// Rows from before the column existed were all filled as this week or next, so
// they must come out refreshed, and reopening the database must not stamp a
// week filled since.
func TestScheduledWeeksRefreshColumnBackfillsOnlyOnce(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	// Put the table back the way it was before the column existed.
	if _, err := src.Conn().Exec(`DROP TABLE scheduled_weeks`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Conn().Exec(`CREATE TABLE scheduled_weeks (goal_id TEXT NOT NULL, week_start TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (goal_id, week_start))`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Conn().Exec(`INSERT INTO scheduled_weeks VALUES ('g', '2026-10-05', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	reopened, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	weeks, _ := reopened.ScheduledWeeks(ctx, "g")
	if !weeks["2026-10-05"] {
		t.Fatalf("legacy week = %v, want refreshed", weeks)
	}

	if err := reopened.MarkWeekScheduled(ctx, "g", "2027-03-01"); err != nil {
		t.Fatal(err)
	}
	again, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	weeks, _ = again.ScheduledWeeks(ctx, "g")
	if weeks["2027-03-01"] {
		t.Error("restarting stamped a week that was filled after the migration")
	}
}
