package api

import (
	"context"
	"testing"
)

// Merging two riders keeps the better best effort per metric, whichever rider
// held it. best_efforts rows are one per ride per metric, so there is no
// "current record" row for the target's to win over: moving every row is the
// whole rule, and the best is whatever BestIn finds across them. A bespoke
// rule that kept the target's row where both had one would throw the source's
// better 20 minutes away.
func TestMergeRiderKeepsTheBetterBestEffortPerMetric(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		ctx := context.Background()
		tok := mergeTok(t)
		from, into := "from"+tok, "into"+tok
		put := func(rider, session, date string, efforts map[string]float64) {
			t.Helper()
			if _, err := env.srv.Training.UpsertEfforts(ctx, rider, session+tok, "cycling", date, efforts); err != nil {
				t.Fatal(err)
			}
		}
		put(from, "f1", "2025-06-01", map[string]float64{"power_1200": 310, "power_300": 340})
		put(from, "f2", "2026-02-01", map[string]float64{"power_1200": 280})
		put(into, "i1", "2026-03-01", map[string]float64{"power_1200": 290, "power_300": 380})
		if err := env.srv.Training.SetEffortAward(ctx, "f1"+tok, "power_1200", "all_time", 0); err != nil {
			t.Fatal(err)
		}

		sum, err := MergeRider(ctx, env.db, env.d, from, into, MergeOptions{})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if c := sum.Tables["best_efforts"]; c.Moved != 3 || c.DroppedSource != 0 {
			t.Errorf("best_efforts summary = %+v, want the source's 3 rows moved and none dropped", c)
		}

		best, ok, err := env.srv.Training.BestIn(ctx, into, "power_1200", "", "")
		if err != nil || !ok {
			t.Fatalf("best 20 min: %v %v", ok, err)
		}
		if best.Value != 310 || best.SessionID != "f1"+tok || best.Award != "all_time" {
			t.Errorf("best 20 min after the merge = %+v, want the source rider's 310 W with its award", best)
		}
		best, _, _ = env.srv.Training.BestIn(ctx, into, "power_300", "", "")
		if best.Value != 380 {
			t.Errorf("best 5 min after the merge = %v, want the target's own 380 W", best.Value)
		}
		// The season view of the same data: 2026 holds one ride from each rider.
		season, _, _ := env.srv.Training.BestIn(ctx, into, "power_1200", "2026-01-01", "2026-12-31")
		if season.Value != 290 {
			t.Errorf("2026 best 20 min = %v, want 290 W across both riders' 2026 rides", season.Value)
		}
		if n := count(t, env, "best_efforts", "rider", from); n != 0 {
			t.Errorf("%d rows left under the merged-away rider", n)
		}
	})
}
