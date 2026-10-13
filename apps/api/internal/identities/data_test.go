package identities

import (
	"context"
	"testing"
	"time"
)

func TestRidersWithDataFindsEveryRiderAndTheirEarliestRow(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		db := e.db.Conn()
		for _, q := range []string{
			`CREATE TABLE IF NOT EXISTS idtest_dated (owner TEXT, created_at TEXT, updated_at TEXT)`,
			`CREATE TABLE IF NOT EXISTS idtest_upd (rider TEXT, updated_at TEXT)`,
			`DELETE FROM idtest_dated`, `DELETE FROM idtest_upd`,
			`INSERT INTO idtest_dated VALUES ('Wilant', '2026-03-01T10:00:00Z', '2026-09-01T10:00:00Z')`,
			`INSERT INTO idtest_dated VALUES ('wilant', '2026-02-01T10:00:00Z', '2026-09-01T10:00:00Z')`,
			`INSERT INTO idtest_dated VALUES ('', '2020-01-01T00:00:00Z', '')`,
			`INSERT INTO idtest_upd VALUES ('wilant', '2026-01-15T10:00:00Z')`,
			`INSERT INTO idtest_upd VALUES ('sam', '2026-05-05T10:00:00Z')`,
			`INSERT INTO idtest_things (owner) VALUES ('robin')`, // no time column at all
			`INSERT INTO idtest_upd VALUES ('pat', 'not a time')`,
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		got, err := e.store.RidersWithData(ctx, TakenColumns{
			"idtest_dated":  {"owner"},
			"idtest_upd":    {"rider"},
			"idtest_things": {"owner"},
			"no_such_table": {"rider"},
		})
		if err != nil {
			t.Fatal(err)
		}
		at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
		want := map[string]time.Time{
			"wilant": at("2026-01-15T10:00:00Z"), // earliest across tables and case variants; created_at preferred over updated_at per table
			"sam":    at("2026-05-05T10:00:00Z"),
			"robin":  {}, // has data, no usable time
			"pat":    {}, // time column unparseable: still has data
		}
		if len(got) != len(want) {
			t.Fatalf("riders = %v, want %v", got, want)
		}
		for rider, w := range want {
			if g, ok := got[rider]; !ok || !g.Equal(w) {
				t.Errorf("%s: earliest = %v (present=%v), want %v", rider, g, ok, w)
			}
		}
	})
}
