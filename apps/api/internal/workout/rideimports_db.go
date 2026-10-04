package workout

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The states and phases a ride-history import moves through. Interrupted is
// never stored: it is what a running job nobody has touched for
// ImportStaleAfter reads as, because a restart killed it.
const (
	ImportRunning     = "running"
	ImportDone        = "done"
	ImportFailed      = "failed"
	ImportInterrupted = "interrupted"

	ImportReading     = "reading"
	ImportAnalysing   = "analysing"
	ImportRecomputing = "recomputing"
)

// ImportStaleAfter is how long a running job may go without an update before
// it is reported interrupted.
const ImportStaleAfter = 2 * time.Minute

// ErrImportRunning is returned when a rider already has an import running.
var ErrImportRunning = errors.New("workout: this rider already has an import running")

// RideImportCounts is what a job has found so far. Nothing else about the
// upload is kept.
type RideImportCounts struct {
	Added        int
	Duplicate    int
	SkippedSport int
	Unsupported  int
	Unreadable   int
}

// RideImport is one import job as the status endpoint reports it.
type RideImport struct {
	ID    string
	Rider string
	State string
	Phase string
	RideImportCounts
	// Error is a short class ("too large"), never a message from inside the upload.
	Error     string
	StartedAt string
	UpdatedAt string
}

func importTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// StartRideImport records a new running job for rider, or returns
// ErrImportRunning when they already have one. A running job that has not been
// touched for ImportStaleAfter was killed by a restart: it is closed as failed
// ("interrupted") first, or the rider could never import again. The partial
// unique index settles a race between two replicas.
func (d *DB) StartRideImport(ctx context.Context, rider, id string, now time.Time) error {
	rider = normalizeRider(rider)
	if rider == "" || id == "" {
		return errors.New("workout: an import needs a rider and an id")
	}
	cutoff := importTime(now.Add(-ImportStaleAfter))
	if _, err := d.db.ExecContext(ctx, d.query(`
        UPDATE ride_imports SET state = ?, error = ?, updated_at = ?
        WHERE rider = ? AND state = ? AND updated_at < ?`),
		ImportFailed, "interrupted", importTime(now), rider, ImportRunning, cutoff); err != nil {
		return err
	}
	running, err := d.hasRunningImport(ctx, rider)
	if err != nil {
		return err
	}
	if running {
		return ErrImportRunning
	}
	ts := importTime(now)
	if _, err := d.db.ExecContext(ctx, d.query(`
        INSERT INTO ride_imports (id, rider, state, phase, started_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?)`), id, rider, ImportRunning, ImportReading, ts, ts); err != nil {
		// Lost the race to another replica between the check and the insert.
		if running, checkErr := d.hasRunningImport(ctx, rider); checkErr == nil && running {
			return ErrImportRunning
		}
		return err
	}
	return nil
}

func (d *DB) hasRunningImport(ctx context.Context, rider string) (bool, error) {
	var one int
	err := d.db.QueryRowContext(ctx, d.query(`SELECT 1 FROM ride_imports WHERE rider = ? AND state = ?`), rider, ImportRunning).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// UpdateRideImport saves a running job's phase and counts, and is also its
// heartbeat: updated_at is what tells another replica it is still alive.
func (d *DB) UpdateRideImport(ctx context.Context, id, phase string, c RideImportCounts, now time.Time) error {
	_, err := d.db.ExecContext(ctx, d.query(`
        UPDATE ride_imports SET phase = ?, added = ?, duplicate = ?, skipped_sport = ?, unsupported = ?, unreadable = ?, updated_at = ?
        WHERE id = ? AND state = ?`),
		phase, c.Added, c.Duplicate, c.SkippedSport, c.Unsupported, c.Unreadable, importTime(now), id, ImportRunning)
	return err
}

// FinishRideImport closes a job as done or failed with its final counts.
// errClass is a short class for a failure and "" otherwise.
func (d *DB) FinishRideImport(ctx context.Context, id, state, errClass string, c RideImportCounts, now time.Time) error {
	_, err := d.db.ExecContext(ctx, d.query(`
        UPDATE ride_imports SET state = ?, error = ?, added = ?, duplicate = ?, skipped_sport = ?, unsupported = ?, unreadable = ?, updated_at = ?
        WHERE id = ?`),
		state, errClass, c.Added, c.Duplicate, c.SkippedSport, c.Unsupported, c.Unreadable, importTime(now), id)
	return err
}

// LatestRideImport returns rider's most recent import. ok is false when they
// have never imported. A running job not updated for ImportStaleAfter before
// now is reported as interrupted.
func (d *DB) LatestRideImport(ctx context.Context, rider string, now time.Time) (ri RideImport, ok bool, err error) {
	err = d.db.QueryRowContext(ctx, d.query(`
        SELECT id, rider, state, phase, added, duplicate, skipped_sport, unsupported, unreadable, error, started_at, updated_at
        FROM ride_imports WHERE rider = ? ORDER BY started_at DESC, id DESC LIMIT 1`), normalizeRider(rider)).Scan(
		&ri.ID, &ri.Rider, &ri.State, &ri.Phase, &ri.Added, &ri.Duplicate, &ri.SkippedSport, &ri.Unsupported, &ri.Unreadable,
		&ri.Error, &ri.StartedAt, &ri.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RideImport{}, false, nil
	}
	if err != nil {
		return RideImport{}, false, err
	}
	if ri.State == ImportRunning && ri.UpdatedAt < importTime(now.Add(-ImportStaleAfter)) {
		ri.State = ImportInterrupted
	}
	return ri, true, nil
}

// EarliestSessionDate is the date of rider's oldest completed session, "" when
// they have none: how far back their fitness history now goes.
func (d *DB) EarliestSessionDate(ctx context.Context, rider string) (string, error) {
	var date sql.NullString
	if err := d.db.QueryRowContext(ctx, d.query(`SELECT MIN(date) FROM completed_sessions WHERE rider = ?`), normalizeRider(rider)).Scan(&date); err != nil {
		return "", err
	}
	return date.String, nil
}
