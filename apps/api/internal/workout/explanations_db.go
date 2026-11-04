package workout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// explanationClaimTTL is how long an empty claim blocks others. A claim is
// released on every failure the process survives; this covers the one it does
// not (a crash between claim and save), which would otherwise leave that shape
// without an explanation for good.
const explanationClaimTTL = 5 * time.Minute

// GetExplanation returns the stored explanation text for a shape key. A claim
// that was never filled reads as absent.
func (d *DB) GetExplanation(ctx context.Context, key string) (string, bool, error) {
	var text string
	err := d.db.QueryRowContext(ctx, d.query(`SELECT text FROM workout_explanations WHERE key = ?`), key).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("workout: get explanation: %w", err)
	}
	return text, text != "", nil
}

// ClaimExplanation inserts an empty row for the key and reports whether this
// call took it. One statement, so when two requests race for the same missing
// explanation exactly one makes the model call. A claim older than
// explanationClaimTTL is taken over.
func (d *DB) ClaimExplanation(ctx context.Context, key string) (bool, error) {
	now := d.now().UTC()
	res, err := d.db.ExecContext(ctx, d.query(`
INSERT INTO workout_explanations (key, text, created_at) VALUES (?, '', ?)
ON CONFLICT (key) DO UPDATE SET created_at = excluded.created_at
WHERE workout_explanations.text = '' AND workout_explanations.created_at < ?`),
		key, now.Format(time.RFC3339), now.Add(-explanationClaimTTL).Format(time.RFC3339))
	if err != nil {
		return false, fmt.Errorf("workout: claim explanation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("workout: claim explanation: %w", err)
	}
	return n == 1, nil
}

// SaveExplanation stores the text, filling a claim or replacing a row.
func (d *DB) SaveExplanation(ctx context.Context, key, text string) error {
	_, err := d.db.ExecContext(ctx, d.query(`
INSERT INTO workout_explanations (key, text, created_at) VALUES (?, ?, ?)
ON CONFLICT (key) DO UPDATE SET text = excluded.text`),
		key, text, d.timestamp())
	if err != nil {
		return fmt.Errorf("workout: save explanation: %w", err)
	}
	return nil
}

// ReleaseExplanation deletes a claim that never got its text. An explanation
// that has text is left alone.
func (d *DB) ReleaseExplanation(ctx context.Context, key string) error {
	_, err := d.db.ExecContext(ctx, d.query(`DELETE FROM workout_explanations WHERE key = ? AND text = ''`), key)
	if err != nil {
		return fmt.Errorf("workout: release explanation: %w", err)
	}
	return nil
}
