package workout

import "testing"

func TestSnoozeCreatesTheProfileRowForARiderWithoutOne(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			if err := db.SnoozeFTPTest(ctx, "Wilant", "2026-04-12"); err != nil {
				t.Fatal(err)
			}
			p, ok, err := db.GetProfile(ctx, "wilant")
			if err != nil || !ok {
				t.Fatalf("profile after snooze: ok=%v err=%v", ok, err)
			}
			if p.FTPTestSnoozedUntil != "2026-04-12" || p.FTPWatts != 0 {
				t.Errorf("profile = %+v", p)
			}
			// A second snooze replaces the date.
			if err := db.SnoozeFTPTest(ctx, "wilant", "2026-05-01"); err != nil {
				t.Fatal(err)
			}
			p, _, _ = db.GetProfile(ctx, "wilant")
			if p.FTPTestSnoozedUntil != "2026-05-01" {
				t.Errorf("snoozed until %q, want 2026-05-01", p.FTPTestSnoozedUntil)
			}
		})
	}
}
