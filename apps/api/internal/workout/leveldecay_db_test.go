package workout

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

func TestDecayClaimIsOncePerWeek(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		for i, want := range []bool{true, false} {
			got, err := db.ClaimDecayWeek(ctx, []string{"Wilant", "wilant"}[i], "2026-10-19")
			if err != nil || got != want {
				t.Fatalf("claim %d = %v, %v; want %v", i, got, err, want)
			}
		}
		if got, err := db.ClaimDecayWeek(ctx, "wilant", "2026-10-26"); err != nil || !got {
			t.Fatalf("next week = %v, %v; want claimed", got, err)
		}
		if got, err := db.ClaimDecayWeek(ctx, "other", "2026-10-19"); err != nil || !got {
			t.Fatalf("another rider = %v, %v; want claimed", got, err)
		}
	})
}

func TestLastTrainedByZone(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		ride := func(rider string, sport model.Sport, zone Zone, date, ext string) {
			t.Helper()
			wk, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: rider, Sport: sport, Name: "w", Steps: exampleSteps(), Zone: zone, Level: 4})
			if err != nil {
				t.Fatal(err)
			}
			sess, err := db.UpsertSession(ctx, UpsertSessionRequest{Rider: rider, Provider: "garmin", ExternalID: ext, Sport: string(sport), Date: date, DurationSeconds: 3600})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.SaveAnalysis(ctx, SessionAnalysis{SessionID: sess.ID, Rider: rider, WorkoutID: wk.ID, Outcome: "nailed"}); err != nil {
				t.Fatal(err)
			}
		}
		ride("wilant", model.SportCycling, ZoneThreshold, "2026-09-01", "a")
		ride("wilant", model.SportCycling, ZoneThreshold, "2026-09-20", "b")
		ride("wilant", model.SportCycling, ZoneVO2Max, "2026-08-15", "c")
		ride("wilant", model.SportRunning, ZoneTempo, "2026-10-01", "d")
		ride("other", model.SportCycling, ZoneSweetSpot, "2026-10-02", "e")

		got, err := db.LastTrainedByZone(ctx, "wilant", "cycling")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got["threshold"] != "2026-09-20" || got["vo2max"] != "2026-08-15" {
			t.Errorf("cycling = %v, want threshold 09-20 and vo2max 08-15 only", got)
		}
	})
}
