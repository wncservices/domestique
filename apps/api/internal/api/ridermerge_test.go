package api

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestEveryRegisteredTableHasAMergeRule is the guard for merge-rider, the way
// TestEveryRegisteredTableHasARenameRule is for rename-rider: a merge walks
// riderTables, so a table whose rename rule is one the merge cannot follow
// (a bespoke table it has no code for) would silently stay behind under the
// rider being merged away.
func TestEveryRegisteredTableHasAMergeRule(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		for table, rt := range riderTables {
			r := rt.Rename
			switch {
			case len(r.Columns) > 0:
				if r.Bespoke || r.Skip != "" {
					t.Errorf("%s: a rename rule must set exactly one of Columns, Bespoke, Skip", table)
				}
				if r.Unique {
					// The merge keeps the target's row on a collision, which needs
					// the whole key: the rider column and the rest of it.
					have := columnsOf(t, env, table)
					if !slices.Contains(have, r.Columns[0]) {
						t.Errorf("%s: unique key column %q is not in the table", table, r.Columns[0])
					}
					for _, c := range r.With {
						if !slices.Contains(have, c) {
							t.Errorf("%s: unique-key column %q is not in the table", table, c)
						}
					}
				}
			case r.Bespoke:
				if !mergeBespoke[table] {
					t.Errorf("%s: renamed by bespoke code, and ridermerge.go has none for it: add a merge rule there and list it in mergeBespoke", table)
				}
			case r.Skip != "":
				// Reached through an id a merge does not change, or ended by
				// the caller (sessions).
			default:
				t.Errorf("%s: no rename rule, so no merge rule", table)
			}
		}
		for table := range mergeBespoke {
			if rt, ok := riderTables[table]; !ok || !rt.Rename.Bespoke {
				t.Errorf("mergeBespoke lists %q, which is not a registered bespoke table", table)
			}
		}
	})
}

// uniqueTables are the registered tables whose key includes the rider, so the
// target's own rows can collide with the ones being merged in.
func collidingSeeds() []seed {
	var out []seed
	for _, s := range allRenameSeeds() {
		rt := riderTables[s.table]
		if rt.Rename.Unique || s.table == "accounts" || s.table == "sync_state" || s.table == "provider_links" {
			out = append(out, s)
		}
	}
	return out
}

// insertForTarget seeds the target's row on the same key as the source's. Some
// tables also have a unique column of their own that the id feeds (a hashed
// token), which the same id cannot satisfy twice: for those a second id still
// collides on the rider key, which is what is being tested.
func insertForTarget(t *testing.T, env *riderDataEnv, s seed, rider, id string) {
	t.Helper()
	if err := s.insert(env, rider, id); err != nil {
		if err2 := s.insert(env, rider, id+"-t"); err2 != nil {
			t.Fatalf("seeding %s for the target: %v (and with another id: %v)", s.table, err, err2)
		}
	}
}

func probeCount(t *testing.T, env *riderDataEnv, s seed, rider, id string) int {
	t.Helper()
	return count(t, env, s.table, s.probeColumn, s.probeValue(env, rider, id))
}

// Nothing overlaps: every table's rows for the rider being merged end up under
// the target, nothing is left behind, and another rider is untouched.
func TestMergeRiderMovesEverythingWhenNothingOverlaps(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b, other := "a"+tok, "b"+tok, "other"+tok
		aID, otherID := "a"+tok, "o"+tok

		seeds := allRenameSeeds()
		for _, s := range seeds {
			if err := s.insert(env, a, aID); err != nil {
				t.Fatalf("seeding %s for %s: %v", s.table, a, err)
			}
			if err := s.insert(env, other, otherID); err != nil {
				t.Fatalf("seeding %s for %s: %v", s.table, other, err)
			}
		}
		before, otherBefore := map[string]int{}, map[string]int{}
		for _, s := range seeds {
			before[s.table] = probeCount(t, env, s, a, aID)
			otherBefore[s.table] = probeCount(t, env, s, other, otherID)
			if before[s.table] == 0 {
				t.Fatalf("%s: the seed did not land", s.table)
			}
		}

		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		for table, c := range sum.Tables {
			if c.KeptTarget != 0 || c.DroppedSource != 0 {
				t.Errorf("%s: kept-target %d, dropped-source %d, want none when nothing overlaps", table, c.KeptTarget, c.DroppedSource)
			}
		}
		if sum.Tables["goals"].Moved != 1 || sum.Tables["routes"].Moved != 1 || sum.Tables["accounts"].Moved != 1 {
			t.Errorf("summary = %+v, want goals, routes and accounts counted as moved", sum.Tables)
		}

		for _, s := range seeds {
			rule := riderTables[s.table].Rename
			switch {
			case rule.Skip != "":
				if n := probeCount(t, env, s, a, aID); n != before[s.table] {
					t.Errorf("%s: %d rows after the merge, want the %d it had (reached through an id that does not change)", s.table, n, before[s.table])
				}
			case slices.Contains(idColumns, s.probeColumn) && s.probeColumn != "account_id":
				if n := probeCount(t, env, s, a, aID); n != before[s.table] {
					t.Errorf("%s: %d rows reachable by id, want %d", s.table, n, before[s.table])
				}
			default:
				if n := probeCount(t, env, s, a, aID); n != 0 {
					t.Errorf("%s: %d rows left under the merged-away rider", s.table, n)
				}
				if n := probeCount(t, env, s, b, aID); n != before[s.table] {
					t.Errorf("%s: %d rows under the target, want %d", s.table, n, before[s.table])
				}
			}
			if n := probeCount(t, env, s, other, otherID); n != otherBefore[s.table] {
				t.Errorf("%s: another rider has %d rows, want %d", s.table, n, otherBefore[s.table])
			}
		}
	})
}

// Where both riders have a row on the same key, the target's stays and the
// source's is dropped, and the summary says which tables that happened in.
func TestMergeRiderKeepsTheTargetsRowWhereTheKeyCollides(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		id := "x" + tok

		seeds := collidingSeeds()
		if len(seeds) < 10 {
			t.Fatalf("only %d unique-keyed seeds: the registry or the seeds changed", len(seeds))
		}
		for _, s := range seeds {
			if err := s.insert(env, a, id); err != nil {
				t.Fatalf("seeding %s for %s: %v", s.table, a, err)
			}
			insertForTarget(t, env, s, b, id)
		}

		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		for _, s := range seeds {
			if n := probeCount(t, env, s, b, id); n != 1 {
				t.Errorf("%s: %d rows under the target, want its own single row kept", s.table, n)
			}
			c := sum.Tables[s.table]
			switch s.table {
			case "sync_state":
				// The source's account was dropped along with its state.
				if c.DroppedSource != 1 {
					t.Errorf("sync_state: dropped-source = %d, want 1", c.DroppedSource)
				}
			default:
				if c.Moved != 0 || c.DroppedSource != 1 || c.KeptTarget != 1 {
					t.Errorf("%s: summary %+v, want nothing moved, 1 kept-target, 1 dropped-source", s.table, c)
				}
			}
		}
	})
}

// Rows that do not collide are carried over even in a table where others did.
func TestMergeRiderCarriesTheRowsThatDoNotCollide(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		for _, row := range []struct{ rider, date string }{{a, "2026-01-02"}, {a, "2026-01-03"}, {b, "2026-01-02"}} {
			if err := exec(env, `INSERT INTO daily_wellness (rider, date, updated_at) VALUES (?, ?, ?)`, row.rider, row.date, ts); err != nil {
				t.Fatal(err)
			}
		}
		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if c := sum.Tables["daily_wellness"]; c.Moved != 1 || c.DroppedSource != 1 || c.KeptTarget != 1 {
			t.Errorf("daily_wellness summary = %+v, want 1 moved, 1 kept-target, 1 dropped-source", c)
		}
		if n := count(t, env, "daily_wellness", "rider", b); n != 2 {
			t.Errorf("daily_wellness under the target = %d, want 2", n)
		}
		if n := count(t, env, "daily_wellness", "rider", a); n != 0 {
			t.Errorf("daily_wellness left under the source = %d", n)
		}
	})
}

// An account on the same provider on both sides keeps the target's, and the
// source's sync state goes with the account it described; the rest moves.
func TestMergeRiderKeepsTheTargetsAccountAndMovesTheOthers(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		for _, q := range []struct {
			q    string
			args []any
		}{
			{`INSERT INTO accounts (id, provider, rider, created_at, updated_at) VALUES (?, 'garmin', ?, ?, ?)`, []any{"garmin:" + a, a, ts, ts}},
			{`INSERT INTO accounts (id, provider, rider, created_at, updated_at) VALUES (?, 'wahoo', ?, ?, ?)`, []any{"wahoo:" + a, a, ts, ts}},
			{`INSERT INTO accounts (id, provider, rider, created_at, updated_at) VALUES (?, 'garmin', ?, ?, ?)`, []any{"garmin:" + b, b, ts, ts}},
			{`INSERT INTO sync_state (account_id, slug, remote_id, content_hash, updated_at) VALUES (?, 's1', 'src', 'h', ?)`, []any{"garmin:" + a, ts}},
			{`INSERT INTO sync_state (account_id, slug, remote_id, content_hash, updated_at) VALUES (?, 's2', 'src', 'h', ?)`, []any{"wahoo:" + a, ts}},
			{`INSERT INTO sync_state (account_id, slug, remote_id, content_hash, updated_at) VALUES (?, 's1', 'target', 'h', ?)`, []any{"garmin:" + b, ts}},
		} {
			if err := exec(env, q.q, q.args...); err != nil {
				t.Fatal(err)
			}
		}
		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if c := sum.Tables["accounts"]; c.Moved != 1 || c.KeptTarget != 1 || c.DroppedSource != 1 {
			t.Errorf("accounts summary = %+v, want the wahoo one moved and the garmin one kept from the target", c)
		}
		if n := count(t, env, "accounts", "rider", b); n != 2 {
			t.Errorf("accounts under the target = %d, want garmin (its own) and wahoo (moved)", n)
		}
		if n := count(t, env, "accounts", "rider", a); n != 0 {
			t.Errorf("accounts left under the source = %d", n)
		}
		if n := count(t, env, "sync_state", "account_id", "wahoo:"+b); n != 1 {
			t.Errorf("sync state of the moved account = %d rows, want 1 under its new id", n)
		}
		var remote string
		if err := env.db.QueryRow(env.d.Rebind(`SELECT remote_id FROM sync_state WHERE account_id = ? AND slug = 's1'`), "garmin:"+b).Scan(&remote); err != nil || remote != "target" {
			t.Errorf("the target's own sync state = %q (%v), want it untouched", remote, err)
		}
		if n := count(t, env, "sync_state", "account_id", "garmin:"+a); n != 0 {
			t.Errorf("the dropped account's sync state survived: %d rows", n)
		}
	})
}

// A rider with training data and nothing else, which is what a split rider
// often is, merges without a word about the tables it never had rows in.
func TestMergeRiderMergesARiderWithOnlyTrainingData(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		var training []seed
		for _, s := range riderSeeds() {
			if strings.HasPrefix(riderTables[s.table].Note, "Training.") || strings.Contains(riderTables[s.table].Note, "Training.Delete") {
				training = append(training, s)
			}
		}
		if len(training) < 8 {
			t.Fatalf("only %d training seeds found", len(training))
		}
		for _, s := range training {
			if err := s.insert(env, a, "a"+tok); err != nil {
				t.Fatalf("seeding %s: %v", s.table, err)
			}
		}
		// The target is somebody with their own, different, training data.
		if err := exec(env, `INSERT INTO daily_wellness (rider, date, updated_at) VALUES (?, '2025-12-31', ?)`, b, ts); err != nil {
			t.Fatal(err)
		}

		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		for table, c := range sum.Tables {
			if rt := riderTables[table]; !strings.Contains(rt.Note, "Training.") {
				t.Errorf("%s was touched (%+v) but the rider had no data there", table, c)
			}
		}
		for _, s := range training {
			if riderTables[s.table].Rename.Skip != "" || slices.Contains(idColumns, s.probeColumn) {
				continue
			}
			if n := probeCount(t, env, s, a, "a"+tok); n != 0 {
				t.Errorf("%s: %d rows left under the merged-away rider", s.table, n)
			}
			if n := probeCount(t, env, s, b, "a"+tok); n == 0 {
				t.Errorf("%s: nothing reached the target", s.table)
			}
		}
	})
}

// A dry run reports exactly what the real run then does, and writes nothing.
func TestMergeRiderDryRunWritesNothingAndMatchesTheRealRun(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		id := "x" + tok
		for _, s := range allRenameSeeds() {
			if err := s.insert(env, a, id); err != nil {
				t.Fatalf("seeding %s: %v", s.table, err)
			}
		}
		for _, s := range collidingSeeds() {
			insertForTarget(t, env, s, b, id)
		}
		snap := func() map[string][2]int {
			out := map[string][2]int{}
			for _, s := range allRenameSeeds() {
				out[s.table] = [2]int{probeCount(t, env, s, a, id), probeCount(t, env, s, b, id)}
			}
			return out
		}
		before := snap()

		dry, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if after := snap(); !reflect.DeepEqual(before, after) {
			t.Fatalf("a dry run changed the data:\nbefore %v\nafter  %v", before, after)
		}

		real, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(dry.Tables, real.Tables) {
			t.Errorf("the dry run said %+v, the real run did %+v", dry.Tables, real.Tables)
		}
		if reflect.DeepEqual(before, snap()) {
			t.Error("the real run changed nothing")
		}
	})
}

// Bindings of the merged-away rider follow it, and the sessions of both
// riders end, after the data is committed and never on a dry run.
func TestMergeRiderMovesBindingsAndEndsBothRidersSessions(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		for _, sub := range []string{"auth0|" + a, "google-oauth2|" + a} {
			if _, err := env.srv.Identities.Bind(context.Background(), "https://issuer.test/", sub, a, ""); err != nil {
				t.Fatal(err)
			}
		}

		var ended []string
		opt := MergeOptions{EndSessions: func(_ context.Context, rider string) (int, error) {
			ended = append(ended, rider)
			return 1, nil
		}}
		dryOpt := opt
		dryOpt.DryRun = true
		if _, err := MergeRider(context.Background(), env.db, env.d, a, b, dryOpt); err != nil {
			t.Fatal(err)
		}
		if len(ended) != 0 {
			t.Fatalf("a dry run ended sessions of %v", ended)
		}

		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, opt)
		if err != nil {
			t.Fatal(err)
		}
		if c := sum.Tables["rider_identities"]; c.Moved != 2 {
			t.Errorf("bindings summary = %+v, want 2 moved", c)
		}
		if n := count(t, env, "rider_identities", "rider", b); n != 2 {
			t.Errorf("bindings under the target = %d, want 2", n)
		}
		slices.Sort(ended)
		if want := []string{a, b}; !slices.Equal(ended, want) {
			t.Errorf("sessions ended for %v, want both of %v", ended, want)
		}
		if sum.SessionsEnded != 2 {
			t.Errorf("sessions ended = %d, want the 2 the hook reported", sum.SessionsEnded)
		}
	})
}

func TestMergeRiderRefusesNonsense(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		for name, args := range map[string][2]string{
			"same":       {"a", "a"},
			"same, case": {"a", " A "},
			"empty from": {"", "b"},
			"empty into": {"a", ""},
			"bad name":   {"a", "b c"},
		} {
			if _, err := MergeRider(context.Background(), env.db, env.d, args[0], args[1], MergeOptions{}); err == nil {
				t.Errorf("%s: merge %q into %q succeeded", name, args[0], args[1])
			}
		}
	})
}
