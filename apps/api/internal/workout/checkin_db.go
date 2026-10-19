package workout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CheckIn is a rider's own answer to the morning questions. A score of 0
// means "not answered"; the API validates the 1-5 range and the note length.
type CheckIn struct {
	Rider, Date              string
	Legs, Mood, SleepQuality int
	Note                     string
	UpdatedAt                string
}

// SaveCheckIn upserts on (rider, date): answering again replaces the day's
// answers, it does not accumulate.
func (d *DB) SaveCheckIn(ctx context.Context, c CheckIn) error {
	rider := normalizeRider(c.Rider)
	if rider == "" {
		return errors.New("workout: no rider — whose check-in is this?")
	}
	now := d.timestamp()
	_, err := d.db.ExecContext(ctx, d.query(`
INSERT INTO daily_checkins (rider, date, legs, mood, sleep_quality, note, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (rider, date) DO UPDATE SET
    legs = excluded.legs, mood = excluded.mood, sleep_quality = excluded.sleep_quality,
    note = excluded.note, updated_at = excluded.updated_at`),
		rider, c.Date, c.Legs, c.Mood, c.SleepQuality, c.Note, now, now)
	if err != nil {
		return fmt.Errorf("workout: save check-in: %w", err)
	}
	return nil
}

// GetCheckIn returns the day's answers, or ok=false when none were given.
func (d *DB) GetCheckIn(ctx context.Context, rider, date string) (CheckIn, bool, error) {
	var c CheckIn
	err := d.db.QueryRowContext(ctx, d.query(`
SELECT rider, date, legs, mood, sleep_quality, note, updated_at
FROM daily_checkins WHERE rider = ? AND date = ?`), normalizeRider(rider), date).
		Scan(&c.Rider, &c.Date, &c.Legs, &c.Mood, &c.SleepQuality, &c.Note, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckIn{}, false, nil
	}
	if err != nil {
		return CheckIn{}, false, fmt.Errorf("workout: get check-in: %w", err)
	}
	return c, true, nil
}

// ListCheckIns returns the rider's answers dated on or after sinceDate,
// oldest first.
func (d *DB) ListCheckIns(ctx context.Context, rider, sinceDate string) ([]CheckIn, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
SELECT rider, date, legs, mood, sleep_quality, note, updated_at
FROM daily_checkins WHERE rider = ? AND date >= ? ORDER BY date`), normalizeRider(rider), sinceDate)
	if err != nil {
		return nil, fmt.Errorf("workout: list check-ins: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CheckIn
	for rows.Next() {
		var c CheckIn
		if err := rows.Scan(&c.Rider, &c.Date, &c.Legs, &c.Mood, &c.SleepQuality, &c.Note, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("workout: list check-ins: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
