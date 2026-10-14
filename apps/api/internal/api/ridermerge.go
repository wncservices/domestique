package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

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
}

// MergeSummary says, per table, what a merge did (or, on a dry run, would do).
// Tables with nothing in them are left out.
type MergeSummary struct {
	Tables map[string]MergeCounts
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

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return sum, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, table := range mergeTables() {
		c, err := mergeGeneric(tx, d, table, from, into)
		if err != nil {
			return sum, err
		}
		sum.add(table, c)
	}
	if err := mergeAccounts(tx, d, from, into, &sum); err != nil {
		return sum, err
	}
	if err := mergeProviderLinks(tx, d, from, into, &sum); err != nil {
		return sum, err
	}

	if opt.DryRun {
		return sum, nil // rolled back by the deferred Rollback: nothing was written
	}
	if err := tx.Commit(); err != nil {
		return sum, err
	}
	if opt.EndSessions != nil {
		for _, rider := range []string{from, into} {
			n, err := opt.EndSessions(ctx, rider)
			if err != nil {
				return sum, fmt.Errorf("merge-rider: the data is merged, but ending %s's sessions failed: %w", rider, err)
			}
			sum.SessionsEnded += n
		}
	}
	return sum, nil
}

func normalizeRiderName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// mergeTables is the registered tables a merge rewrites by column, in a stable
// order: every one with rename Columns.
func mergeTables() []string { return registeredRenames() }

// mergeGeneric merges one registry table by its rename rule.
func mergeGeneric(tx *sql.Tx, d dbx.Dialect, table, from, into string) (MergeCounts, error) {
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
	if rule.Unique && have[rule.Columns[0]] {
		key := rule.Columns[0]
		same := func(a, b string) string {
			s := ""
			for _, w := range rule.With {
				s += " AND " + a + "." + w + " = " + b + "." + w
			}
			return s
		}
		// Source rows the target already has a row for, on the whole key.
		srcCollide := `FROM ` + table + ` WHERE ` + table + `.` + key + ` = ? AND EXISTS (SELECT 1 FROM ` + table +
			` n WHERE n.` + key + ` = ?` + same("n", table) + `)`
		// #nosec G701 -- built only from riderTables constants; the riders are bound.
		if err := tx.QueryRow(d.Rebind(`SELECT COUNT(*) `+srcCollide), from, into).Scan(&c.DroppedSource); err != nil {
			return c, fmt.Errorf("merge-rider: counting collisions in %s: %w", table, err)
		}
		if c.DroppedSource > 0 {
			// The target rows those collided with, counted from the target's side.
			kept := `SELECT COUNT(*) FROM ` + table + ` n WHERE n.` + key + ` = ? AND EXISTS (SELECT 1 FROM ` + table +
				` o WHERE o.` + key + ` = ?` + same("o", "n") + `)`
			// #nosec G701 -- built only from riderTables constants; the riders are bound.
			if err := tx.QueryRow(d.Rebind(kept), into, from).Scan(&c.KeptTarget); err != nil {
				return c, fmt.Errorf("merge-rider: counting the target's rows in %s: %w", table, err)
			}
			// #nosec G701 -- built only from riderTables constants; the riders are bound.
			if _, err := tx.Exec(d.Rebind(`DELETE `+srcCollide), from, into); err != nil {
				return c, fmt.Errorf("merge-rider: dropping %s's colliding rows in %s: %w", from, table, err)
			}
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
