package workout

import (
	"strings"
	"testing"
	"time"
)

// Every date or stamp the store derives comes from DB.Clock, not the wall
// clock. api's tests fix Server.Clock and rely on this: a plan anchors to the
// Monday of a goal's created_at, an untouched session is one whose updated_at
// still equals created_at, and fitness runs up to "today". With the store on
// the real clock those outcomes changed with the day the suite ran.
func TestTheStoreStampsFromItsClockOnEachEngine(t *testing.T) {
	fixed := time.Date(2031, time.March, 18, 9, 30, 0, 0, time.UTC)
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			db.Clock = func() time.Time { return fixed }
			ctx := t.Context()

			g, err := db.CreateGoal(ctx, CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
			if err != nil {
				t.Fatal(err)
			}
			if g.CreatedAt != "2031-03-18T09:30:00Z" || g.UpdatedAt != g.CreatedAt {
				t.Errorf("goal stamped %q/%q, want the store clock", g.CreatedAt, g.UpdatedAt)
			}

			p, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 250})
			if err != nil {
				t.Fatal(err)
			}
			if p.FTPVerifiedAt != "2031-03-18" {
				t.Errorf("FTP verified %q, want the store's day", p.FTPVerifiedAt)
			}

			if _, err := db.UpsertSession(ctx, UpsertSessionRequest{
				Rider: "wilant", Provider: "garmin", ExternalID: "s1", Sport: "cycling",
				Date: "2031-03-10", DurationSeconds: 3600, TrainingLoad: 100,
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.RecomputeFitnessSnapshots(ctx, "wilant"); err != nil {
				t.Fatal(err)
			}
			var last string
			if err := db.db.QueryRowContext(ctx, db.query(`SELECT MAX(date) FROM fitness_snapshots WHERE rider = ?`), "wilant").Scan(&last); err != nil {
				t.Fatal(err)
			}
			if last != "2031-03-18" {
				t.Errorf("fitness runs to %q, want the store's today", last)
			}

			if !strings.HasPrefix(db.adjustmentTime(), "2031-03-18T09:30:00") {
				t.Errorf("adjustment time %q is not from the store clock", db.adjustmentTime())
			}
		})
	}
}
