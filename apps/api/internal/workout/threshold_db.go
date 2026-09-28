package workout

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// thresholdTimestamp is nanosecond-precision, unlike this package's shared
// timestamp() (second-precision RFC3339) — ListPendingSuggestions orders by
// created_at DESC to mean "most recently created first", and two
// suggestions created within the same sync run (or the same test) need a
// created_at that actually orders them, not one that ties and falls back to
// comparing random ids.
func thresholdTimestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Threshold suggestion status values — see ThresholdSuggestion's own doc
// comment and docs/superpowers/specs/2026-09-28-threshold-detection-design.md's
// "Data" section.
const (
	ThresholdPending   = "pending"
	ThresholdAccepted  = "accepted"
	ThresholdDismissed = "dismissed"
)

// ErrThresholdSuggestionNotFound is returned for a suggestion id nothing
// matches.
var ErrThresholdSuggestionNotFound = errors.New("no such threshold suggestion")

// ThresholdSuggestion is one internal/thresholds finding stored for a rider
// to act on — the rider-typed-field counterpart to an Auto finding, which
// is applied straight to the profile and never stored here at all. Field is
// "ftp" | "max_hr" | "threshold_pace", matching thresholds.Finding.Field and
// (for the latter two) this package's own FieldMaxHR/FieldThresholdPace
// constants.
type ThresholdSuggestion struct {
	ID       string
	Rider    string
	Field    string
	Value    float64
	Previous float64
	// Direction is "up" or "down", matching thresholds.Finding.Direction —
	// what tells the dismissed-suggestion gate an up finding apart from a
	// down dismissal, and vice versa: those are unrelated claims ("FTP
	// rose" and "FTP dropped"), never the same estimate moving further in
	// one direction. A row written before this column existed backfills to
	// "up" (see addThresholdDirectionColumn) — the common case, and safe
	// either way: at worst a pre-migration down suggestion is briefly
	// treated as an up one for gating purposes until it is next replaced.
	Direction       string
	SourceSessionID string
	SourceDate      string
	Reason          string
	// Status is one of the Threshold* constants above.
	Status    string
	CreatedAt string
	UpdatedAt string
}

// newThresholdSuggestionID mirrors internal/schedule's own newID: 16 random
// bytes, hex-encoded. Not a slug — a suggestion has no rider-chosen name to
// derive one from, and successive suggestions for the same rider/field are
// meant to keep distinct ids across history (pending, then dismissed, then
// a later pending again), never collide on one.
func newThresholdSuggestionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate threshold suggestion id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateSuggestion inserts a new pending suggestion for (rider, field),
// first deleting whatever suggestion is currently pending for that same
// pair — spec: "at most one pending per (rider, field) — a newer estimate
// replaces the pending one." An accepted or dismissed suggestion for the
// same field is left alone: it is history a later dismissed-gate check
// reads, not something this overwrites.
func (d *DB) CreateSuggestion(ctx context.Context, s ThresholdSuggestion) (ThresholdSuggestion, error) {
	rider := normalizeRider(s.Rider)
	if rider == "" {
		return ThresholdSuggestion{}, errors.New("workout: no rider — whose threshold suggestion is this?")
	}
	if s.Field == "" {
		return ThresholdSuggestion{}, errors.New("workout: a threshold suggestion needs a field")
	}

	id, err := newThresholdSuggestionID()
	if err != nil {
		return ThresholdSuggestion{}, err
	}
	ts := thresholdTimestamp()

	direction := s.Direction
	if direction == "" {
		direction = "up"
	}

	if err := d.DeletePendingSuggestion(ctx, rider, s.Field); err != nil {
		return ThresholdSuggestion{}, err
	}

	_, err = d.db.ExecContext(ctx, d.query(`
        INSERT INTO threshold_suggestions (id, rider, field, value, previous, direction, source_session_id,
                    source_date, reason, status, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, rider, s.Field, s.Value, s.Previous, direction, s.SourceSessionID, s.SourceDate, s.Reason,
		ThresholdPending, ts, ts)
	if err != nil {
		return ThresholdSuggestion{}, err
	}
	return d.GetSuggestion(ctx, id)
}

// DeletePendingSuggestion removes a rider's pending suggestion for field, if
// any — the sync's own stale-suggestion cleanup, called directly when a
// detection pass finds nothing at all to say about a field, and indirectly
// by CreateSuggestion's own "a newer pending replaces the older" rule. A
// no-op (not an error) when nothing is pending for that field.
func (d *DB) DeletePendingSuggestion(ctx context.Context, rider, field string) error {
	_, err := d.db.ExecContext(ctx, d.query(
		`DELETE FROM threshold_suggestions WHERE rider = ? AND field = ? AND status = ?`),
		normalizeRider(rider), field, ThresholdPending)
	return err
}

// GetSuggestion returns one suggestion by id, or ErrThresholdSuggestionNotFound.
func (d *DB) GetSuggestion(ctx context.Context, id string) (ThresholdSuggestion, error) {
	row := d.db.QueryRowContext(ctx, d.query(`
        SELECT id, rider, field, value, previous, direction, source_session_id, source_date, reason,
               status, created_at, updated_at
        FROM threshold_suggestions WHERE id = ?`), id)
	s, err := scanThresholdSuggestion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ThresholdSuggestion{}, ErrThresholdSuggestionNotFound
	}
	return s, err
}

// ListPendingSuggestions returns a rider's pending suggestions, newest
// first — GET /api/training/thresholds' own shape.
func (d *DB) ListPendingSuggestions(ctx context.Context, rider string) ([]ThresholdSuggestion, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT id, rider, field, value, previous, direction, source_session_id, source_date, reason,
               status, created_at, updated_at
        FROM threshold_suggestions WHERE rider = ? AND status = ?
        ORDER BY created_at DESC, id`), normalizeRider(rider), ThresholdPending)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ThresholdSuggestion
	for rows.Next() {
		s, err := scanThresholdSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LatestDismissedSuggestion returns the most recently updated dismissed
// suggestion for (rider, field) in the given direction, if any — what the
// sync's "don't re-suggest unless it moved a further 3%" rule reads before
// creating a new one. Scoped to direction deliberately: a dismissed "FTP
// dropped to 240" suggestion must never gate a fresh "FTP rose to 270"
// finding, or vice versa — those are different claims, not the same
// estimate moving further in one direction.
func (d *DB) LatestDismissedSuggestion(ctx context.Context, rider, field, direction string) (ThresholdSuggestion, bool, error) {
	row := d.db.QueryRowContext(ctx, d.query(`
        SELECT id, rider, field, value, previous, direction, source_session_id, source_date, reason,
               status, created_at, updated_at
        FROM threshold_suggestions WHERE rider = ? AND field = ? AND direction = ? AND status = ?
        ORDER BY updated_at DESC, id DESC LIMIT 1`), normalizeRider(rider), field, direction, ThresholdDismissed)
	s, err := scanThresholdSuggestion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ThresholdSuggestion{}, false, nil
	}
	if err != nil {
		return ThresholdSuggestion{}, false, err
	}
	return s, true, nil
}

// MarkSuggestionAccepted marks a pending suggestion accepted. The caller
// (the API handler) is responsible for actually saving the profile change
// first — this only flips the suggestion's own status, and refuses when the
// suggestion is not currently pending (a double-click, or a race with a
// second resolve of the same id).
func (d *DB) MarkSuggestionAccepted(ctx context.Context, id string) error {
	return d.markSuggestion(ctx, id, ThresholdAccepted)
}

// MarkSuggestionDismissed marks a pending suggestion dismissed.
func (d *DB) MarkSuggestionDismissed(ctx context.Context, id string) error {
	return d.markSuggestion(ctx, id, ThresholdDismissed)
}

func (d *DB) markSuggestion(ctx context.Context, id, status string) error {
	result, err := d.db.ExecContext(ctx, d.query(
		`UPDATE threshold_suggestions SET status = ?, updated_at = ? WHERE id = ? AND status = ?`),
		status, thresholdTimestamp(), id, ThresholdPending)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		// Either the id does not exist, or it exists but is no longer
		// pending — the caller (handleResolveThreshold) has already fetched
		// the row and checked both before calling this, so reaching zero
		// rows here means a race rather than the ordinary case; surfaced as
		// "not found" either way, since there is nothing left to mark.
		return ErrThresholdSuggestionNotFound
	}
	return nil
}

func scanThresholdSuggestion(row rowScanner) (ThresholdSuggestion, error) {
	var s ThresholdSuggestion
	if err := row.Scan(&s.ID, &s.Rider, &s.Field, &s.Value, &s.Previous, &s.Direction, &s.SourceSessionID,
		&s.SourceDate, &s.Reason, &s.Status, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return ThresholdSuggestion{}, err
	}
	return s, nil
}
