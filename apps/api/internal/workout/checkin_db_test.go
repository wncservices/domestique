package workout

import "testing"

func TestCheckInRoundTripUpsertListDelete(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		if _, ok, err := db.GetCheckIn(ctx, "wilant", "2026-10-18"); err != nil || ok {
			t.Fatalf("empty get = %v, %v; want absent", ok, err)
		}
		if err := db.SaveCheckIn(ctx, CheckIn{Rider: "Wilant", Date: "2026-10-18", Legs: 3, Mood: 4, SleepQuality: 2, Note: "tired"}); err != nil {
			t.Fatal(err)
		}
		got, ok, err := db.GetCheckIn(ctx, "wilant", "2026-10-18")
		if err != nil || !ok {
			t.Fatalf("get = %v, %v", ok, err)
		}
		if got.Legs != 3 || got.Mood != 4 || got.SleepQuality != 2 || got.Note != "tired" || got.UpdatedAt == "" {
			t.Fatalf("got %+v", got)
		}
		if err := db.SaveCheckIn(ctx, CheckIn{Rider: "wilant", Date: "2026-10-18", Legs: 1}); err != nil {
			t.Fatal(err)
		}
		got, _, _ = db.GetCheckIn(ctx, "wilant", "2026-10-18")
		if got.Legs != 1 || got.Mood != 0 || got.Note != "" {
			t.Fatalf("upsert did not replace: %+v", got)
		}
		var created string
		if err := db.db.QueryRowContext(ctx, db.query(`SELECT created_at FROM daily_checkins WHERE rider = ?`), "wilant").Scan(&created); err != nil || created == "" {
			t.Fatalf("created_at = %q, %v", created, err)
		}
		for _, d := range []string{"2026-10-10", "2026-10-12"} {
			if err := db.SaveCheckIn(ctx, CheckIn{Rider: "wilant", Date: d, Legs: 4}); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.SaveCheckIn(ctx, CheckIn{Rider: "other", Date: "2026-10-18", Legs: 4}); err != nil {
			t.Fatal(err)
		}
		list, err := db.ListCheckIns(ctx, "wilant", "2026-10-11")
		if err != nil || len(list) != 2 || list[0].Date != "2026-10-12" || list[1].Date != "2026-10-18" {
			t.Fatalf("list = %+v, %v; want 12th and 18th ascending", list, err)
		}
		if _, err := db.DeleteRider(ctx, "wilant"); err != nil {
			t.Fatal(err)
		}
		if list, _ := db.ListCheckIns(ctx, "wilant", ""); len(list) != 0 {
			t.Fatalf("after delete: %+v", list)
		}
		if _, ok, _ := db.GetCheckIn(ctx, "other", "2026-10-18"); !ok {
			t.Fatal("another rider's check-in was deleted")
		}
	})
}
