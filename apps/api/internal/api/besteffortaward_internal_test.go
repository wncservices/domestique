package api

import (
	"bytes"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var awardClock = time.Date(2026, time.September, 10, 9, 0, 0, 0, time.UTC)

func awardServer(t *testing.T) (*Server, *workout.DB, *bytes.Buffer, *sql.DB) {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	s := &Server{Training: store, Log: slog.New(slog.NewTextHandler(logs, nil)), Clock: func() time.Time { return awardClock }}
	store.Clock = s.Clock
	for i, date := range []string{"2026-03-01", "2026-04-01", "2026-05-01"} {
		if _, err := store.UpsertEfforts(t.Context(), "wilant", "seed"+date, "cycling", date, map[string]float64{"power_300": float64(250 + 5*i)}); err != nil {
			t.Fatal(err)
		}
	}
	return s, store, logs, db.Conn()
}

// A decision about an award is made once, at the ride's first analysis. If the
// fitness recompute that follows the analyses fails, the sync fails and the ride
// is not analysed again, so the award has to be written before the recompute or
// it is lost for good.
func TestAnAwardIsWrittenEvenWhenTheFitnessRecomputeFails(t *testing.T) {
	s, store, _, conn := awardServer(t)
	ctx := t.Context()
	if _, err := store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "1", Sport: "cycling", Date: "2026-09-09", DurationSeconds: 1200, TrainingLoad: 50,
	}); err != nil {
		t.Fatal(err)
	}
	prev := analyzeRide
	t.Cleanup(func() { analyzeRide = prev })
	analyzeRide = func(rideanalysis.Input) rideanalysis.Analysis {
		return rideanalysis.Analysis{PowerCurve: map[int]float64{300: 317}, Load: 55}
	}
	// Without fitness_snapshots the recompute errors, while everything the
	// analysis itself needs still works.
	if _, err := conn.Exec(`DROP TABLE fitness_snapshots`); err != nil {
		t.Fatal(err)
	}

	_, err := s.analyseNewSessions(ctx, "wilant", workout.RiderProfile{Rider: "wilant"}, nil, GarminConsumer{}, garmin.Session{}, false, "", false)
	if err == nil {
		t.Fatal("the injected recompute failure did not surface, so this test proves nothing")
	}
	best, ok, _ := store.BestIn(ctx, "wilant", "power_300", "2026-09-01", "2026-09-30")
	if !ok || best.Award != "all_time" {
		t.Errorf("stored best = %+v %v, want the award written before the recompute failed", best, ok)
	}
}

// Imports never earn an award. The sync never hands one to awardNewEfforts (an
// imported ride is skipped before analysis), so this guard is defence in depth
// for a future caller, and it is kept and tested here rather than left silent.
func TestAnImportedRideInABatchIsNeverAwarded(t *testing.T) {
	s, store, _, _ := awardServer(t)
	ctx := t.Context()
	fresh, err := store.UpsertEfforts(ctx, "wilant", "import:1", "cycling", "2026-09-09", map[string]float64{"power_300": 400})
	if err != nil || len(fresh) != 1 {
		t.Fatal(fresh, err)
	}
	s.awardNewEfforts(ctx, "wilant", []newEfforts{{
		session: workout.CompletedSession{ID: "import:1", Provider: "import", Date: "2026-09-09", Sport: "cycling"},
		values:  map[string]float64{"power_300": 400},
	}})
	if pending, _ := store.UnannouncedAwards(ctx, "wilant"); len(pending) != 0 {
		t.Errorf("an imported ride was awarded: %+v", pending)
	}
	// The same ride from a provider is awarded, so the guard is what stopped it.
	if _, err := store.UpsertEfforts(ctx, "wilant", "garmin:1", "cycling", "2026-09-09", map[string]float64{"power_300": 410}); err != nil {
		t.Fatal(err)
	}
	s.awardNewEfforts(ctx, "wilant", []newEfforts{{
		session: workout.CompletedSession{ID: "garmin:1", Provider: "garmin", Date: "2026-09-09", Sport: "cycling"},
		values:  map[string]float64{"power_300": 410},
	}})
	if pending, _ := store.UnannouncedAwards(ctx, "wilant"); len(pending) != 1 {
		t.Errorf("a synced ride with the same value was not awarded: %+v", pending)
	}
}

// A failed read of the history is logged, not silent: the award decision for
// that metric is skipped and an operator can see why.
func TestAFailedScopeReadIsLoggedAtWarn(t *testing.T) {
	s, store, logs, conn := awardServer(t)
	ctx := t.Context()
	if _, err := store.UpsertEfforts(ctx, "wilant", "garmin:2", "cycling", "2026-09-09", map[string]float64{"power_300": 410}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`DROP TABLE best_efforts`); err != nil {
		t.Fatal(err)
	}
	s.awardNewEfforts(ctx, "wilant", []newEfforts{{
		session: workout.CompletedSession{ID: "garmin:2", Provider: "garmin", Date: "2026-09-09", Sport: "cycling"},
		values:  map[string]float64{"power_300": 410},
	}})
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "best efforts: reading") {
		t.Errorf("no Warn for the failed read:\n%s", logs.String())
	}
}
