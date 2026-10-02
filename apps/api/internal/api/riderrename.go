package api

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// RenameOptions are the flags of the rename-rider command.
type RenameOptions struct {
	// DryRun counts and checks everything a real run would and writes nothing.
	DryRun bool
	// Replace resolves a conflict in accounts, sync state and provider sign-ins
	// by deleting the new rider's row and keeping the old rider's. It never
	// reaches any other table: a collision there (health data, a profile, a
	// crew membership) is always refused, because deleting someone's wellness
	// history is not something a flag on a rename should do quietly.
	Replace bool
}

// RenameSummary is how many rows carried the old rider, counted whether or not
// the run wrote anything, so a dry run reports what a real one would.
type RenameSummary struct {
	// Tables is the rows renamed per registry table (riderTables), for every
	// table that has a rider column rewritten generically.
	Tables map[string]int
	// Accounts, SyncState and ProviderLinks are the three tables RenameRider
	// carries by hand (a derived id, and --replace).
	Accounts, SyncState, ProviderLinks int
	// Replaced* are what --replace deleted on the new rider's side; zero
	// whenever it was not asked for.
	ReplacedAccounts, ReplacedSyncState, ReplacedProviderLinks int
}

// RenameRider moves every row that names old onto next, across every table in
// riderTables, in one transaction: a failure part-way, or a collision, leaves
// the database as it was.
//
// It is driven by the registry the purge is, so a table added to the schema
// cannot be missed here either: TestEveryRegisteredTableHasARenameRule fails
// until its rule says what happens to it. Before this the rename carried
// routes, accounts, sync state and provider sign-ins by hand and left every
// training table under the old name.
//
// Both names must already be normalised (lower-case, trimmed). Tables or
// columns the database does not have yet (a deployment that has not started
// the newer image since an upgrade) are skipped rather than failed on: there
// is nothing in them to rename.
func RenameRider(db *sql.DB, d dbx.Dialect, old, next string, opt RenameOptions) (RenameSummary, error) {
	sum := RenameSummary{Tables: map[string]int{}}

	tx, err := db.Begin()
	if err != nil {
		return sum, err
	}
	defer func() { _ = tx.Rollback() }()

	// Collisions first, across every table, so a refusal names the table and
	// happens before any statement that writes.
	if err := checkRenameCollisions(tx, d, old, next); err != nil {
		return sum, err
	}
	if err := renameGeneric(tx, d, old, next, opt.DryRun, &sum); err != nil {
		return sum, err
	}
	if err := renameBespoke(tx, d, old, next, opt, &sum); err != nil {
		return sum, err
	}

	if opt.DryRun {
		return sum, nil // rolled back by the deferred Rollback; nothing was ever written
	}
	if err := tx.Commit(); err != nil {
		return sum, err
	}
	return sum, nil
}

// registeredRenames is the registry's rename rules, in a stable order.
func registeredRenames() []string {
	names := make([]string, 0, len(riderTables))
	for table, rt := range riderTables {
		if len(rt.Rename.Columns) > 0 {
			names = append(names, table)
		}
	}
	sort.Strings(names)
	return names
}

// existingColumns is the columns table has now; empty when it does not exist.
// A missing table has to be asked about, not queried: on PostgreSQL a query
// against it would abort the whole transaction.
func existingColumns(tx *sql.Tx, d dbx.Dialect, table string) (map[string]bool, error) {
	q := `SELECT name FROM pragma_table_info(?)`
	if d.Name == dbx.Postgres.Name {
		q = `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?`
	}
	// #nosec G701 -- constant statement, bound parameter.
	rows, err := tx.Query(d.Rebind(q), table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols[c] = true
	}
	return cols, rows.Err()
}

// liveColumns is the rule's columns the table actually has.
func liveColumns(have map[string]bool, rule renameRule) []string {
	var cols []string
	for _, c := range rule.Columns {
		if have[c] {
			cols = append(cols, c)
		}
	}
	return cols
}

// checkRenameCollisions refuses the rename when the new rider already holds a
// row that the renamed one would land on: the same unique key with only the
// rider differing. There is no merge: which of two riders' wellness for one
// day is right is not a question a rename can answer.
func checkRenameCollisions(tx *sql.Tx, d dbx.Dialect, old, next string) error {
	for _, table := range registeredRenames() {
		rule := riderTables[table].Rename
		if !rule.Unique {
			continue
		}
		have, err := existingColumns(tx, d, table)
		if err != nil {
			return fmt.Errorf("rename-rider: reading the %s schema: %w", table, err)
		}
		if len(have) == 0 || !have[rule.Columns[0]] {
			continue
		}
		key := rule.Columns[0]
		same := ""
		for _, c := range rule.With {
			same += " AND o." + c + " = n." + c
		}
		// Table and column names are constants from riderTables, never input.
		// #nosec G701
		q := `SELECT COUNT(*) FROM ` + table + ` n WHERE n.` + key + ` = ? AND EXISTS (SELECT 1 FROM ` + table + ` o WHERE o.` + key + ` = ?` + same + `)`
		var n int
		// #nosec G701 -- q is built only from riderTables constants.
		if err := tx.QueryRow(d.Rebind(q), next, old).Scan(&n); err != nil {
			return fmt.Errorf("rename-rider: checking %s for a collision: %w", table, err)
		}
		if n > 0 {
			return fmt.Errorf("rename-rider: %s already has %d row(s) in %s that would collide with %s's — "+
				"nothing was changed; resolve that by hand, then retry", next, n, table, old)
		}
	}
	return nil
}

// renameGeneric rewrites every registered rider column from old to next.
func renameGeneric(tx *sql.Tx, d dbx.Dialect, old, next string, dryRun bool, sum *RenameSummary) error {
	for _, table := range registeredRenames() {
		rule := riderTables[table].Rename
		have, err := existingColumns(tx, d, table)
		if err != nil {
			return fmt.Errorf("rename-rider: reading the %s schema: %w", table, err)
		}
		cols := liveColumns(have, rule)
		if len(cols) == 0 {
			continue
		}

		// Rows that name the rider in any of the columns, counted once.
		var conds []string
		var args []any
		for _, c := range cols {
			conds = append(conds, c+" = ?")
			args = append(args, old)
		}
		// #nosec G701 -- table and column names are constants from riderTables.
		q := `SELECT COUNT(*) FROM ` + table + ` WHERE ` + strings.Join(conds, " OR ")
		var n int
		// #nosec G701 -- q is built only from riderTables constants.
		if err := tx.QueryRow(d.Rebind(q), args...).Scan(&n); err != nil {
			return fmt.Errorf("rename-rider: counting %s: %w", table, err)
		}
		if n == 0 {
			continue
		}
		sum.Tables[table] = n
		if dryRun {
			continue
		}
		for _, c := range cols {
			// #nosec G701 -- table and column names are constants from riderTables.
			if _, err := tx.Exec(d.Rebind(`UPDATE `+table+` SET `+c+` = ? WHERE `+c+` = ?`), next, old); err != nil {
				return fmt.Errorf("rename-rider: renaming %s.%s: %w", table, c, err)
			}
		}
	}
	return nil
}

// renameBespoke carries the three tables a column rewrite cannot: accounts
// (whose id is "<provider>:<rider>"), sync_state (which holds that id) and
// provider_links, whose conflicts --replace can resolve.
func renameBespoke(tx *sql.Tx, d dbx.Dialect, old, next string, opt RenameOptions, sum *RenameSummary) error {
	dryRun, replace := opt.DryRun, opt.Replace
	// accounts.id is a derived composite key ("<provider>:<rider>"), and
	// sync_state.account_id is that same string as half of its own primary
	// key — so an account and its sync state have to be renamed together, or
	// the sync state is silently orphaned and every route looks like it needs
	// pushing again from scratch.
	// #nosec G701
	acctRows, err := tx.Query(d.Rebind(`SELECT id, provider FROM accounts WHERE rider = ?`), old)
	if err != nil {
		return fmt.Errorf("reading %s's accounts: %w", old, err)
	}
	type acctPair struct{ oldID, provider string }
	var pairs []acctPair
	for acctRows.Next() {
		var p acctPair
		if err := acctRows.Scan(&p.oldID, &p.provider); err != nil {
			_ = acctRows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	if err := acctRows.Err(); err != nil {
		_ = acctRows.Close()
		return err
	}
	_ = acctRows.Close()

	for _, p := range pairs {
		newID := accounts.ID(model.Provider(p.provider), next)

		var conflict int
		// #nosec G701
		if err := tx.QueryRow(d.Rebind(`SELECT COUNT(1) FROM accounts WHERE id = ?`), newID).
			Scan(&conflict); err != nil {
			return err
		}
		if conflict > 0 {
			if !replace {
				return fmt.Errorf(
					"rename-rider: %s already has a %s account (%s) — resolve that conflict first, then retry",
					next, p.provider, newID)
			}
			// --replace's contract: the old rider's row wins, so the new
			// rider's conflicting account (and whatever sync state still
			// points at it) is deleted first, clearing the id for the
			// UPDATE below to claim.
			var staleStateRows int
			// #nosec G701
			if err := tx.QueryRow(d.Rebind(`SELECT COUNT(1) FROM sync_state WHERE account_id = ?`), newID).
				Scan(&staleStateRows); err != nil {
				return err
			}
			sum.ReplacedAccounts++
			sum.ReplacedSyncState += staleStateRows
			if !dryRun {
				// #nosec G701
				if _, err := tx.Exec(d.Rebind(`DELETE FROM sync_state WHERE account_id = ?`), newID); err != nil {
					return fmt.Errorf("clearing %s's stale sync state: %w", newID, err)
				}
				// #nosec G701
				if _, err := tx.Exec(d.Rebind(`DELETE FROM accounts WHERE id = ?`), newID); err != nil {
					return fmt.Errorf("clearing %s's stale account: %w", newID, err)
				}
			}
		}

		// A slug can still collide even with no accounts-row conflict above:
		// an account deleted (unlinked) rather than renamed leaves its
		// sync_state rows orphaned — nothing references them, but they keep
		// existing, sitting at the same (account_id, slug) primary key the
		// old rider's own rows are about to move into. The accounts-conflict
		// branch above only clears sync_state that belonged to a *deleted*
		// conflicting account; this is the same problem with no account left
		// to have flagged it. Checked unconditionally, not only when the
		// accounts check found nothing, since an accounts row that does
		// still exist was already handled — and already cleared — above.
		// #nosec G701
		orphanRows, err := tx.Query(d.Rebind(
			`SELECT slug FROM sync_state WHERE account_id = ? AND slug IN
			 (SELECT slug FROM sync_state WHERE account_id = ?)`), newID, p.oldID)
		if err != nil {
			return err
		}
		var orphanSlugs []string
		for orphanRows.Next() {
			var slug string
			if err := orphanRows.Scan(&slug); err != nil {
				_ = orphanRows.Close()
				return err
			}
			orphanSlugs = append(orphanSlugs, slug)
		}
		if err := orphanRows.Err(); err != nil {
			_ = orphanRows.Close()
			return err
		}
		_ = orphanRows.Close()
		if len(orphanSlugs) > 0 {
			if !replace {
				return fmt.Errorf(
					"rename-rider: %s already has %d orphaned sync state row(s) under %s that collide by slug — resolve that conflict first, then retry",
					next, len(orphanSlugs), newID)
			}
			sum.ReplacedSyncState += len(orphanSlugs)
			if !dryRun {
				for _, slug := range orphanSlugs {
					// #nosec G701
					if _, err := tx.Exec(d.Rebind(`DELETE FROM sync_state WHERE account_id = ? AND slug = ?`),
						newID, slug); err != nil {
						return fmt.Errorf("clearing %s's orphaned sync state for %s: %w", newID, slug, err)
					}
				}
			}
		}

		var stateRows int
		// #nosec G701
		if err := tx.QueryRow(d.Rebind(`SELECT COUNT(1) FROM sync_state WHERE account_id = ?`), p.oldID).
			Scan(&stateRows); err != nil {
			return err
		}
		sum.Accounts++
		sum.SyncState += stateRows

		if dryRun {
			continue
		}
		// #nosec G701
		if _, err := tx.Exec(d.Rebind(`UPDATE accounts SET id = ?, rider = ? WHERE id = ?`),
			newID, next, p.oldID); err != nil {
			return fmt.Errorf("renaming account %s: %w", p.oldID, err)
		}
		if stateRows > 0 {
			// #nosec G701
			if _, err := tx.Exec(d.Rebind(`UPDATE sync_state SET account_id = ? WHERE account_id = ?`),
				newID, p.oldID); err != nil {
				return fmt.Errorf("renaming sync state for %s: %w", p.oldID, err)
			}
		}
	}

	// provider_links: composite key (provider, rider), no derived id to carry
	// anywhere else.
	// #nosec G701
	linkRows, err := tx.Query(d.Rebind(`SELECT provider FROM provider_links WHERE rider = ?`), old)
	if err != nil {
		return fmt.Errorf("reading %s's provider sign-ins: %w", old, err)
	}
	var providers []string
	for linkRows.Next() {
		var provider string
		if err := linkRows.Scan(&provider); err != nil {
			_ = linkRows.Close()
			return err
		}
		providers = append(providers, provider)
	}
	if err := linkRows.Err(); err != nil {
		_ = linkRows.Close()
		return err
	}
	_ = linkRows.Close()

	for _, provider := range providers {
		var conflict int
		// #nosec G701
		if err := tx.QueryRow(d.Rebind(`SELECT COUNT(1) FROM provider_links WHERE provider = ? AND rider = ?`),
			provider, next).Scan(&conflict); err != nil {
			return err
		}
		if conflict > 0 {
			if !replace {
				return fmt.Errorf(
					"rename-rider: %s already has a %s sign-in — resolve that conflict first, then retry",
					next, provider)
			}
			sum.ReplacedProviderLinks++
			if !dryRun {
				// #nosec G701
				if _, err := tx.Exec(d.Rebind(`DELETE FROM provider_links WHERE provider = ? AND rider = ?`),
					provider, next); err != nil {
					return fmt.Errorf("clearing %s's stale %s sign-in: %w", next, provider, err)
				}
			}
		}
	}
	sum.ProviderLinks = len(providers)
	if !dryRun && len(providers) > 0 {
		// #nosec G701
		if _, err := tx.Exec(d.Rebind(`UPDATE provider_links SET rider = ? WHERE rider = ?`),
			next, old); err != nil {
			return fmt.Errorf("renaming provider sign-ins: %w", err)
		}
	}
	return nil
}
