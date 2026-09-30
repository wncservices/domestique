package workout

import (
	"context"
	"database/sql"
	"errors"
)

// ErrSessionNotFound is GetSession's answer for an id that is not on file.
var ErrSessionNotFound = errors.New("workout: no such session")

// GetSession returns one completed session by id.
func (d *DB) GetSession(ctx context.Context, id string) (CompletedSession, error) {
	s, err := d.getSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return CompletedSession{}, ErrSessionNotFound
	}
	return s, err
}

// SetSessionLink records which planned workout a ride was, as the rider said
// it: workoutID "" means "not a planned session". It replaces any earlier
// link for the same ride.
func (d *DB) SetSessionLink(ctx context.Context, rider, sessionID, workoutID string) error {
	rider = normalizeRider(rider)
	if rider == "" || sessionID == "" {
		return errors.New("workout: a session link needs a rider and a session")
	}
	_, err := d.db.ExecContext(ctx, d.query(`
        INSERT INTO session_links (session_id, rider, workout_id, created_at)
        VALUES (?, ?, ?, ?)
        ON CONFLICT (session_id) DO UPDATE SET
            rider = excluded.rider, workout_id = excluded.workout_id, created_at = excluded.created_at`),
		sessionID, rider, workoutID, timestamp())
	return err
}

// ClearSessionLink hands a ride back to the automatic match. Idempotent.
func (d *DB) ClearSessionLink(ctx context.Context, sessionID string) error {
	_, err := d.db.ExecContext(ctx, d.query(`DELETE FROM session_links WHERE session_id = ?`), sessionID)
	return err
}

// SessionLinks returns every ride the rider linked by hand: session id to
// workout id, "" for "not a planned session".
func (d *DB) SessionLinks(ctx context.Context, rider string) (map[string]string, error) {
	rows, err := d.db.QueryContext(ctx, d.query(
		`SELECT session_id, workout_id FROM session_links WHERE rider = ?`), normalizeRider(rider))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var sessionID, workoutID string
		if err := rows.Scan(&sessionID, &workoutID); err != nil {
			return nil, err
		}
		out[sessionID] = workoutID
	}
	return out, rows.Err()
}

// DeleteAnalysis forgets a ride's analysis so the next analysis pass scores
// it afresh. Idempotent.
func (d *DB) DeleteAnalysis(ctx context.Context, sessionID string) error {
	_, err := d.db.ExecContext(ctx, d.query(`DELETE FROM session_analyses WHERE session_id = ?`), sessionID)
	return err
}
