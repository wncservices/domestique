package workout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/why"
)

// The two kinds of thing an adjustment can explain. A level carries its own
// reason without a second table.
const (
	SubjectWorkout = "workout"
	SubjectLevel   = "level"
)

// levelAdjustmentsKept is how many rows one level subject keeps. A level can
// move many times over a season and only the recent moves are worth a rider's
// attention; a workout's rows go with the workout instead.
const levelAdjustmentsKept = 5

// adjustmentBatch bounds the ids in one IN (...) list, well under the
// smallest bound-parameter limit either engine has ever had.
const adjustmentBatch = 400

// Adjustment is one stored reason. Inputs holds whatever internal/why's typed
// struct for Rule flattened to, so a number read back is a float64.
type Adjustment struct {
	ID          string
	Rider       string
	SubjectKind string
	SubjectID   string
	Rule        why.Rule
	Inputs      map[string]any
	Text        string
	Day         string
	CreatedAt   string
}

// adjustmentTime is fixed-width, unlike RFC3339Nano (which drops trailing
// zeros), so "latest by created_at" is a plain string comparison.
func adjustmentTime() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func newAdjustmentID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// RecordAdjustment stores why a subject was changed. It upserts on
// (rider, kind, subject, day, rule), so a pass that runs twice, or a retry,
// leaves one row; the second write's inputs and text win. A level subject is
// then pruned to its latest few rows.
func (d *DB) RecordAdjustment(ctx context.Context, rider, kind, subjectID string, r why.Record, day string) error {
	rider = normalizeRider(rider)
	if rider == "" || subjectID == "" || (kind != SubjectWorkout && kind != SubjectLevel) {
		return errors.New("workout: an adjustment needs a rider, a subject and a known kind")
	}
	inputs, err := json.Marshal(r.Inputs)
	if err != nil {
		return err
	}
	id, err := newAdjustmentID()
	if err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, d.query(`
        INSERT INTO adjustments (id, rider, subject_kind, subject_id, rule, inputs, text, day, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (rider, subject_kind, subject_id, day, rule) DO UPDATE SET
            inputs = excluded.inputs, text = excluded.text, created_at = excluded.created_at`),
		id, rider, kind, subjectID, string(r.Rule), string(inputs), r.Text, day, adjustmentTime()); err != nil {
		return err
	}
	if kind == SubjectLevel {
		return d.pruneLevelAdjustments(ctx, rider, subjectID)
	}
	return nil
}

// pruneLevelAdjustments keeps the newest levelAdjustmentsKept rows of one
// rider's level subject. Selected and deleted by id in Go rather than with a
// LIMIT/OFFSET subquery, which the two engines spell differently on DELETE.
func (d *DB) pruneLevelAdjustments(ctx context.Context, rider, subjectID string) error {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT id FROM adjustments WHERE rider = ? AND subject_kind = ? AND subject_id = ?
        ORDER BY created_at DESC, id DESC`), rider, SubjectLevel, subjectID)
	if err != nil {
		return err
	}
	var stale []string
	for n := 0; rows.Next(); n++ {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if n >= levelAdjustmentsKept {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, id := range stale {
		if _, err := d.db.ExecContext(ctx, d.query(`DELETE FROM adjustments WHERE id = ?`), id); err != nil {
			return err
		}
	}
	return nil
}

// LatestAdjustments returns, for each of subjectIDs the rider has a reason
// for, the most recent one, keyed by subject id. One query per adjustmentBatch
// ids, never one per subject. Filtered by rider and kind, so it can never
// return another rider's row.
func (d *DB) LatestAdjustments(ctx context.Context, rider, kind string, subjectIDs []string) (map[string]Adjustment, error) {
	out := map[string]Adjustment{}
	rider = normalizeRider(rider)
	for start := 0; start < len(subjectIDs); start += adjustmentBatch {
		end := min(start+adjustmentBatch, len(subjectIDs))
		batch := subjectIDs[start:end]
		args := []any{rider, kind}
		for _, id := range batch {
			args = append(args, id)
		}
		// #nosec G202 -- only "?" placeholders are joined into the statement;
		// every value is bound.
		q := `SELECT id, rider, subject_kind, subject_id, rule, inputs, text, day, created_at
            FROM adjustments WHERE rider = ? AND subject_kind = ? AND subject_id IN (` +
			strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + `)
            ORDER BY created_at ASC, id ASC`
		rows, err := d.db.QueryContext(ctx, d.query(q), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var a Adjustment
			var rule, inputs string
			if err := rows.Scan(&a.ID, &a.Rider, &a.SubjectKind, &a.SubjectID, &rule, &inputs, &a.Text, &a.Day, &a.CreatedAt); err != nil {
				_ = rows.Close()
				return nil, err
			}
			a.Rule = why.Rule(rule)
			if inputs != "" {
				// An unreadable blob leaves Inputs nil: the sentence still shows.
				_ = json.Unmarshal([]byte(inputs), &a.Inputs)
			}
			out[a.SubjectID] = a // ascending, so the last one written wins
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// DeleteRiderAdjustments removes every reason a rider has, workouts and
// levels alike, and reports how many. For rider deletion.
func (d *DB) DeleteRiderAdjustments(ctx context.Context, rider string) (int, error) {
	res, err := d.db.ExecContext(ctx, d.query(`DELETE FROM adjustments WHERE rider = ?`), normalizeRider(rider))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
