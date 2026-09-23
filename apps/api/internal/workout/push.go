package workout

import (
	"context"
	"database/sql"
	"errors"
)

// GetPush returns the record of a workout's copy on a provider, and whether
// there is one.
func (d *DB) GetPush(ctx context.Context, workoutID, provider string) (Push, bool, error) {
	var p Push
	err := d.db.QueryRowContext(ctx, d.query(`
        SELECT workout_id, provider, remote_id, schedule_id, scheduled_date, content_hash, pushed_at
        FROM workout_pushes WHERE workout_id = ? AND provider = ?`), workoutID, provider).Scan(
		&p.WorkoutID, &p.Provider, &p.RemoteID, &p.ScheduleID, &p.ScheduledDate, &p.ContentHash, &p.PushedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Push{}, false, nil
	}
	if err != nil {
		return Push{}, false, err
	}
	return p, true, nil
}

// SavePush upserts the record: a workout has at most one copy per provider,
// and recording a first push or a later update is the same write.
func (d *DB) SavePush(ctx context.Context, p Push) error {
	p.PushedAt = timestamp()
	_, err := d.db.ExecContext(ctx, d.query(`
        INSERT INTO workout_pushes (workout_id, provider, remote_id, schedule_id, scheduled_date, content_hash, pushed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (workout_id, provider) DO UPDATE SET
            remote_id = excluded.remote_id, schedule_id = excluded.schedule_id,
            scheduled_date = excluded.scheduled_date, content_hash = excluded.content_hash,
            pushed_at = excluded.pushed_at`),
		p.WorkoutID, p.Provider, p.RemoteID, p.ScheduleID, p.ScheduledDate, p.ContentHash, p.PushedAt)
	return err
}

// DeletePush forgets a workout's copy on a provider.
func (d *DB) DeletePush(ctx context.Context, workoutID, provider string) error {
	_, err := d.db.ExecContext(ctx, d.query(`DELETE FROM workout_pushes WHERE workout_id = ? AND provider = ?`), workoutID, provider)
	return err
}

// ListAutoPushRiders returns every rider who has opted in to having their
// scheduled workouts placed on their devices automatically.
func (d *DB) ListAutoPushRiders(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`SELECT rider FROM rider_profiles WHERE auto_push_workouts = ? ORDER BY rider`), true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var riders []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		riders = append(riders, r)
	}
	return riders, rows.Err()
}
