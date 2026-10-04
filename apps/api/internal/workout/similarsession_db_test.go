package workout

import "testing"

func TestFindSimilarSessionEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			put := func(rider, provider, id, sport, date string) {
				t.Helper()
				if _, err := db.UpsertSession(ctx, UpsertSessionRequest{Rider: rider, Provider: provider, ExternalID: id,
					Sport: sport, Date: date, DurationSeconds: 3600}); err != nil {
					t.Fatal(err)
				}
			}
			put("wilant", "garmin", "1", "cycling", "2026-03-01")
			put("wilant", "wahoo", "2", "cycling", "2026-03-02")
			put("wilant", "garmin", "3", "cycling", "2026-03-03") // outside the asked dates
			put("wilant", "garmin", "4", "running", "2026-03-02") // another sport
			put("other", "garmin", "5", "cycling", "2026-03-02")  // another rider

			got, err := db.FindSimilarSession(ctx, "Wilant", "cycling", []string{"2026-03-01", "2026-03-02", "2026-02-28"})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].ID != "garmin:1" || got[1].ID != "wahoo:2" {
				t.Fatalf("got %+v, want garmin:1 then wahoo:2", got)
			}
			for _, s := range got {
				if s.Rider != "wilant" || s.Sport != "cycling" {
					t.Errorf("leaked %+v", s)
				}
			}

			none, err := db.FindSimilarSession(ctx, "wilant", "cycling", nil)
			if err != nil || len(none) != 0 {
				t.Errorf("no dates: %v %v, want an empty answer", none, err)
			}
		})
	}
}
