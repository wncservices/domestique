package api

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// keptSeeds are the registered tables a purge keeps (the crew's plan, the
// deployment's settings) but a rename must still follow, because they name a
// rider as owner or author.
func keptSeeds() []seed {
	return []seed{
		{"crews", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO crews (id, name, owner, created_at, updated_at) VALUES (?, 'Crew', ?, ?, ?)`, "crew-"+id, rider, ts, ts)
		}, "owner", byRider},
		{"crew_rides", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO crew_rides (id, crew_id, route_slug, date, created_by, created_at) VALUES (?, 'c', 'r', '2026-02-01', ?, ?)`, "ride-"+id, rider, ts)
		}, "created_by", byRider},
		{"ride_series", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO ride_series (id, crew_id, route_slug, interval_weeks, created_by, created_at) VALUES (?, 'c', 'r', 1, ?, ?)`, "series-"+id, rider, ts)
		}, "created_by", byRider},
		{"settings", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO settings (name, value, updated_by, updated_at) VALUES (?, ?, ?, ?)`, "s-"+id, []byte("v"), rider, ts)
		}, "updated_by", byRider},
		{"flags", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO flags (name, enabled, updated_by, updated_at) VALUES (?, ?, ?, ?)`, "f-"+id, true, rider, ts)
		}, "updated_by", byRider},
	}
}

// columnsOf is every column of table in the live schema.
func columnsOf(t *testing.T, env *riderDataEnv, table string) []string {
	t.Helper()
	var q string
	if env.d.Name == dbx.Postgres.Name {
		q = `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?`
	} else {
		q = `SELECT name FROM pragma_table_info(?)`
	}
	rows, err := env.db.Query(env.d.Rebind(q), table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return cols
}

// TestEveryRegisteredTableHasARenameRule is the guard for rename-rider, the
// way TestEveryRiderKeyedTableIsRegistered is for purge: it fails for a table
// whose rename rule is missing or does not name every rider column the real
// schema has, so a new table cannot be added and silently skipped (the bug
// this replaces: training tables were never renamed).
func TestEveryRegisteredTableHasARenameRule(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		for table, rt := range riderTables {
			r := rt.Rename
			kinds := 0
			if len(r.Columns) > 0 {
				kinds++
			}
			if r.Bespoke {
				kinds++
			}
			if r.Skip != "" {
				kinds++
			}
			if kinds != 1 {
				t.Errorf("%s: rename rule must set exactly one of Columns, Bespoke, Skip (has %d)", table, kinds)
				continue
			}

			var live []string
			for _, c := range columnsOf(t, env, table) {
				if slices.Contains(riderColumns, c) {
					live = append(live, c)
				}
			}
			sort.Strings(live)
			switch {
			case r.Skip != "":
				if len(live) > 0 {
					t.Errorf("%s: skipped for rename (%s) but has rider columns %v", table, r.Skip, live)
				}
			case r.Bespoke:
				// accounts/sync_state/provider_links: carried by RenameRider's
				// own code, covered by the end-to-end test below.
			default:
				declared := slices.Clone(r.Columns)
				sort.Strings(declared)
				if !slices.Equal(declared, live) {
					t.Errorf("%s: rename rule rewrites %v but the table has rider columns %v", table, declared, live)
				}
				for _, c := range r.With {
					if !slices.Contains(columnsOf(t, env, table), c) {
						t.Errorf("%s: unique-key column %q is not in the table", table, c)
					}
				}
				if len(r.With) > 0 && !r.Unique {
					t.Errorf("%s: With is set without Unique", table)
				}
			}
		}
	})
}

func allRenameSeeds() []seed { return append(riderSeeds(), keptSeeds()...) }

// A rename moves everything: every table for rider A ends up under B, nothing
// is left under A, and another rider's rows are untouched.
func TestRenameRiderMovesEveryTable(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b, other := "a"+tok, "b"+tok, "other"+tok
		aID, otherID := "a"+tok, "o"+tok

		seeds := allRenameSeeds()
		seeded := map[string]bool{}
		for _, s := range seeds {
			seeded[s.table] = true
			if err := s.insert(env, a, aID); err != nil {
				t.Fatalf("seeding %s for %s: %v", s.table, a, err)
			}
			if err := s.insert(env, other, otherID); err != nil {
				t.Fatalf("seeding %s for %s: %v", s.table, other, err)
			}
		}
		for table := range riderTables {
			if !seeded[table] {
				t.Errorf("%s is registered but this test has no seed for it", table)
			}
		}

		before := map[string]int{}
		otherBefore := map[string]int{}
		for _, s := range seeds {
			before[s.table] = count(t, env, s.table, s.probeColumn, s.probeValue(env, a, aID))
			otherBefore[s.table] = count(t, env, s.table, s.probeColumn, s.probeValue(env, other, otherID))
			if before[s.table] == 0 {
				t.Fatalf("%s: seed for the renamed rider did not land", s.table)
			}
		}

		sum, err := RenameRider(env.db, env.d, a, b, RenameOptions{})
		if err != nil {
			t.Fatalf("rename: %v", err)
		}
		if sum.Tables["workouts"] != 1 || sum.Tables["daily_wellness"] != 1 {
			t.Errorf("summary tables = %v, want the training tables counted", sum.Tables)
		}

		for _, s := range seeds {
			if riderTables[s.table].Rename.Skip != "" {
				if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, a, aID)); n != before[s.table] {
					t.Errorf("%s: %d rows after skipped rename, want unchanged count %d", s.table, n, before[s.table])
				}
				if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, other, otherID)); n != otherBefore[s.table] {
					t.Errorf("%s: skipped rename changed another rider's %d rows, want %d", s.table, n, otherBefore[s.table])
				}
				continue
			}
			idKeyed := slices.Contains(idColumns, s.probeColumn) && s.probeColumn != "account_id"
			if idKeyed {
				// Reached through an id the rider owns; the id does not change,
				// so the rows are still there and still hang off a workout, goal
				// or session that is now B's.
				if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, a, aID)); n != before[s.table] {
					t.Errorf("%s: %d rows reachable by id after the rename, want %d", s.table, n, before[s.table])
				}
			} else {
				if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, a, aID)); n != 0 {
					t.Errorf("%s: %d rows left under the old rider", s.table, n)
				}
				if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, b, aID)); n != before[s.table] {
					t.Errorf("%s: %d rows under the new rider, want %d", s.table, n, before[s.table])
				}
			}
			if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, other, otherID)); n != otherBefore[s.table] {
				t.Errorf("%s: the other rider has %d rows, want %d", s.table, n, otherBefore[s.table])
			}
		}

		// A rename run twice is a no-op, not an error: the retry after a
		// partial failure the docs promise.
		if _, err := RenameRider(env.db, env.d, a, b, RenameOptions{}); err != nil {
			t.Errorf("second run: %v", err)
		}
	})
}

// Every rider-name column a table has is rewritten, not just the first:
// crew_members names the rider and, separately, who decided their request.
func TestRenameRiderRewritesAttributionColumnsToo(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		if err := exec(env, `INSERT INTO crew_members (crew_id, rider, status, requested_at, decided_by) VALUES ('c1', 'someone', 'approved', ?, ?)`, ts, a); err != nil {
			t.Fatal(err)
		}
		if _, err := RenameRider(env.db, env.d, a, b, RenameOptions{}); err != nil {
			t.Fatal(err)
		}
		if n := count(t, env, "crew_members", "decided_by", b); n != 1 {
			t.Errorf("decided_by under the new name: %d rows, want 1", n)
		}
		if n := count(t, env, "crew_members", "decided_by", a); n != 0 {
			t.Errorf("decided_by under the old name: %d rows, want 0", n)
		}
	})
}

// If the new name already has data where the rider is part of a unique key,
// there is no merge to be had: refuse, and change nothing anywhere, tables
// before and after the colliding one included.
func TestRenameRiderRefusesACollisionAndChangesNothing(t *testing.T) {
	collisions := []struct {
		table string
		seed  func(e *riderDataEnv, a, b string) error
	}{
		{"rider_profiles", func(e *riderDataEnv, a, b string) error {
			return exec(e, `INSERT INTO rider_profiles (rider, updated_at) VALUES (?, ?)`, b, ts)
		}},
		{"weather_locations", func(e *riderDataEnv, a, b string) error {
			return exec(e, `INSERT INTO weather_locations (rider, place, lat, lon, updated_at) VALUES (?, 'Ghent', 51.05, 3.72, ?)`, b, ts)
		}},
		{"daily_wellness", func(e *riderDataEnv, a, b string) error {
			// Same date as the seeded one for the old rider: a real collision.
			return exec(e, `INSERT INTO daily_wellness (rider, date, updated_at) VALUES (?, '2026-01-02', ?)`, b, ts)
		}},
		{"progression_levels", func(e *riderDataEnv, a, b string) error {
			return exec(e, `INSERT INTO progression_levels (rider, sport, zone, level, updated_at) VALUES (?, 'cycling', 'threshold', 2, ?)`, b, ts)
		}},
		{"crew_members", func(e *riderDataEnv, a, b string) error {
			return exec(e, `INSERT INTO crew_members (crew_id, rider, status, requested_at) VALUES (?, ?, 'approved', ?)`, "crew-"+a, b, ts)
		}},
	}
	for _, c := range collisions {
		t.Run(c.table, func(t *testing.T) {
			eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
				tok := fmt.Sprintf("%d", time.Now().UnixNano())
				a, b := "a"+tok, "b"+tok
				seeds := allRenameSeeds()
				for _, s := range seeds {
					if err := s.insert(env, a, "a"+tok); err != nil {
						t.Fatalf("seeding %s: %v", s.table, err)
					}
				}
				if err := c.seed(env, a, b); err != nil {
					t.Fatalf("seeding the collision: %v", err)
				}

				_, err := RenameRider(env.db, env.d, a, b, RenameOptions{})
				if err == nil {
					t.Fatal("rename onto a colliding rider succeeded")
				}
				if !strings.Contains(err.Error(), c.table) || !strings.Contains(err.Error(), "nothing was changed") {
					t.Errorf("error = %q, want it to name %s and say nothing changed", err, c.table)
				}
				for _, s := range seeds {
					if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, a, "a"+tok)); n == 0 {
						t.Errorf("%s: the old rider lost rows although the rename was refused", s.table)
					}
					if s.table != c.table && !slices.Contains(idColumns, s.probeColumn) {
						if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, b, "a"+tok)); n != 0 {
							t.Errorf("%s: %d rows reached the new rider although the rename was refused", s.table, n)
						}
					}
				}
			})
		})
	}
}

// Two rows only collide when the whole unique key does: the new rider having
// a profile for another table, or a wellness row for a different date, is
// fine and must not block the move.
func TestRenameRiderAllowsNeighbouringKeysThatDoNotCollide(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		if err := exec(env, `INSERT INTO daily_wellness (rider, date, updated_at) VALUES (?, '2026-01-02', ?)`, a, ts); err != nil {
			t.Fatal(err)
		}
		if err := exec(env, `INSERT INTO daily_wellness (rider, date, updated_at) VALUES (?, '2026-01-03', ?)`, b, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := RenameRider(env.db, env.d, a, b, RenameOptions{}); err != nil {
			t.Fatalf("rename blocked by a non-colliding row: %v", err)
		}
		if n := count(t, env, "daily_wellness", "rider", b); n != 2 {
			t.Errorf("daily_wellness under the new rider: %d, want 2", n)
		}
	})
}

// A dry run reports what a real run would and writes nothing.
func TestRenameRiderDryRunWritesNothing(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		for _, s := range riderSeeds() {
			if err := s.insert(env, a, "a"+tok); err != nil {
				t.Fatal(err)
			}
		}
		sum, err := RenameRider(env.db, env.d, a, b, RenameOptions{DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if sum.Tables["goals"] != 1 {
			t.Errorf("dry-run summary = %v, want it to count the goal", sum.Tables)
		}
		if n := count(t, env, "goals", "rider", b); n != 0 {
			t.Error("a dry run wrote")
		}
		if n := count(t, env, "goals", "rider", a); n != 1 {
			t.Error("a dry run removed the old rider's rows")
		}
	})
}
