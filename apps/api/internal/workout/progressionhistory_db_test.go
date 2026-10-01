package workout

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

// Every move a level makes is a point on the chart; saving the same value
// again is not.
func TestLevelHistoryRecordsEveryMoveEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres}
	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			save := func(level float64, at string) {
				t.Helper()
				if err := db.SaveLevel(ctx, ProgressionLevel{Rider: "wilant", Sport: model.SportCycling, Zone: ZoneThreshold, Level: level, UpdatedAt: at}); err != nil {
					t.Fatal(err)
				}
			}
			save(3.0, "2026-08-01T10:00:00Z")
			save(3.4, "2026-09-01T10:00:00Z")
			save(3.4, "2026-09-02T10:00:00Z") // unchanged: no point
			save(3.1, "2026-09-20T10:00:00Z")

			got, err := db.LevelHistory(ctx, "wilant", "2026-09-01")
			if err != nil {
				t.Fatal(err)
			}
			// The last value before the window comes first, so the line starts
			// where the rider was.
			want := []float64{3.0, 3.4, 3.1}
			if len(got) != len(want) {
				t.Fatalf("history = %+v, want levels %v", got, want)
			}
			for i, p := range got {
				if p.Level != want[i] || p.Zone != ZoneThreshold {
					t.Errorf("point %d = %+v, want level %v", i, p, want[i])
				}
			}
		})
	}
}

// Levels saved before the history table existed get one, walked back from
// today's level through each ride's own level_delta.
func TestBackfillProgressionHistoryWalksBackThroughRides(t *testing.T) {
	db := openTestDB(t)
	ctx := t.Context()
	wk, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "Threshold", Steps: exampleSteps(), Zone: ZoneThreshold, Level: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range []struct {
		date  string
		delta float64
	}{{"2026-09-10", 0.3}, {"2026-09-20", 0.2}} {
		sess, err := db.UpsertSession(ctx, UpsertSessionRequest{Rider: "wilant", Provider: "garmin", ExternalID: string(rune('a' + i)), Sport: "cycling", Date: r.date, DurationSeconds: 3600})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveAnalysis(ctx, SessionAnalysis{SessionID: sess.ID, Rider: "wilant", WorkoutID: wk.ID, Outcome: "nailed", LevelDelta: r.delta}); err != nil {
			t.Fatal(err)
		}
	}
	// A level as it stood before history existed: no history rows.
	if _, err := db.db.Exec(`INSERT INTO progression_levels (rider, sport, zone, level, reason, updated_at) VALUES ('wilant', 'cycling', 'threshold', 3.5, '', '2026-09-20T18:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	for range 2 { // once only
		if err := db.backfillProgressionHistory(); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.LevelHistory(ctx, "wilant", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		at    string
		level float64
	}{{"2026-09-09T12:00:00Z", 3.0}, {"2026-09-10T12:00:00Z", 3.3}, {"2026-09-20T12:00:00Z", 3.5}}
	if len(got) != len(want) {
		t.Fatalf("history = %+v, want %+v", got, want)
	}
	for i, p := range got {
		if p.At != want[i].at || p.Level != want[i].level {
			t.Errorf("point %d = %s %.1f, want %s %.1f", i, p.At, p.Level, want[i].at, want[i].level)
		}
	}
}
