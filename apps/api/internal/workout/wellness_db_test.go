package workout

import "testing"

// TestDailyWellnessEachEngine mirrors TestProgressionLevelsEachEngine's own
// SQLite-and-PostgreSQL structure (see progression_db_test.go) for the
// daily_wellness table, new in this change.
func TestDailyWellnessEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("wellness saves, round-trips, upserts, and lists owner-only ascending by date", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				err := db.SaveWellness(ctx, DailyWellness{
					Rider: "Wilant", Date: "2026-03-02",
					HRVLastNight: 48, HRVWeeklyAvg: 50, HRVStatus: "BALANCED",
					SleepSeconds: 25920, SleepScore: 82,
					ReadinessScore: 71, ReadinessLevel: "MODERATE", RestingHR: 48,
				})
				if err != nil {
					t.Fatalf("save wellness: %v", err)
				}
				if err := db.SaveWellness(ctx, DailyWellness{
					Rider: "wilant", Date: "2026-03-01",
					HRVLastNight: 45, HRVWeeklyAvg: 49, HRVStatus: "LOW",
					SleepSeconds: 20000, SleepScore: 60,
					ReadinessScore: 40, ReadinessLevel: "LOW", RestingHR: 52,
				}); err != nil {
					t.Fatalf("save second day: %v", err)
				}
				if err := db.SaveWellness(ctx, DailyWellness{
					Rider: "other", Date: "2026-03-02",
					HRVLastNight: 99, HRVWeeklyAvg: 99, HRVStatus: "BALANCED", RestingHR: 40,
				}); err != nil {
					t.Fatalf("save other rider's wellness: %v", err)
				}

				list, err := db.ListWellness(ctx, "WILANT", "")
				if err != nil {
					t.Fatalf("list wellness: %v", err)
				}
				if len(list) != 2 {
					t.Fatalf("list = %+v, want 2 rows for wilant only", list)
				}
				// Ascending by date: 03-01 before 03-02.
				if list[0].Date != "2026-03-01" || list[1].Date != "2026-03-02" {
					t.Errorf("dates = %q, %q, want ascending 03-01 then 03-02", list[0].Date, list[1].Date)
				}
				for _, w := range list {
					if w.Rider != "wilant" {
						t.Errorf("row %+v: rider not normalized", w)
					}
				}
				second := list[1]
				if second.HRVLastNight != 48 || second.HRVWeeklyAvg != 50 || second.HRVStatus != "BALANCED" {
					t.Errorf("HRV = %+v", second)
				}
				if second.SleepSeconds != 25920 || second.SleepScore != 82 {
					t.Errorf("sleep = %+v", second)
				}
				if second.ReadinessScore != 71 || second.ReadinessLevel != "MODERATE" {
					t.Errorf("readiness = %+v", second)
				}
				if second.RestingHR != 48 {
					t.Errorf("resting HR = %d, want 48", second.RestingHR)
				}
				if second.UpdatedAt == "" {
					t.Error("updated_at was not stamped")
				}

				// sinceDate filters out earlier rows.
				since, err := db.ListWellness(ctx, "wilant", "2026-03-02")
				if err != nil {
					t.Fatalf("list since: %v", err)
				}
				if len(since) != 1 || since[0].Date != "2026-03-02" {
					t.Fatalf("list since 2026-03-02 = %+v, want just that one day", since)
				}

				// Re-saving the same (rider, date) upserts rather than
				// accumulating a second row.
				if err := db.SaveWellness(ctx, DailyWellness{
					Rider: "wilant", Date: "2026-03-02",
					HRVLastNight: 52, HRVWeeklyAvg: 50, HRVStatus: "BALANCED",
					SleepSeconds: 26000, SleepScore: 85, ReadinessScore: 78, ReadinessLevel: "HIGH", RestingHR: 46,
				}); err != nil {
					t.Fatalf("re-save wellness: %v", err)
				}
				list, err = db.ListWellness(ctx, "wilant", "")
				if err != nil {
					t.Fatalf("list after upsert: %v", err)
				}
				if len(list) != 2 {
					t.Fatalf("list after upsert = %+v, want still 2 rows (upsert, not insert)", list)
				}
				for _, w := range list {
					if w.Date == "2026-03-02" && w.HRVLastNight != 52 {
						t.Errorf("HRV after upsert = %v, want 52", w.HRVLastNight)
					}
				}

				// A rider with no saved wellness gets an empty list, not an
				// error.
				none, err := db.ListWellness(ctx, "nobody", "")
				if err != nil || len(none) != 0 {
					t.Errorf("list for unknown rider = %+v, err %v, want empty", none, err)
				}
			})

			t.Run("saving wellness without a rider or date fails", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				if err := db.SaveWellness(ctx, DailyWellness{Date: "2026-03-02"}); err == nil {
					t.Error("expected an error for wellness with no rider")
				}
				if err := db.SaveWellness(ctx, DailyWellness{Rider: "wilant"}); err == nil {
					t.Error("expected an error for wellness with no date")
				}
			})
		})
	}
}
