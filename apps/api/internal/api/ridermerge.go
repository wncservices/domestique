package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// mergeBespoke is the registered tables whose rename needs code of its own (a
// derived id, a composite key), and so whose merge does too. A bespoke table
// that is not listed here has no merge rule, which
// TestEveryRegisteredTableHasAMergeRule refuses: it would stay behind under the
// rider being merged away.
var mergeBespoke = map[string]bool{"accounts": true, "sync_state": true, "provider_links": true}

// MergeOptions are the flags of the merge-rider command.
type MergeOptions struct {
	// DryRun does everything a real merge does inside the transaction, counts
	// it, and rolls back: the summary is exactly what a real run would do, and
	// nothing is written.
	DryRun bool
	// EndSessions, when set, is called after a real merge has committed, for
	// the rider merged away and for the target. Sessions hold the rider they
	// were made with, so both riders' logins end and sign in again through the
	// bindings. It lives outside MergeRider because a session is found by a keyed
	// hash of the rider, which needs the deployment's key.
	EndSessions func(ctx context.Context, rider string) (int, error)
	// RecomputeFitness, when set, rebuilds a rider's fitness history from their
	// sessions after a real merge, for the target.
	RecomputeFitness func(ctx context.Context, rider string) error
	// Now replaces time.Now (what "future" means to the plan warning), for tests.
	Now func() time.Time
}

// ErrMergeBusy means a background job holds a lock the merge needs.
var ErrMergeBusy = errors.New("merge-rider: a background job is running; try again in a minute")

// MergeDrop is one row the merge deletes because the target has its own, named
// so the person can see what it costs them before anything is written.
type MergeDrop struct {
	Table string
	// What names the row: a provider, a crew ride.
	What        string
	Consequence string
}

// MergeCounts is what a merge did in one table.
type MergeCounts struct {
	// Moved is the rows that now belong to the target.
	Moved int
	// KeptTarget is the target's rows that were left alone because the rider
	// being merged away had one on the same key.
	KeptTarget int
	// DroppedSource is the merged-away rider's rows that were deleted for the
	// same reason: two riders cannot both hold one key, and the target wins.
	DroppedSource int
	// Merged is the target's rows that took the stronger of the two (crew
	// membership): not a loss, so not counted as dropped.
	Merged int
}

// MergeSummary says, per table, what a merge did (or, on a dry run, would do).
// Tables with nothing in them are left out.
type MergeSummary struct {
	Tables map[string]MergeCounts
	// Dropped lists, by name, the rows that cost something to lose.
	Dropped []MergeDrop
	// Warnings are things to read before (or after) merging: two plans that will
	// overlap, a follow-up step that failed once the data had moved.
	Warnings []string
	// SessionsEnded is what EndSessions reported, summed over both riders.
	SessionsEnded int
}

// Totals adds every table up.
func (m MergeSummary) Totals() MergeCounts {
	var t MergeCounts
	for _, c := range m.Tables {
		t.Moved += c.Moved
		t.KeptTarget += c.KeptTarget
		t.DroppedSource += c.DroppedSource
		t.Merged += c.Merged
	}
	return t
}

// TableNames is the tables the merge touched, sorted.
func (m MergeSummary) TableNames() []string {
	names := make([]string, 0, len(m.Tables))
	for t := range m.Tables {
		names = append(names, t)
	}
	sort.Strings(names)
	return names
}

func (m *MergeSummary) add(table string, c MergeCounts) {
	if c == (MergeCounts{}) {
		return
	}
	cur := m.Tables[table]
	cur.Moved += c.Moved
	cur.KeptTarget += c.KeptTarget
	cur.DroppedSource += c.DroppedSource
	cur.Merged += c.Merged
	m.Tables[table] = cur
}

// MergeRider moves everything the rider from owns onto into, across every table
// in riderTables, in one transaction, and leaves from with nothing. It is for a
// person who has ended up as two riders (an old name-derived rider and the one
// their account is bound to), where rename-rider would refuse because the
// target already holds data.
//
// The rules follow the registry's rename rules, which is what keeps a new table
// from being missed (TestEveryRegisteredTableHasAMergeRule):
//
//   - a plain table has its rows moved;
//   - a table whose key includes the rider (renameRule.Unique) has its rows
//     moved except where the target already has one on the same key: there the
//     target's stays and the source's is dropped, and the summary counts both.
//     Which of two riders' wellness for one day is right is not something this
//     can know; the target's is the one the person is signed in to;
//   - accounts, whose id is "<provider>:<rider>", and the sync state and
//     provider sign-ins that hang off them are handled by mergeBespoke's code:
//     one account (or sign-in) per provider, the target's kept;
//   - identity bindings are ordinary rows and move, so the accounts that signed
//     in as from now sign in as into;
//   - sessions are ended for both riders through opt.EndSessions, after the
//     commit.
//
// Both names are normalised here. A failure part-way rolls everything back.
func MergeRider(ctx context.Context, db *sql.DB, d dbx.Dialect, from, into string, opt MergeOptions) (MergeSummary, error) {
	sum := MergeSummary{Tables: map[string]MergeCounts{}}
	from, into = normalizeRiderName(from), normalizeRiderName(into)
	switch {
	case from == "" || into == "":
		return sum, errors.New("merge-rider: neither rider may be empty")
	case from == into:
		return sum, errors.New("merge-rider: both names are the same rider — nothing to do")
	case !accounts.RiderPattern.MatchString(into):
		return sum, fmt.Errorf("merge-rider: %q has characters that cannot appear in an account id", into)
	}

	// A real merge holds the locks the scheduling and background-sync ticks hold,
	// so neither can write between the moment the data is read and the commit.
	// They are try-locks: a tick that is running makes the merge refuse (the
	// caller says to try again) instead of waiting behind it. A dry run writes
	// nothing and needs neither. On SQLite there is one writer and no lock.
	var (
		err    error
		locked = true
	)
	run := func() { err = mergeInTx(ctx, db, d, from, into, opt, &sum) }
	if opt.DryRun {
		run()
	} else {
		ranOuter := withDBLock(ctx, db, autoScheduleLockKey, func() {
			ranInner := withDBLock(ctx, db, backgroundSyncLockKey, run)
			locked = locked && ranInner
		})
		locked = locked && ranOuter
		if !locked {
			return sum, ErrMergeBusy
		}
	}
	if err != nil {
		return sum, err
	}
	if opt.DryRun {
		return sum, nil
	}

	// The data has moved. Nothing below is an error any more: each step that
	// fails is a warning, because the caller can still tell the person what was
	// merged and what still needs doing.
	if opt.RecomputeFitness != nil {
		if err := opt.RecomputeFitness(ctx, into); err != nil {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("the data is merged, but %s's fitness history could not be rebuilt (%v); it is rebuilt the next time a session syncs", into, err))
		}
	}
	if opt.EndSessions != nil {
		for _, rider := range []string{from, into} {
			n, err := opt.EndSessions(ctx, rider)
			if err != nil {
				sum.Warnings = append(sum.Warnings, fmt.Sprintf("the data is merged, but the sessions of %s could not be ended (%v); they stay signed in until they expire", rider, err))
				continue
			}
			sum.SessionsEnded += n
		}
	}
	return sum, nil
}

// mergeInTx does the merge in one transaction and commits it unless opt.DryRun.
func mergeInTx(ctx context.Context, db *sql.DB, d dbx.Dialect, from, into string, opt MergeOptions, sum *MergeSummary) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	// Read before anything moves: it is the state the person is deciding on.
	if err := planWarnings(tx, d, from, into, now(), sum); err != nil {
		return err
	}
	for _, table := range mergeTables() {
		c, err := mergeGeneric(tx, d, table, from, into, sum)
		if err != nil {
			return err
		}
		sum.add(table, c)
	}
	if err := mergeAccounts(tx, d, from, into, sum); err != nil {
		return err
	}
	if err := mergeProviderLinks(tx, d, from, into, sum); err != nil {
		return err
	}
	if opt.DryRun {
		return nil // rolled back by the deferred Rollback: nothing was written
	}
	return tx.Commit()
}

// planWarnings says what a merge cannot sort out by itself. Training data is
// concatenated, not merged: two riders' goals and planned workouts do not
// collide on any key, so both sets survive, side by side, and the person has to
// tidy them up.
func planWarnings(tx *sql.Tx, d dbx.Dialect, from, into string, now time.Time, sum *MergeSummary) error {
	countOf := func(table string, extra string, args ...any) (int, error) {
		have, err := existingColumns(tx, d, table)
		if err != nil {
			return 0, err
		}
		if len(have) == 0 {
			return 0, nil
		}
		var n int
		// #nosec G701 -- table and clause are constants in this file; values are bound.
		err = tx.QueryRow(d.Rebind(`SELECT COUNT(*) FROM `+table+` WHERE `+extra), args...).Scan(&n)
		return n, err
	}

	gFrom, err := countOf("goals", `rider = ?`, from)
	if err != nil {
		return fmt.Errorf("merge-rider: counting goals: %w", err)
	}
	gInto, err := countOf("goals", `rider = ?`, into)
	if err != nil {
		return fmt.Errorf("merge-rider: counting goals: %w", err)
	}
	if gFrom > 0 && gInto > 0 {
		sum.Warnings = append(sum.Warnings, fmt.Sprintf(
			"both riders have training goals (%d for %s, %d for %s): they are concatenated, so %s ends up with %d goals and two training plans; delete the one you do not want after merging",
			gFrom, from, gInto, into, into, gFrom+gInto))
	}

	today := now.UTC().Format("2006-01-02")
	wFrom, err := countOf("workouts", `rider = ? AND date >= ?`, from, today)
	if err != nil {
		return fmt.Errorf("merge-rider: counting planned workouts: %w", err)
	}
	wInto, err := countOf("workouts", `rider = ? AND date >= ?`, into, today)
	if err != nil {
		return fmt.Errorf("merge-rider: counting planned workouts: %w", err)
	}
	if wFrom == 0 || wInto == 0 {
		return nil
	}
	var days int
	// #nosec G701 -- constant statement; values are bound.
	if err := tx.QueryRow(d.Rebind(`SELECT COUNT(DISTINCT w.date) FROM workouts w WHERE w.rider = ? AND w.date >= ?
        AND EXISTS (SELECT 1 FROM workouts o WHERE o.rider = ? AND o.date = w.date)`), from, today, into).Scan(&days); err != nil {
		return fmt.Errorf("merge-rider: comparing the two plans: %w", err)
	}
	switch {
	case days == 1:
		sum.Warnings = append(sum.Warnings, fmt.Sprintf("two plans will overlap on 1 day (%d planned workouts for %s, %d for %s from %s on): both sets are kept; delete one set after merging", wFrom, from, wInto, into, today))
	case days > 1:
		sum.Warnings = append(sum.Warnings, fmt.Sprintf("two plans will overlap on %d days (%d planned workouts for %s, %d for %s from %s on): both sets are kept; delete one set after merging", days, wFrom, from, wInto, into, today))
	default:
		sum.Warnings = append(sum.Warnings, fmt.Sprintf("both riders have planned workouts from %s on (%d for %s, %d for %s): they are concatenated, on different days", today, wFrom, from, wInto, into))
	}
	return nil
}

func normalizeRiderName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// mergeTables is the registered tables a merge rewrites by column, in a stable
// order: every one with rename Columns.
func mergeTables() []string { return registeredRenames() }

// dropNotes is what a dropped row costs the person, for the tables where it
// is not nothing. What is the column that names the row, or empty. The target's
// own row is always the one kept, so each says what stops working with the
// source's.
var dropNotes = map[string]struct {
	What        string
	Consequence func(from, into string) string
}{
	"calendar_feeds": {"", func(from, into string) string {
		return fmt.Sprintf("the calendar link of %s stops working (%s's own link is kept)", from, into)
	}},
	"morning_summaries": {"", func(from, into string) string {
		return fmt.Sprintf("the morning summary settings of %s are dropped (%s's own are kept)", from, into)
	}},
	"rider_profiles": {"", func(from, into string) string {
		return fmt.Sprintf("the training profile of %s (FTP, test dates) is dropped; %s's own profile is kept", from, into)
	}},
	"workouts": {"crew_ride_id", func(from, into string) string {
		return fmt.Sprintf("the fixed session %s had for this crew ride is dropped (%s's own is kept); a copy already pushed to a device stays there", from, into)
	}},
}

// mergeKeys is every unique key of a table that includes the rider: the one
// Unique/With state and each Also.
func mergeKeys(rule renameRule) []uniqueKey {
	var keys []uniqueKey
	if rule.Unique {
		keys = append(keys, uniqueKey{Columns: append([]string{rule.Columns[0]}, rule.With...)})
	}
	return append(keys, rule.Also...)
}

// mergeGeneric merges one registry table by its rename rule.
func mergeGeneric(tx *sql.Tx, d dbx.Dialect, table, from, into string, sum *MergeSummary) (MergeCounts, error) {
	var c MergeCounts
	rule := riderTables[table].Rename
	have, err := existingColumns(tx, d, table)
	if err != nil {
		return c, fmt.Errorf("merge-rider: reading the %s schema: %w", table, err)
	}
	cols := liveColumns(have, rule)
	if len(cols) == 0 {
		return c, nil // the table, or its rider column, is not in this database yet
	}

	// Table and column names are constants from riderTables, never input.
	for _, key := range mergeKeys(rule) {
		riderCol, rest, ok := splitKey(key, rule, have)
		if !ok {
			continue
		}
		same := func(a, b string) string {
			s := ""
			for _, w := range rest {
				s += " AND " + a + "." + w + " = " + b + "." + w
			}
			return s
		}
		// Only rows that take part in a partial index can collide on it.
		partial := ""
		for _, col := range key.NonEmpty {
			partial += " AND " + table + "." + col + " <> ''"
		}
		partialN := strings.ReplaceAll(partial, table+".", "n.")
		// Source rows the target already has a row for, on the whole key.
		srcCollide := `FROM ` + table + ` WHERE ` + table + `.` + riderCol + ` = ?` + partial + ` AND EXISTS (SELECT 1 FROM ` + table +
			` n WHERE n.` + riderCol + ` = ?` + same("n", table) + `)`
		var n int
		// #nosec G701 -- built only from riderTables constants; the riders are bound.
		if err := tx.QueryRow(d.Rebind(`SELECT COUNT(*) `+srcCollide), from, into).Scan(&n); err != nil {
			return c, fmt.Errorf("merge-rider: counting collisions in %s: %w", table, err)
		}
		if n == 0 {
			continue
		}
		// The target rows those collided with, counted from the target's side.
		var kept int
		keptQ := `SELECT COUNT(*) FROM ` + table + ` n WHERE n.` + riderCol + ` = ?` + partialN + ` AND EXISTS (SELECT 1 FROM ` + table +
			` o WHERE o.` + riderCol + ` = ?` + same("o", "n") + `)`
		// #nosec G701 -- built only from riderTables constants; the riders are bound.
		if err := tx.QueryRow(d.Rebind(keptQ), into, from).Scan(&kept); err != nil {
			return c, fmt.Errorf("merge-rider: counting the target's rows in %s: %w", table, err)
		}

		if table == "crew_members" {
			// Not a loss: the stronger of the two rows is what is left.
			if err := strengthenMemberships(tx, d, from, into); err != nil {
				return c, err
			}
			c.Merged += n
		} else {
			if note, ok := dropNotes[table]; ok {
				if err := listDrops(tx, d, table, note.What, srcCollide, from, into, note.Consequence(from, into), sum); err != nil {
					return c, err
				}
			}
			if table == "workouts" {
				if err := dropPushesOfCollidingWorkouts(tx, d, srcCollide, from, into); err != nil {
					return c, err
				}
			}
			c.KeptTarget += kept
			c.DroppedSource += n
		}
		// #nosec G701 -- built only from riderTables constants; the riders are bound.
		if _, err := tx.Exec(d.Rebind(`DELETE `+srcCollide), from, into); err != nil {
			return c, fmt.Errorf("merge-rider: dropping %s's colliding rows in %s: %w", from, table, err)
		}
	}

	var conds []string
	var args []any
	for _, col := range cols {
		conds = append(conds, col+" = ?")
		args = append(args, from)
	}
	// #nosec G701 -- table and column names are constants from riderTables.
	if err := tx.QueryRow(d.Rebind(`SELECT COUNT(*) FROM `+table+` WHERE `+strings.Join(conds, " OR ")), args...).Scan(&c.Moved); err != nil {
		return c, fmt.Errorf("merge-rider: counting %s: %w", table, err)
	}
	for _, col := range cols {
		// #nosec G701 -- table and column names are constants from riderTables.
		if _, err := tx.Exec(d.Rebind(`UPDATE `+table+` SET `+col+` = ? WHERE `+col+` = ?`), into, from); err != nil {
			return c, fmt.Errorf("merge-rider: moving %s.%s: %w", table, col, err)
		}
	}
	return c, nil
}

// splitKey is a key's rider column (the one the rename rule rewrites) and the
// rest of it. False when the table does not have all of the key's columns yet.
func splitKey(key uniqueKey, rule renameRule, have map[string]bool) (riderCol string, rest []string, ok bool) {
	for _, col := range key.Columns {
		if !have[col] {
			return "", nil, false
		}
		if riderCol == "" && slices.Contains(rule.Columns, col) {
			riderCol = col
			continue
		}
		rest = append(rest, col)
	}
	return riderCol, rest, riderCol != ""
}

// listDrops names, before they are deleted, the source rows that collide.
func listDrops(tx *sql.Tx, d dbx.Dialect, table, whatCol, srcCollide, from, into, consequence string, sum *MergeSummary) error {
	sel := `1`
	if whatCol != "" {
		sel = table + `.` + whatCol
	}
	// #nosec G701 -- built only from constants in this file; the riders are bound.
	rows, err := tx.Query(d.Rebind(`SELECT `+sel+` `+srcCollide), from, into)
	if err != nil {
		return fmt.Errorf("merge-rider: listing what %s drops from %s: %w", from, table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var what any
		if err := rows.Scan(&what); err != nil {
			return err
		}
		name := ""
		if whatCol != "" {
			name = fmt.Sprint(what)
		}
		sum.Dropped = append(sum.Dropped, MergeDrop{Table: table, What: name, Consequence: consequence})
	}
	return rows.Err()
}

// dropPushesOfCollidingWorkouts removes the push records of the workouts about
// to be dropped, which would otherwise point at nothing.
func dropPushesOfCollidingWorkouts(tx *sql.Tx, d dbx.Dialect, srcCollide, from, into string) error {
	have, err := existingColumns(tx, d, "workout_pushes")
	if err != nil {
		return err
	}
	if len(have) == 0 {
		return nil
	}
	// #nosec G701 -- built only from constants in this file; the riders are bound.
	_, err = tx.Exec(d.Rebind(`DELETE FROM workout_pushes WHERE workout_id IN (SELECT workouts.id `+srcCollide+`)`), from, into)
	if err != nil {
		return fmt.Errorf("merge-rider: dropping the push records of dropped workouts: %w", err)
	}
	return nil
}

// strengthenMemberships settles each crew both riders belong to on the stronger
// of the two rows before the source's is removed: approved beats pending, and
// is_owner and can_schedule are each the OR of the two. Keeping the target's row
// as it stood would, for the owner of a crew who signs in as the other rider,
// leave the crew without an owner nobody can sign in as.
func strengthenMemberships(tx *sql.Tx, d dbx.Dialect, from, into string) error {
	for _, col := range []string{"is_owner", "can_schedule"} {
		// #nosec G701 -- col is one of two constants; values are bound.
		if _, err := tx.Exec(d.Rebind(`UPDATE crew_members SET `+col+` = ? WHERE rider = ? AND `+col+` = ? AND EXISTS
            (SELECT 1 FROM crew_members s WHERE s.crew_id = crew_members.crew_id AND s.rider = ? AND s.`+col+` = ?)`),
			true, into, false, from, true); err != nil {
			return fmt.Errorf("merge-rider: merging crew memberships (%s): %w", col, err)
		}
	}
	// The decision that made the source approved comes along with its status.
	// #nosec G701 -- constant statement; values are bound.
	if _, err := tx.Exec(d.Rebind(`UPDATE crew_members SET status = 'approved',
            decided_by = (SELECT s.decided_by FROM crew_members s WHERE s.crew_id = crew_members.crew_id AND s.rider = ?),
            decided_at = (SELECT s.decided_at FROM crew_members s WHERE s.crew_id = crew_members.crew_id AND s.rider = ?)
        WHERE rider = ? AND status <> 'approved' AND EXISTS
            (SELECT 1 FROM crew_members s WHERE s.crew_id = crew_members.crew_id AND s.rider = ? AND s.status = 'approved')`),
		from, from, into, from); err != nil {
		return fmt.Errorf("merge-rider: merging crew memberships (status): %w", err)
	}
	return nil
}

// mergeAccounts merges the linked head units and their sync state. An account
// id is "<provider>:<rider>", and sync_state holds that id as half of its own
// key, so the two move together. Where the target already has an account for
// the provider it is kept, and the source's account goes with the sync state
// that described what it held: that state is about a different remote account,
// and would mislead the diff engine under the target's.
func mergeAccounts(tx *sql.Tx, d dbx.Dialect, from, into string, sum *MergeSummary) error {
	haveAccounts, err := existingColumns(tx, d, "accounts")
	if err != nil {
		return fmt.Errorf("merge-rider: reading the accounts schema: %w", err)
	}
	if len(haveAccounts) == 0 {
		return nil
	}
	haveState, err := existingColumns(tx, d, "sync_state")
	if err != nil {
		return fmt.Errorf("merge-rider: reading the sync_state schema: %w", err)
	}

	// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
	rows, err := tx.Query(d.Rebind(`SELECT id, provider FROM accounts WHERE rider = ? ORDER BY id`), from)
	if err != nil {
		return fmt.Errorf("merge-rider: reading %s's accounts: %w", from, err)
	}
	type pair struct{ oldID, provider string }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.oldID, &p.provider); err != nil {
			_ = rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	countState := func(id string) (n int, err error) {
		if len(haveState) == 0 {
			return 0, nil
		}
		// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
		err = tx.QueryRow(d.Rebind(`SELECT COUNT(*) FROM sync_state WHERE account_id = ?`), id).Scan(&n)
		return n, err
	}

	for _, p := range pairs {
		newID := accounts.ID(model.Provider(p.provider), into)
		var clash int
		// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
		if err := tx.QueryRow(d.Rebind(`SELECT COUNT(*) FROM accounts WHERE id = ?`), newID).Scan(&clash); err != nil {
			return err
		}
		oldState, err := countState(p.oldID)
		if err != nil {
			return err
		}

		if clash > 0 {
			targetState, err := countState(newID)
			if err != nil {
				return err
			}
			if len(haveState) > 0 {
				// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
				if _, err := tx.Exec(d.Rebind(`DELETE FROM sync_state WHERE account_id = ?`), p.oldID); err != nil {
					return fmt.Errorf("merge-rider: dropping %s's sync state: %w", p.oldID, err)
				}
			}
			// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
			if _, err := tx.Exec(d.Rebind(`DELETE FROM accounts WHERE id = ?`), p.oldID); err != nil {
				return fmt.Errorf("merge-rider: dropping account %s: %w", p.oldID, err)
			}
			sum.Dropped = append(sum.Dropped, MergeDrop{Table: "accounts", What: p.provider,
				Consequence: fmt.Sprintf("the %s head unit linked as %s is unlinked (%s's own %s account is kept): relink it under %s if it is a different device", p.provider, from, into, p.provider, into)})
			sum.add("accounts", MergeCounts{KeptTarget: 1, DroppedSource: 1})
			sum.add("sync_state", MergeCounts{KeptTarget: targetState, DroppedSource: oldState})
			continue
		}

		// The target has no account here, but may still hold sync state left
		// behind by one that was unlinked: on a slug both have, the target's is kept.
		var droppedState, keptState int
		if len(haveState) > 0 {
			slugs := `FROM sync_state WHERE account_id = ? AND slug IN (SELECT slug FROM sync_state WHERE account_id = ?)`
			// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
			if err := tx.QueryRow(d.Rebind(`SELECT COUNT(*) `+slugs), p.oldID, newID).Scan(&droppedState); err != nil {
				return err
			}
			keptState = droppedState
			if droppedState > 0 {
				// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
				if _, err := tx.Exec(d.Rebind(`DELETE `+slugs), p.oldID, newID); err != nil {
					return fmt.Errorf("merge-rider: dropping %s's sync state that %s already has: %w", p.oldID, newID, err)
				}
			}
		}
		// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
		if _, err := tx.Exec(d.Rebind(`UPDATE accounts SET id = ?, rider = ? WHERE id = ?`), newID, into, p.oldID); err != nil {
			return fmt.Errorf("merge-rider: moving account %s: %w", p.oldID, err)
		}
		if len(haveState) > 0 {
			// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
			if _, err := tx.Exec(d.Rebind(`UPDATE sync_state SET account_id = ? WHERE account_id = ?`), newID, p.oldID); err != nil {
				return fmt.Errorf("merge-rider: moving sync state of %s: %w", p.oldID, err)
			}
		}
		sum.add("accounts", MergeCounts{Moved: 1})
		sum.add("sync_state", MergeCounts{Moved: oldState - droppedState, KeptTarget: keptState, DroppedSource: droppedState})
	}
	return nil
}

// mergeProviderLinks merges the provider sign-ins, one per provider and rider:
// the target's is kept, and the source's (a sealed token for some other login)
// is dropped.
func mergeProviderLinks(tx *sql.Tx, d dbx.Dialect, from, into string, sum *MergeSummary) error {
	have, err := existingColumns(tx, d, "provider_links")
	if err != nil {
		return fmt.Errorf("merge-rider: reading the provider_links schema: %w", err)
	}
	if len(have) == 0 {
		return nil
	}
	var moved, clash int
	// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
	if err := tx.QueryRow(d.Rebind(`SELECT COUNT(*) FROM provider_links WHERE rider = ?`), from).Scan(&moved); err != nil {
		return err
	}
	if moved == 0 {
		return nil
	}
	const colliding = `FROM provider_links WHERE rider = ? AND provider IN (SELECT provider FROM provider_links WHERE rider = ?)`
	// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
	if err := tx.QueryRow(d.Rebind(`SELECT COUNT(*) `+colliding), from, into).Scan(&clash); err != nil {
		return err
	}
	if clash > 0 {
		// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
		rows, err := tx.Query(d.Rebind(`SELECT provider `+colliding+` ORDER BY provider`), from, into)
		if err != nil {
			return err
		}
		for rows.Next() {
			var provider string
			if err := rows.Scan(&provider); err != nil {
				_ = rows.Close()
				return err
			}
			sum.Dropped = append(sum.Dropped, MergeDrop{Table: "provider_links", What: provider,
				Consequence: fmt.Sprintf("the %s sign-in of %s is dropped (%s's own is kept): if %s's has expired, reconnect %s", provider, from, into, into, provider)})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
		if _, err := tx.Exec(d.Rebind(`DELETE `+colliding), from, into); err != nil {
			return fmt.Errorf("merge-rider: dropping %s's provider sign-ins that %s already has: %w", from, into, err)
		}
	}
	// #nosec G701 -- constant statement or one built from riderTables constants; riders are bound.
	if _, err := tx.Exec(d.Rebind(`UPDATE provider_links SET rider = ? WHERE rider = ?`), into, from); err != nil {
		return fmt.Errorf("merge-rider: moving provider sign-ins: %w", err)
	}
	sum.add("provider_links", MergeCounts{Moved: moved - clash, KeptTarget: clash, DroppedSource: clash})
	return nil
}
