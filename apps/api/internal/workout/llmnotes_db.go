package workout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The kinds of model-written note. changes explains a day's plan changes;
// weekly summarises a plan week (keyed by its Monday).
const (
	LLMNoteChanges = "changes"
	LLMNoteWeekly  = "weekly"
)

// SaveLLMNote stores the text of a note, replacing any text already there.
// It also fills a row ClaimLLMNote made.
func (d *DB) SaveLLMNote(ctx context.Context, rider, kind, day, text string) error {
	rider = normalizeRider(rider)
	if rider == "" {
		return errors.New("workout: no rider — whose note is this?")
	}
	_, err := d.db.ExecContext(ctx, d.query(`
INSERT INTO llm_notes (rider, kind, day, text, created_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (rider, kind, day) DO UPDATE SET text = excluded.text`),
		rider, kind, day, text, d.timestamp())
	if err != nil {
		return fmt.Errorf("workout: save llm note: %w", err)
	}
	return nil
}

// GetLLMNote returns the note's text. A note with no text, claimed but never
// written, or never made, reads as ok=false.
func (d *DB) GetLLMNote(ctx context.Context, rider, kind, day string) (string, bool, error) {
	var text string
	err := d.db.QueryRowContext(ctx, d.query(`
SELECT text FROM llm_notes WHERE rider = ? AND kind = ? AND day = ?`),
		normalizeRider(rider), kind, day).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("workout: get llm note: %w", err)
	}
	if text == "" {
		return "", false, nil
	}
	return text, true, nil
}

// ClaimLLMNote inserts an empty row for the note and reports whether this call
// inserted it. It is one statement, so when two replicas race for the same
// note exactly one wins and makes the model call.
func (d *DB) ClaimLLMNote(ctx context.Context, rider, kind, day string) (bool, error) {
	res, err := d.db.ExecContext(ctx, d.query(`
INSERT INTO llm_notes (rider, kind, day, text, created_at) VALUES (?, ?, ?, '', ?)
ON CONFLICT (rider, kind, day) DO NOTHING`),
		normalizeRider(rider), kind, day, d.timestamp())
	if err != nil {
		return false, fmt.Errorf("workout: claim llm note: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("workout: claim llm note: %w", err)
	}
	return n == 1, nil
}

// PruneLLMNotes deletes every note dated before day (YYYY-MM-DD), for every
// rider. Notes are only shown for recent days, so older ones are dropped.
func (d *DB) PruneLLMNotes(ctx context.Context, before string) error {
	_, err := d.db.ExecContext(ctx, d.query(`DELETE FROM llm_notes WHERE day < ?`), before)
	if err != nil {
		return fmt.Errorf("workout: prune llm notes: %w", err)
	}
	return nil
}
