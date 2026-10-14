package workout

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// FillWeek creates a plan week's sessions and records the week as filled in one
// transaction. Either every session lands together with the marker, or nothing
// does and the week stays unrecorded for the next pass to fill from scratch.
//
// The alternative, recording the marker last and letting a re-run top the week
// up, cannot work here: a week with sessions but no marker is exactly what the
// legacy-deployment check reads as "filled before scheduled_weeks existed", so
// a half-built week would be adopted as complete. A transaction needs no such
// guess, and the store already makes it cheap (DeleteRider, DeleteGoal).
//
// refreshed also stamps the week as refreshed, for a week close enough that it
// was built from current data (see MarkWeekRefreshed). The created sessions are
// returned only when the transaction committed.
func (d *DB) FillWeek(ctx context.Context, goalID, weekStart string, reqs []CreateWorkoutRequest, refreshed bool) ([]Workout, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("workout: fill week: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ids := make([]string, 0, len(reqs))
	for _, req := range reqs {
		id, err := d.insertWorkout(ctx, tx, req)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	now := d.now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, d.query(`
INSERT INTO scheduled_weeks (goal_id, week_start, created_at) VALUES (?, ?, ?)
ON CONFLICT (goal_id, week_start) DO NOTHING`), goalID, weekStart, now); err != nil {
		return nil, err
	}
	if refreshed {
		if _, err := tx.ExecContext(ctx, d.query(
			`UPDATE scheduled_weeks SET refreshed_at = ? WHERE goal_id = ? AND week_start = ? AND refreshed_at = ''`),
			now, goalID, weekStart); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("workout: fill week: %w", err)
	}
	fillWeekAfterCommit()

	// The week is stored from here on. Reading the sessions back is only to
	// tell the caller what was made, so a failure (or a context that ended with
	// the commit) is logged and what was read is returned: an error here would
	// make the caller treat a stored week as unfilled.
	readCtx := context.WithoutCancel(ctx)
	out := make([]Workout, 0, len(ids))
	for _, id := range ids {
		w, err := d.GetWorkout(readCtx, id)
		if err != nil {
			slog.Warn("fill week: committed, but a session could not be read back", "goal", goalID, "week", weekStart, "workout", id, "err", err)
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

// fillWeekAfterCommit is called right after FillWeek commits. It exists for the
// test that ends the context at that moment; production leaves it a no-op.
var fillWeekAfterCommit = func() {}
