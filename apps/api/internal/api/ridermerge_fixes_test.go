package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func mergeTok(t *testing.T) string { return fmt.Sprintf("%d", time.Now().UnixNano()) }

// uniqueIndexKeys is the column sets of every UNIQUE index (primary keys
// included) the live schema has on table.
func uniqueIndexKeys(t *testing.T, env *riderDataEnv, table string) [][]string {
	t.Helper()
	var q string
	if env.d.Name == dbx.Postgres.Name {
		q = `SELECT i.relname, a.attname
               FROM pg_index x
               JOIN pg_class t ON t.oid = x.indrelid
               JOIN pg_class i ON i.oid = x.indexrelid
               JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY (x.indkey)
              WHERE x.indisunique AND t.relname = ? AND t.relnamespace = current_schema()::regnamespace
              ORDER BY i.relname, a.attname`
	} else {
		q = `SELECT il.name, ii.name FROM pragma_index_list(?) il, pragma_index_info(il.name) ii
              WHERE il."unique" = 1 ORDER BY il.name, ii.name`
	}
	rows, err := env.db.Query(env.d.Rebind(q), table)
	if err != nil {
		t.Fatalf("reading the unique indexes of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	byIndex := map[string][]string{}
	for rows.Next() {
		var idx, col string
		if err := rows.Scan(&idx, &col); err != nil {
			t.Fatal(err)
		}
		byIndex[idx] = append(byIndex[idx], col)
	}
	var out [][]string
	for _, cols := range byIndex {
		sort.Strings(cols)
		out = append(out, cols)
	}
	return out
}

// TestEveryUniqueIndexOnARiderKeyedTableIsInTheRegistry is the guard for the
// merge's collision handling: a unique key that includes a rider column and is
// not in the registry (as Unique/With or as an Also key) is a collision the
// merge does not know about, which aborts a production merge half-way through
// the data instead of keeping the target's row. It compares the keys the real
// schema has with the registry's, in both directions, so a missing key and a key
// the registry states too short both fail.
func TestEveryUniqueIndexOnARiderKeyedTableIsInTheRegistry(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		for table, rt := range riderTables {
			rule := rt.Rename
			if rule.Bespoke || rule.Skip != "" {
				continue // accounts, sync_state, provider_links: mergeBespoke's code
			}
			have := columnsOf(t, env, table)
			var want [][]string
			if rule.Unique {
				want = append(want, append([]string{rule.Columns[0]}, rule.With...))
			}
			for _, k := range rule.Also {
				want = append(want, slices.Clone(k.Columns))
			}
			for _, k := range want {
				sort.Strings(k)
			}

			var live [][]string
			for _, key := range uniqueIndexKeys(t, env, table) {
				involvesRider := false
				for _, c := range key {
					if slices.Contains(riderColumns, c) {
						involvesRider = true
					}
				}
				if involvesRider {
					live = append(live, key)
				}
			}
			for _, key := range live {
				found := false
				for _, w := range want {
					found = found || slices.Equal(w, key)
				}
				if !found {
					t.Errorf("%s has a unique key on %v that its registry rule does not state (Unique/With or Also): a merge would collide on it", table, key)
				}
			}
			for _, w := range want {
				found := false
				for _, key := range live {
					found = found || slices.Equal(w, key)
				}
				if !found {
					t.Errorf("%s: the registry states a unique key on %v that the schema does not have", table, w)
				}
				for _, c := range w {
					if !slices.Contains(have, c) {
						t.Errorf("%s: key column %q is not in the table", table, c)
					}
				}
			}
		}
	})
}

func insertWorkout(t *testing.T, env *riderDataEnv, id, rider, crewRide, date string) {
	t.Helper()
	if err := exec(env, `INSERT INTO workouts (id, rider, name, date, crew_ride_id, steps, created_at, updated_at) VALUES (?, ?, 'W', ?, ?, ?, ?, ?)`,
		id, rider, date, crewRide, []byte("[]"), ts, ts); err != nil {
		t.Fatal(err)
	}
}

// Both riders have a fixed session for the same crew ride: the index that keeps
// a rider to one of them would abort the whole merge. The target's stays, the
// source's goes (with its push record), it is reported, and everything else
// carries over.
func TestMergeRiderDropsTheSourcesFixedSessionForACrewRideBothHave(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := mergeTok(t)
		a, b := "a"+tok, "b"+tok
		insertWorkout(t, env, "wa1"+tok, a, "ride-1", "")
		insertWorkout(t, env, "wa2"+tok, a, "ride-2", "")
		insertWorkout(t, env, "wa3"+tok, a, "", "")
		insertWorkout(t, env, "wa4"+tok, a, "", "") // two without a crew ride is fine
		insertWorkout(t, env, "wb1"+tok, b, "ride-1", "")
		insertWorkout(t, env, "wb2"+tok, b, "", "")
		if err := exec(env, `INSERT INTO workout_pushes (workout_id, provider, remote_id, content_hash, pushed_at) VALUES (?, 'garmin', 'r', 'h', ?)`, "wa1"+tok, ts); err != nil {
			t.Fatal(err)
		}

		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{})
		if err != nil {
			t.Fatalf("a clash on the crew-ride key aborted the merge: %v", err)
		}
		c := sum.Tables["workouts"]
		if c.Moved != 3 || c.DroppedSource != 1 || c.KeptTarget != 1 {
			t.Errorf("workouts summary = %+v, want 3 moved, 1 kept-target, 1 dropped-source", c)
		}
		if n := count(t, env, "workouts", "rider", b); n != 5 {
			t.Errorf("workouts under the target = %d, want its 2 plus the 3 that moved", n)
		}
		if n := count(t, env, "workouts", "rider", a); n != 0 {
			t.Errorf("workouts left under the source = %d", n)
		}
		if n := count(t, env, "workout_pushes", "workout_id", "wa1"+tok); n != 0 {
			t.Errorf("the dropped workout's push record survived: %d", n)
		}
		listed := false
		for _, d := range sum.Dropped {
			listed = listed || (d.Table == "workouts" && d.Consequence != "")
		}
		if !listed {
			t.Errorf("dropped = %+v, want the dropped fixed session reported by table with a consequence", sum.Dropped)
		}
	})
}

func insertMember(t *testing.T, env *riderDataEnv, crew, rider, status string, owner, schedule bool) {
	t.Helper()
	if err := exec(env, `INSERT INTO crew_members (crew_id, rider, status, requested_at, can_schedule, is_owner) VALUES (?, ?, ?, ?, ?, ?)`,
		crew, rider, status, ts, schedule, owner); err != nil {
		t.Fatal(err)
	}
}

func member(t *testing.T, env *riderDataEnv, crew, rider string) (status string, owner, schedule bool) {
	t.Helper()
	if err := env.db.QueryRow(env.d.Rebind(`SELECT status, is_owner, can_schedule FROM crew_members WHERE crew_id = ? AND rider = ?`), crew, rider).
		Scan(&status, &owner, &schedule); err != nil {
		t.Fatalf("reading %s in %s: %v", rider, crew, err)
	}
	return
}

// The owner's case: the old rider is the crew's approved owner, the one they
// sign in as is only a pending member. Keeping the target's row would leave the
// crew without its owner; the strongest of the two wins and each flag is the OR.
func TestMergeRiderKeepsTheStrongestCrewMembership(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := mergeTok(t)
		a, b := "a"+tok, "b"+tok
		insertMember(t, env, "owned"+tok, a, "approved", true, true)    // source: owner
		insertMember(t, env, "owned"+tok, b, "pending", false, false)   // target: only asked to join
		insertMember(t, env, "flags"+tok, a, "approved", false, true)   // source can schedule
		insertMember(t, env, "flags"+tok, b, "approved", true, false)   // target is owner
		insertMember(t, env, "weaker"+tok, a, "pending", false, false)  // source is weaker
		insertMember(t, env, "weaker"+tok, b, "approved", false, false) // target stays approved
		insertMember(t, env, "only"+tok, a, "approved", false, false)   // no clash: moves

		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if st, o, s := member(t, env, "owned"+tok, b); st != "approved" || !o || !s {
			t.Errorf("owned crew: target is %s owner=%v schedule=%v, want approved owner who can schedule", st, o, s)
		}
		if st, o, s := member(t, env, "flags"+tok, b); st != "approved" || !o || !s {
			t.Errorf("flags crew: target is %s owner=%v schedule=%v, want the OR of both rows", st, o, s)
		}
		if st, o, s := member(t, env, "weaker"+tok, b); st != "approved" || o || s {
			t.Errorf("weaker crew: target is %s owner=%v schedule=%v, want unchanged", st, o, s)
		}
		if n := count(t, env, "crew_members", "rider", a); n != 0 {
			t.Errorf("%d memberships left under the source", n)
		}
		c := sum.Tables["crew_members"]
		if c.Merged != 3 || c.DroppedSource != 0 || c.Moved != 1 {
			t.Errorf("crew_members summary = %+v, want 3 merged, none dropped, 1 moved", c)
		}
	})
}

func TestMergeRiderWarnsWhenBothRidersHaveTrainingPlans(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := mergeTok(t)
		a, b := "a"+tok, "b"+tok
		now := func() time.Time { return time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC) }
		for _, r := range []string{a, b} {
			if err := exec(env, `INSERT INTO goals (id, rider, name, created_at, updated_at) VALUES (?, ?, 'Goal', ?, ?)`, "g"+r, r, ts, ts); err != nil {
				t.Fatal(err)
			}
		}
		for i, day := range []string{"2026-03-01", "2026-03-02", "2026-03-03"} {
			insertWorkout(t, env, fmt.Sprintf("fa%d%s", i, tok), a, "", day)
		}
		insertWorkout(t, env, "fb1"+tok, b, "", "2026-03-01")
		insertWorkout(t, env, "fb2"+tok, b, "", "2026-03-02")
		insertWorkout(t, env, "fb3"+tok, b, "", "2026-03-09")
		insertWorkout(t, env, "pa"+tok, a, "", "2026-01-10") // past: not a plan
		insertWorkout(t, env, "pb"+tok, b, "", "2026-01-10")

		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{DryRun: true, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(sum.Warnings, "\n")
		if !strings.Contains(joined, "two plans will overlap on 2 days") {
			t.Errorf("warnings = %q, want the overlap on 2 days", joined)
		}
		if !strings.Contains(joined, "goals") {
			t.Errorf("warnings = %q, want a warning that both riders have goals", joined)
		}

		// One side with nothing planned is nothing to warn about.
		clean, err := MergeRider(context.Background(), env.db, env.d, "nobody"+tok, b, MergeOptions{DryRun: true, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		if len(clean.Warnings) != 0 {
			t.Errorf("warnings = %q for a rider with nothing, want none", clean.Warnings)
		}
	})
}

// What a collision costs the person is named before anything is written: the
// provider sign-in to reconnect, the calendar link that stops working.
func TestMergeRiderListsWhatItDropsByNameWithTheConsequence(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := mergeTok(t)
		a, b := "a"+tok, "b"+tok
		for _, r := range []string{a, b} {
			for _, s := range riderSeeds() {
				switch s.table {
				case "provider_links", "calendar_feeds", "morning_summaries", "rider_profiles", "accounts":
					if err := s.insert(env, r, "x"+r); err != nil {
						t.Fatalf("seeding %s: %v", s.table, err)
					}
				}
			}
		}
		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		byTable := map[string]MergeDrop{}
		for _, d := range sum.Dropped {
			byTable[d.Table] = d
		}
		for table, want := range map[string]string{
			"provider_links":    "reconnect",
			"calendar_feeds":    "calendar link",
			"morning_summaries": "morning",
			"rider_profiles":    "profile",
			"accounts":          "relink",
		} {
			d, ok := byTable[table]
			if !ok {
				t.Errorf("%s: not listed among the dropped rows %+v", table, sum.Dropped)
				continue
			}
			if !strings.Contains(strings.ToLower(d.Consequence), want) {
				t.Errorf("%s: consequence %q does not say %q", table, d.Consequence, want)
			}
		}
		if d := byTable["provider_links"]; d.What != "garmin" {
			t.Errorf("provider_links is listed as %q, want it by provider name", d.What)
		}
	})
}

// After the commit nothing is an error any more: the data has moved, so a
// session or a fitness step that fails is a warning the caller shows.
func TestMergeRiderTurnsPostCommitFailuresIntoWarnings(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := mergeTok(t)
		a, b := "a"+tok, "b"+tok
		insertWorkout(t, env, "w"+tok, a, "", "")
		sum, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{
			EndSessions:      func(context.Context, string) (int, error) { return 0, errors.New("sessions are down") },
			RecomputeFitness: func(context.Context, string) error { return errors.New("fitness is down") },
		})
		if err != nil {
			t.Fatalf("a post-commit failure was returned as an error: %v", err)
		}
		joined := strings.Join(sum.Warnings, "\n")
		if !strings.Contains(joined, "sessions") || !strings.Contains(joined, "fitness") {
			t.Errorf("warnings = %q, want both failures named", joined)
		}
		if n := count(t, env, "workouts", "rider", b); n != 1 {
			t.Errorf("the data did not move: %d", n)
		}
	})
}

// A merge takes the scheduling and background-sync locks, so a tick cannot
// interleave with it; while one is held it refuses rather than waiting.
func TestMergeRiderRefusesWhileABackgroundJobHoldsTheLock(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		if env.d.Name != dbx.Postgres.Name {
			t.Skip("advisory locks exist on PostgreSQL only; SQLite has one writer")
		}
		tx, err := env.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		var locked bool
		if err := tx.QueryRow(`SELECT pg_try_advisory_xact_lock(hashtext($1))`, autoScheduleLockKey).Scan(&locked); err != nil || !locked {
			t.Fatalf("could not take the lock for the test: %v %v", locked, err)
		}
		tok := mergeTok(t)
		_, err = MergeRider(context.Background(), env.db, env.d, "a"+tok, "b"+tok, MergeOptions{})
		if !errors.Is(err, ErrMergeBusy) {
			t.Errorf("err = %v, want ErrMergeBusy while the scheduling lock is held", err)
		}
		// A dry run writes nothing, so it does not need the lock.
		if _, err := MergeRider(context.Background(), env.db, env.d, "a"+tok, "b"+tok, MergeOptions{DryRun: true}); err != nil {
			t.Errorf("dry run: %v", err)
		}
	})
}

func TestMergeRiderRecomputesTheTargetsFitnessSnapshots(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		ctx := context.Background()
		tok := mergeTok(t)
		a, b, c := "a"+tok, "b"+tok, "c"+tok
		env.srv.Training.Clock = func() time.Time { return time.Date(2026, 1, 20, 9, 0, 0, 0, time.UTC) }
		session := func(rider, ext, date string, load float64) {
			if _, err := env.srv.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
				Rider: rider, Provider: "garmin", ExternalID: ext, Sport: "cycling", Date: date, TrainingLoad: load,
			}); err != nil {
				t.Fatal(err)
			}
		}
		session(a, "a1"+tok, "2026-01-10", 80)
		session(b, "b1"+tok, "2026-01-05", 40)
		for _, r := range []string{a, b} {
			if err := env.srv.Training.RecomputeFitnessSnapshots(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		before, _ := env.srv.Training.ListFitnessSnapshots(ctx, b)

		opt := MergeOptions{RecomputeFitness: env.srv.Training.RecomputeFitnessSnapshots}
		first, err := MergeRider(ctx, env.db, env.d, a, b, opt)
		if err != nil || len(first.Warnings) != 0 {
			t.Fatalf("merge: %v, warnings %q", err, first.Warnings)
		}
		after, err := env.srv.Training.ListFitnessSnapshots(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		ctlOn := func(snaps []workout.FitnessSnapshot, date string) float64 {
			for _, s := range snaps {
				if s.Date == date {
					return s.CTL
				}
			}
			return -1
		}
		if got, was := ctlOn(after, "2026-01-11"), ctlOn(before, "2026-01-11"); !(got > was) {
			t.Errorf("CTL the day after the source's session = %v after the merge, %v before: the history was not recomputed with it", got, was)
		}

		// A target left with no sessions has no history to show: the source's
		// snapshots that moved across would be fiction.
		if err := exec(env, `INSERT INTO fitness_snapshots (rider, date, ctl, atl, tsb) VALUES (?, '2026-01-02', 5, 5, 0)`, c); err != nil {
			t.Fatal(err)
		}
		d := "d" + tok
		if err := exec(env, `INSERT INTO fitness_snapshots (rider, date, ctl, atl, tsb) VALUES (?, '2026-01-03', 9, 9, 0)`, d); err != nil {
			t.Fatal(err)
		}
		opt = MergeOptions{RecomputeFitness: env.srv.Training.RecomputeFitnessSnapshots}
		if _, err := MergeRider(ctx, env.db, env.d, c, d, opt); err != nil {
			t.Fatal(err)
		}
		if n := count(t, env, "fitness_snapshots", "rider", d); n != 0 {
			t.Errorf("a target with no sessions kept %d fitness snapshots", n)
		}
	})
}

// mergeRequest drives the handler the way the router would, as an admin.
func mergeRequest(t *testing.T, srv *Server, from, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/people/riders/"+from+"/merge", strings.NewReader(body))
	req.SetPathValue("rider", from)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{User: "boss", Role: auth.RoleAdmin}))
	rec := httptest.NewRecorder()
	srv.handlePeopleMergeRider(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The handler's wiring after the commit: it ends both riders' sessions,
// recomputes the target's fitness, forgets the held loops of both riders, and
// returns 200 whatever the follow-through did, with a warning when it failed.
func TestTheMergeHandlerFollowsThroughAfterTheCommit(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		ctx := context.Background()
		tok := mergeTok(t)
		a, b := "a"+tok, "b"+tok
		srv := env.srv

		if _, err := srv.Training.UpsertSession(ctx, workout.UpsertSessionRequest{Rider: a, Provider: "garmin", ExternalID: "x" + tok, Sport: "cycling", Date: "2026-01-10", TrainingLoad: 50}); err != nil {
			t.Fatal(err)
		}
		tokenA, _, err := srv.Sessions.Create(auth.Identity{User: a, Sub: "sub|a" + tok}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		tokenB, _, err := srv.Sessions.Create(auth.Identity{User: b, Sub: "sub|b" + tok}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		held := func(rider string) int { return srv.candidateStore().Count(rider) }
		srv.candidateStore().Hold(a, "w1", []heldCandidate{{}})
		srv.candidateStore().Hold(b, "w2", []heldCandidate{{}})
		if held(a) == 0 || held(b) == 0 {
			t.Fatal("the test could not seed held loops")
		}

		code, out := mergeRequest(t, srv, a, fmt.Sprintf(`{"into":%q}`, b))
		if code != http.StatusOK {
			t.Fatalf("merge = %d %v", code, out)
		}
		if _, ok := srv.Sessions.Lookup(tokenA); ok {
			t.Error("the merged-away rider's session survived: EndSessions was not wired")
		}
		if _, ok := srv.Sessions.Lookup(tokenB); ok {
			t.Error("the target's session survived: EndSessions was not wired for both riders")
		}
		if held(a) != 0 || held(b) != 0 {
			t.Errorf("held loops after the merge: %d and %d, want both forgotten", held(a), held(b))
		}
		snaps, _ := srv.Training.ListFitnessSnapshots(ctx, b)
		if len(snaps) == 0 {
			t.Error("the target has no fitness snapshots: the recompute was not wired")
		}
		if out["warnings"] != nil && len(out["warnings"].([]any)) != 0 {
			t.Errorf("warnings = %v on a clean merge", out["warnings"])
		}

		// Now the follow-through fails: the sessions table is gone. The data
		// has still moved, so it is a 200 with a warning, and the loops are
		// still forgotten.
		c, d := "c"+tok, "d"+tok
		insertWorkout(t, env, "wc"+tok, c, "", "")
		srv.candidateStore().Hold(c, "w3", []heldCandidate{{}})
		if err := exec(env, `DROP TABLE sessions`); err != nil {
			t.Fatal(err)
		}
		code, out = mergeRequest(t, srv, c, fmt.Sprintf(`{"into":%q}`, d))
		if code != http.StatusOK {
			t.Fatalf("a failed EndSessions after the commit gave %d %v, want 200", code, out)
		}
		warns, _ := out["warnings"].([]any)
		if len(warns) == 0 || !strings.Contains(fmt.Sprint(warns), "session") {
			t.Errorf("warnings = %v, want one about the sessions", out["warnings"])
		}
		if n := count(t, env, "workouts", "rider", d); n != 1 {
			t.Errorf("the data did not move: %d", n)
		}
		if held(c) != 0 {
			t.Error("Forget was skipped after a failed EndSessions")
		}
	})
}
