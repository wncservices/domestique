package workout

import (
	"context"
	"database/sql"
	"errors"
)

// ErrLifeEventNotFound is returned for a life event that does not exist or is
// not the caller's: another rider's event is indistinguishable from a missing
// one, so its existence is never confirmed.
var ErrLifeEventNotFound = errors.New("no such life event")

// LifeEvent is a stretch of days a rider is away from training: travel,
// illness, a busy spell. The rules that turn one into plan changes live in
// internal/lifeevents, which re-exports this as lifeevents.Event; the type is
// here because that package imports this one.
type LifeEvent struct {
	ID    string
	Rider string
	// Kind is travel, illness, busy or other.
	Kind string
	// Start and End are inclusive, "YYYY-MM-DD".
	Start string
	End   string
	// Option is the kind's own choice (no_bike or gym; mild or proper), empty
	// for busy and other.
	Option string
	// Note is the rider's own text. It is never sent to a model and never
	// logged.
	Note      string
	CreatedAt string
	UpdatedAt string
}

const lifeEventColumns = `id, rider, kind, start_date, end_date, option, note, created_at, updated_at`

func scanLifeEvent(row rowScanner) (LifeEvent, error) {
	var e LifeEvent
	err := row.Scan(&e.ID, &e.Rider, &e.Kind, &e.Start, &e.End, &e.Option, &e.Note, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

// ListLifeEvents returns rider's events, oldest start first. A non-empty
// fromDate keeps only the events that end on or after it, so the Plan page
// does not carry every trip the rider ever took.
func (d *DB) ListLifeEvents(ctx context.Context, rider, fromDate string) ([]LifeEvent, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT `+lifeEventColumns+` FROM life_events
        WHERE rider = ? AND end_date >= ?
        ORDER BY start_date, id`), normalizeRider(rider), fromDate)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []LifeEvent
	for rows.Next() {
		e, err := scanLifeEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetLifeEvent returns rider's event id, ErrLifeEventNotFound when there is
// no such event of theirs.
func (d *DB) GetLifeEvent(ctx context.Context, rider, id string) (LifeEvent, error) {
	e, err := scanLifeEvent(d.db.QueryRowContext(ctx, d.query(
		`SELECT `+lifeEventColumns+` FROM life_events WHERE id = ? AND rider = ?`), id, normalizeRider(rider)))
	if errors.Is(err, sql.ErrNoRows) {
		return LifeEvent{}, ErrLifeEventNotFound
	}
	return e, err
}

// CreateLifeEvent stores e for e.Rider and returns it with its id and
// timestamps. The rules (limits, overlap) are the caller's: this only stores.
func (d *DB) CreateLifeEvent(ctx context.Context, e LifeEvent) (LifeEvent, error) {
	rider := normalizeRider(e.Rider)
	if rider == "" {
		return LifeEvent{}, errors.New("workout: no rider — who is this life event for?")
	}
	id, err := d.uniqueID(ctx, "life_events", e.Kind+"-"+e.Start)
	if err != nil {
		return LifeEvent{}, err
	}
	ts := timestamp()
	_, err = d.db.ExecContext(ctx, d.query(`
        INSERT INTO life_events (`+lifeEventColumns+`)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, rider, e.Kind, e.Start, e.End, e.Option, e.Note, ts, ts)
	if err != nil {
		return LifeEvent{}, err
	}
	return d.GetLifeEvent(ctx, rider, id)
}

// UpdateLifeEvent replaces the editable fields of rider's event id: kind,
// dates, option and note.
func (d *DB) UpdateLifeEvent(ctx context.Context, rider, id string, e LifeEvent) (LifeEvent, error) {
	result, err := d.db.ExecContext(ctx, d.query(`
        UPDATE life_events SET kind = ?, start_date = ?, end_date = ?, option = ?, note = ?, updated_at = ?
        WHERE id = ? AND rider = ?`),
		e.Kind, e.Start, e.End, e.Option, e.Note, timestamp(), id, normalizeRider(rider))
	if err != nil {
		return LifeEvent{}, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return LifeEvent{}, ErrLifeEventNotFound
	}
	return d.GetLifeEvent(ctx, rider, id)
}

// DeleteLifeEvent removes rider's event id.
func (d *DB) DeleteLifeEvent(ctx context.Context, rider, id string) error {
	result, err := d.db.ExecContext(ctx, d.query(
		`DELETE FROM life_events WHERE id = ? AND rider = ?`), id, normalizeRider(rider))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrLifeEventNotFound
	}
	return nil
}
