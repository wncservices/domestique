package workout

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

// effortsOf is a stand-in for bestefforts.Efforts, which this package cannot
// import (it imports this one): the storage only needs a function.
func effortsOf(a SessionAnalysis, sport string) map[string]float64 {
	out := map[string]float64{}
	if sport == "cycling" {
		if v := a.PowerCurve["300"]; v > 0 {
			out["power_300"] = v
		}
		if v := a.PowerCurve["1200"]; v > 0 {
			out["power_1200"] = v
		}
	}
	if a.MaxHR > 0 {
		out["max_hr"] = float64(a.MaxHR)
	}
	return out
}

func eachEffortEngine(t *testing.T, f func(t *testing.T, db *DB)) {
	t.Helper()
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) { f(t, open(t)) })
	}
}

func keys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestUpsertEffortsWritesOneRowPerMetricAndKeepsTheAward(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		first := map[string]float64{"power_300": 310, "power_1200": 280, "max_hr": 181}
		fresh, err := db.UpsertEfforts(ctx, "wilant", "garmin:1", "cycling", "2026-05-02", first)
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(fresh)
		if !reflect.DeepEqual(fresh, keys(first)) {
			t.Errorf("new rows = %v, want all three", fresh)
		}
		if err := db.SetEffortAward(ctx, "garmin:1", "power_300", "season", 0); err != nil {
			t.Fatal(err)
		}

		// A re-analysis: values and date move, the award stays, and nothing is new.
		fresh, err = db.UpsertEfforts(ctx, "wilant", "garmin:1", "cycling", "2026-05-03", map[string]float64{"power_300": 315, "power_1200": 280, "max_hr": 181})
		if err != nil {
			t.Fatal(err)
		}
		if len(fresh) != 0 {
			t.Errorf("a re-run reported new rows %v", fresh)
		}
		got, ok, err := db.BestIn(ctx, "wilant", "power_300", "2026-01-01", "2026-12-31")
		if err != nil || !ok {
			t.Fatalf("best: %v %v", ok, err)
		}
		if got.Value != 315 || got.Date != "2026-05-03" || got.SessionID != "garmin:1" || got.Award != "season" || got.Sport != "cycling" {
			t.Errorf("after re-analysis = %+v, want 315 on the 3rd and the season award kept", got)
		}
	})
}

func TestUpsertEffortsDropsAMetricTheRideNoLongerHasUnlessItWasAwarded(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		if _, err := db.UpsertEfforts(ctx, "wilant", "garmin:1", "cycling", "2026-05-02", map[string]float64{"power_300": 310, "power_1200": 280, "max_hr": 181}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetEffortAward(ctx, "garmin:1", "power_1200", "all_time", 0); err != nil {
			t.Fatal(err)
		}
		// The re-analysis has only max HR: power_300 goes, power_1200 stays
		// because an award is a record of what happened and is never removed.
		if _, err := db.UpsertEfforts(ctx, "wilant", "garmin:1", "cycling", "2026-05-02", map[string]float64{"max_hr": 181}); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := db.BestIn(ctx, "wilant", "power_300", "2026-01-01", "2026-12-31"); ok {
			t.Error("an unawarded stale metric survived a re-analysis")
		}
		if got, ok, _ := db.BestIn(ctx, "wilant", "power_1200", "2026-01-01", "2026-12-31"); !ok || got.Award != "all_time" {
			t.Errorf("an awarded metric was removed: %+v %v", got, ok)
		}
	})
}

func TestBestInIsScopedByRiderMetricAndDatesAndTiesGoToTheEarliest(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		put := func(rider, session, date string, w float64) {
			t.Helper()
			if _, err := db.UpsertEfforts(ctx, rider, session, "cycling", date, map[string]float64{"power_300": w}); err != nil {
				t.Fatal(err)
			}
		}
		put("wilant", "a", "2026-03-10", 300)
		put("wilant", "b", "2026-02-01", 300) // a tie, earlier
		put("wilant", "c", "2025-12-31", 400) // last year
		put("wilant", "d", "2026-06-01", 290)
		put("someone", "e", "2026-04-01", 500) // another rider

		got, ok, err := db.BestIn(ctx, "wilant", "power_300", "2026-01-01", "2026-12-31")
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if got.SessionID != "b" || got.Value != 300 {
			t.Errorf("season best = %+v, want the earlier of the two 300 W rides", got)
		}
		all, _, _ := db.BestIn(ctx, "wilant", "power_300", "", "")
		if all.SessionID != "c" {
			t.Errorf("all-time best = %+v, want last year's 400 W (no bounds)", all)
		}
		if _, ok, _ := db.BestIn(ctx, "wilant", "power_300", "2024-01-01", "2024-12-31"); ok {
			t.Error("a year with no rides reported a best")
		}
		// Both boundary days are inside.
		edge, ok, _ := db.BestIn(ctx, "wilant", "power_300", "2025-12-31", "2025-12-31")
		if !ok || edge.SessionID != "c" {
			t.Errorf("a one-day scope on the boundary = %+v %v", edge, ok)
		}
	})
}

func TestScopeValuesExcludesTheRideItself(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		for i, d := range []string{"2026-01-05", "2026-02-05", "2026-03-05", "2025-03-05"} {
			session := string(rune('a' + i))
			if _, err := db.UpsertEfforts(ctx, "wilant", session, "cycling", d, map[string]float64{"power_300": float64(200 + 10*i)}); err != nil {
				t.Fatal(err)
			}
		}
		season, err := db.ScopeValues(ctx, "wilant", "power_300", "2026-01-01", "2026-12-31", "c")
		if err != nil {
			t.Fatal(err)
		}
		sort.Float64s(season)
		if !reflect.DeepEqual(season, []float64{200, 210}) {
			t.Errorf("season values = %v, want the two other 2026 rides", season)
		}
		all, _ := db.ScopeValues(ctx, "wilant", "power_300", "", "", "c")
		if len(all) != 3 {
			t.Errorf("all-time values = %v, want three (everything but the ride itself)", all)
		}
	})
}

func TestDeleteEffortsForSessionRemovesItsRowsOnly(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		for _, s := range []string{"a", "b"} {
			if _, err := db.UpsertEfforts(ctx, "wilant", s, "cycling", "2026-01-05", map[string]float64{"power_300": 250, "max_hr": 170}); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.DeleteEffortsForSession(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		vals, _ := db.ScopeValues(ctx, "wilant", "power_300", "", "", "")
		if len(vals) != 1 {
			t.Errorf("after deleting one session %d power rows remain, want 1", len(vals))
		}
	})
}

func TestScopeStatsAreTheMaxAndCountWithoutTheRideItself(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		for i, d := range []string{"2026-01-05", "2026-02-05", "2026-03-05", "2025-03-05"} {
			session := string(rune('a' + i))
			if _, err := db.UpsertEfforts(ctx, "wilant", session, "cycling", d, map[string]float64{"power_300": float64(200 + 10*i)}); err != nil {
				t.Fatal(err)
			}
		}
		// Another rider and another metric must not count.
		if _, err := db.UpsertEfforts(ctx, "other", "z", "cycling", "2026-01-05", map[string]float64{"power_300": 999, "max_hr": 190}); err != nil {
			t.Fatal(err)
		}

		season, err := db.ScopeStats(ctx, "wilant", "power_300", "2026-01-01", "2026-12-31", "c")
		if err != nil {
			t.Fatal(err)
		}
		if season.Count != 2 || season.Max != 210 {
			t.Errorf("season = %+v, want 2 values, max 210 (the ride c itself excluded)", season)
		}
		all, _ := db.ScopeStats(ctx, "wilant", "power_300", "", "", "c")
		if all.Count != 3 || all.Max != 230 {
			t.Errorf("all-time = %+v, want 3 values, max 230", all)
		}
		none, err := db.ScopeStats(ctx, "wilant", "power_300", "2030-01-01", "2030-12-31", "")
		if err != nil || none.Count != 0 || none.Max != 0 {
			t.Errorf("an empty scope = %+v, %v, want zero values and no error", none, err)
		}
	})
}

func seedAnalysed(t *testing.T, db *DB, rider, ext, date string, curve map[string]float64, maxHR int) string {
	t.Helper()
	ctx := t.Context()
	s, err := db.UpsertSession(ctx, UpsertSessionRequest{Rider: rider, Provider: "garmin", ExternalID: ext, Sport: "cycling", Date: date, DurationSeconds: 3600, TrainingLoad: 50})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveAnalysis(ctx, SessionAnalysis{SessionID: s.ID, Rider: rider, PowerCurve: curve, MaxHR: maxHR}); err != nil {
		t.Fatal(err)
	}
	return s.ID
}

func TestRebuildRecreatesEveryRowWithNoAwardAndRunsOnlyOnAnEmptyTable(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		a := seedAnalysed(t, db, "wilant", "1", "2026-04-01", map[string]float64{"300": 300, "1200": 260}, 180)
		seedAnalysed(t, db, "wilant", "2", "2026-04-08", map[string]float64{"300": 320}, 0)
		seedAnalysed(t, db, "jonas", "3", "2026-04-09", map[string]float64{"300": 400}, 175)

		n, err := db.RebuildBestEfforts(ctx, effortsOf, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n != 6 {
			t.Errorf("rebuilt %d rows, want 6 (3 + 1 + 2)", n)
		}
		got, ok, _ := db.BestIn(ctx, "wilant", "power_300", "", "")
		if !ok || got.Value != 320 || got.Award != "" || got.Date != "2026-04-08" {
			t.Errorf("rebuilt best = %+v: it has to come from the analysis and the session's date, with no award", got)
		}
		if other, _, _ := db.BestIn(ctx, "jonas", "power_300", "", ""); other.Value != 400 {
			t.Errorf("another rider's row = %+v", other)
		}

		// A rebuild is idempotent at every start: a second run adds what is
		// missing (a new analysis here) and leaves what is there alone.
		seedAnalysed(t, db, "wilant", "4", "2026-04-10", map[string]float64{"300": 500}, 0)
		n, err = db.RebuildBestEfforts(ctx, effortsOf, nil)
		if err != nil || n != 1 {
			t.Errorf("second rebuild = %d, %v; want 1 (only the new analysis)", n, err)
		}
		if got, _, _ := db.BestIn(ctx, "wilant", "power_300", "", ""); got.Value != 500 {
			t.Errorf("the new analysis was not picked up: best = %v", got.Value)
		}
		if n, _ = db.RebuildBestEfforts(ctx, effortsOf, nil); n != 0 {
			t.Errorf("third rebuild wrote %d rows, want 0", n)
		}
		_ = a
	})
}

func TestBackfillFillsOnlyTheRidersMissingSessionsAndKeepsWhatIsThere(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		have := seedAnalysed(t, db, "wilant", "1", "2026-04-01", map[string]float64{"300": 300}, 0)
		if _, err := db.UpsertEfforts(ctx, "wilant", have, "cycling", "2026-04-01", map[string]float64{"power_300": 300}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetEffortAward(ctx, have, "power_300", "season", 0); err != nil {
			t.Fatal(err)
		}
		seedAnalysed(t, db, "wilant", "2", "2026-04-08", map[string]float64{"300": 320}, 0)
		seedAnalysed(t, db, "jonas", "3", "2026-04-09", map[string]float64{"300": 400}, 0)

		n, err := db.BackfillEfforts(ctx, "wilant", effortsOf)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("backfilled %d rows, want 1 (only the session with none)", n)
		}
		if _, ok, _ := db.BestIn(ctx, "jonas", "power_300", "", ""); ok {
			t.Error("a backfill for one rider wrote another's rows")
		}
		if got, _, _ := db.BestIn(ctx, "wilant", "power_300", "2026-04-01", "2026-04-01"); got.Award != "season" {
			t.Errorf("a backfill touched an existing row: %+v", got)
		}
		n, _ = db.BackfillEfforts(ctx, "wilant", effortsOf)
		if n != 0 {
			t.Errorf("a second backfill wrote %d rows, want 0", n)
		}
	})
}

func TestUnannouncedAwardsAreListedOnceAndMarkedAnnounced(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		if _, err := db.UpsertEfforts(ctx, "wilant", "a", "cycling", "2026-05-01", map[string]float64{"power_300": 300, "max_hr": 180}); err != nil {
			t.Fatal(err)
		}
		// A rebuilt or imported row has no award and is never announced.
		if got, _ := db.UnannouncedAwards(ctx, "wilant"); len(got) != 0 {
			t.Fatalf("unawarded rows listed: %+v", got)
		}
		if err := db.SetEffortAward(ctx, "a", "power_300", "all_time", 0); err != nil {
			t.Fatal(err)
		}
		got, err := db.UnannouncedAwards(ctx, "wilant")
		if err != nil || len(got) != 1 || got[0].Metric != "power_300" || got[0].Award != "all_time" {
			t.Fatalf("pending = %+v %v", got, err)
		}
		if other, _ := db.UnannouncedAwards(ctx, "jonas"); len(other) != 0 {
			t.Errorf("another rider's awards listed: %+v", other)
		}
		if err := db.MarkAwardsAnnounced(ctx, "wilant", got); err != nil {
			t.Fatal(err)
		}
		if again, _ := db.UnannouncedAwards(ctx, "wilant"); len(again) != 0 {
			t.Errorf("an announced award came back: %+v", again)
		}
		// Marking another rider's award does nothing: the rider is part of the key.
		if err := db.SetEffortAward(ctx, "a", "max_hr", "season", 0); err != nil {
			t.Fatal(err)
		}
		pending, _ := db.UnannouncedAwards(ctx, "wilant")
		if err := db.MarkAwardsAnnounced(ctx, "jonas", pending); err != nil {
			t.Fatal(err)
		}
		if still, _ := db.UnannouncedAwards(ctx, "wilant"); len(still) != 1 {
			t.Errorf("another rider announced %d awards for wilant", 1-len(still))
		}
	})
}

func TestBestEffortsMigrationRunsTwiceAndKeepsRows(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "twice.db")
	src, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	first, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.UpsertEfforts(context.Background(), "wilant", "a", "cycling", "2026-05-01", map[string]float64{"power_300": 300}); err != nil {
		t.Fatal(err)
	}
	second, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if _, ok, _ := second.BestIn(context.Background(), "wilant", "power_300", "", ""); !ok {
		t.Error("the second migration lost a row")
	}
}

func TestBestEffortsStampFromTheStoreClock(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		db.Clock = func() time.Time { return time.Date(2031, time.March, 18, 9, 30, 0, 0, time.UTC) }
		ctx := t.Context()
		if _, err := db.UpsertEfforts(ctx, "wilant", "a", "cycling", "2031-03-17", map[string]float64{"power_300": 300}); err != nil {
			t.Fatal(err)
		}
		got, _, _ := db.BestIn(ctx, "wilant", "power_300", "", "")
		if got.CreatedAt != "2031-03-18T09:30:00Z" {
			t.Errorf("created_at = %q, want the store clock", got.CreatedAt)
		}
	})
}

func TestAHalfDoneRebuildCompletesOnTheNextStartInChunks(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		for i := 1; i <= 7; i++ {
			seedAnalysed(t, db, "wilant", string(rune('a'+i)), "2026-04-0"+string(rune('0'+i)), map[string]float64{"300": float64(300 + i)}, 170+i)
		}
		var calls []int
		db.EffortChunk = 3
		n, err := db.RebuildBestEfforts(ctx, effortsOf, func(written int) { calls = append(calls, written) })
		if err != nil || n != 14 {
			t.Fatalf("rebuild = %d, %v; want 14 rows", n, err)
		}
		if len(calls) < 4 || calls[len(calls)-1] != 14 {
			t.Errorf("progress = %v, want several chunks of 3 ending at 14", calls)
		}
		// A crash part-way: some sessions have rows and some do not.
		if _, err := db.db.Exec(db.query(`DELETE FROM best_efforts WHERE session_id IN (?, ?, ?)`), "garmin:c", "garmin:e", "garmin:h"); err != nil {
			t.Fatal(err)
		}
		n, err = db.RebuildBestEfforts(ctx, effortsOf, nil)
		if err != nil || n != 6 {
			t.Errorf("completing rebuild = %d, %v; want the 6 missing rows", n, err)
		}
	})
}
