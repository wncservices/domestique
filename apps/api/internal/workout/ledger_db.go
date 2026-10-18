package workout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// LedgerWeek is one rider's TSS ledger for one plan week (Monday-keyed).
type LedgerWeek struct {
	Rider, WeekStart        string
	TargetTSS, CarriedInTSS float64
	// OpenDays is how many of the rider's available days were not blacked out
	// when the week opened; 0 for a row that predates the column.
	OpenDays int
	// Reduced is set for a recovery or taper week, whose shortfall is not debt.
	Reduced bool
	// UpgradedAt is the day the week's one upgrade landed, "" before it does.
	UpgradedAt   string
	ForgivenTSS  float64
	LastPassDate string
}

// GetLedgerWeek returns the row, or ok=false when the week was never opened.
func (d *DB) GetLedgerWeek(ctx context.Context, rider, weekStart string) (LedgerWeek, bool, error) {
	var w LedgerWeek
	err := d.db.QueryRowContext(ctx, d.query(`
SELECT rider, week_start, target_tss, carried_in_tss, open_days, reduced, upgraded_at, forgiven_tss, last_pass_date
FROM load_ledger WHERE rider = ? AND week_start = ?`), normalizeRider(rider), weekStart).
		Scan(&w.Rider, &w.WeekStart, &w.TargetTSS, &w.CarriedInTSS, &w.OpenDays, &w.Reduced, &w.UpgradedAt, &w.ForgivenTSS, &w.LastPassDate)
	if errors.Is(err, sql.ErrNoRows) {
		return LedgerWeek{}, false, nil
	}
	if err != nil {
		return LedgerWeek{}, false, fmt.Errorf("workout: get ledger week: %w", err)
	}
	return w, true, nil
}

// ClaimLedgerDay makes sure the week's row exists (created reports that this
// call made it) and takes the daily pass for day. claimed is false when the
// pass already ran that day. The claim is one compare-and-set statement, so two
// replicas cannot both win.
func (d *DB) ClaimLedgerDay(ctx context.Context, rider, weekStart, day string) (claimed, created bool, err error) {
	rider = normalizeRider(rider)
	now := d.timestamp()
	res, err := d.db.ExecContext(ctx, d.query(`
INSERT INTO load_ledger (rider, week_start, created_at, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (rider, week_start) DO NOTHING`), rider, weekStart, now, now)
	if err != nil {
		return false, false, fmt.Errorf("workout: claim ledger day: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, false, fmt.Errorf("workout: claim ledger day: %w", err)
	}
	created = n == 1
	// #nosec G701 -- constant statement, every value bound.
	res, err = d.db.ExecContext(ctx, d.query(`
UPDATE load_ledger SET last_pass_date = ?, updated_at = ?
WHERE rider = ? AND week_start = ? AND last_pass_date <> ?`), day, now, rider, weekStart, day)
	if err != nil {
		return false, created, fmt.Errorf("workout: claim ledger day: %w", err)
	}
	n, err = res.RowsAffected()
	if err != nil {
		return false, created, fmt.Errorf("workout: claim ledger day: %w", err)
	}
	return n == 1, created, nil
}

// OpenLedgerWeek records the week's target, what was carried into it and how
// many available days it had then (reduced: a recovery or taper week),
// creating the row if needed and leaving last_pass_date, upgraded_at and
// forgiven alone.
func (d *DB) OpenLedgerWeek(ctx context.Context, rider, weekStart string, target, carriedIn float64, openDays int, reduced bool) error {
	now := d.timestamp()
	_, err := d.db.ExecContext(ctx, d.query(`
INSERT INTO load_ledger (rider, week_start, target_tss, carried_in_tss, open_days, reduced, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (rider, week_start) DO UPDATE SET
    target_tss = excluded.target_tss, carried_in_tss = excluded.carried_in_tss, open_days = excluded.open_days,
    reduced = excluded.reduced, updated_at = excluded.updated_at`),
		normalizeRider(rider), weekStart, target, carriedIn, openDays, reduced, now, now)
	if err != nil {
		return fmt.Errorf("workout: open ledger week: %w", err)
	}
	return nil
}

// SetLedgerForgiven records the debt a finished week let go of. It changes
// nothing when the week was never opened.
func (d *DB) SetLedgerForgiven(ctx context.Context, rider, weekStart string, forgiven float64) error {
	_, err := d.db.ExecContext(ctx, d.query(
		`UPDATE load_ledger SET forgiven_tss = ?, updated_at = ? WHERE rider = ? AND week_start = ?`),
		forgiven, d.timestamp(), normalizeRider(rider), weekStart)
	if err != nil {
		return fmt.Errorf("workout: set ledger forgiven: %w", err)
	}
	return nil
}

// MarkLedgerUpgraded records the day the week's upgrade landed. The first day
// stays: the upgrade is once a week, so a second mark changes nothing.
func (d *DB) MarkLedgerUpgraded(ctx context.Context, rider, weekStart, day string) error {
	_, err := d.db.ExecContext(ctx, d.query(
		`UPDATE load_ledger SET upgraded_at = ?, updated_at = ? WHERE rider = ? AND week_start = ? AND upgraded_at = ''`),
		day, d.timestamp(), normalizeRider(rider), weekStart)
	if err != nil {
		return fmt.Errorf("workout: mark ledger upgraded: %w", err)
	}
	return nil
}
