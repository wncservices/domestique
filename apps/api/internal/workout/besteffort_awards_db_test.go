package workout

import "testing"

func TestListAwardsReturnsOnlyAwardedRowsInRangeForTheRider(t *testing.T) {
	eachEffortEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		put := func(rider, session, date string) {
			t.Helper()
			if _, err := db.UpsertEfforts(ctx, rider, session, "cycling", date, map[string]float64{"power_300": 300, "max_hr": 180}); err != nil {
				t.Fatal(err)
			}
		}
		put("wilant", "a", "2025-12-31")
		put("wilant", "b", "2026-01-01")
		put("wilant", "c", "2026-06-01")
		put("jonas", "d", "2026-06-01")
		for _, s := range []string{"a", "b", "d"} {
			if err := db.SetEffortAward(ctx, s, "power_300", "season", 250); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.SetEffortAward(ctx, "b", "max_hr", "all_time", 170); err != nil {
			t.Fatal(err)
		}

		got, err := db.ListAwards(ctx, "wilant", "2026-01-01", "2026-12-31")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].SessionID != "b" || got[0].Metric != "max_hr" || got[1].Metric != "power_300" || got[0].Previous != 170 {
			t.Errorf("awards = %+v, want b's two awards (the unawarded ride, last year's and jonas's are out)", got)
		}
		all, _ := db.ListAwards(ctx, "wilant", "", "")
		if len(all) != 3 {
			t.Errorf("open range = %d awards, want 3", len(all))
		}
	})
}
