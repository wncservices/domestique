package workout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// ftpChangeSeq breaks ties between changes recorded in the same instant.
var ftpChangeSeq atomic.Uint64

// FTP change sources, as stored: who moved the rider's FTP.
const (
	// FTPSourceManual is the rider typing a number in the profile.
	FTPSourceManual = "manual"
	// FTPSourceDetection is a threshold finding, or a Garmin or history
	// estimate, applied or accepted: the rides said so.
	FTPSourceDetection = "detection"
	// FTPSourceTest is a result read from an FTP test ride.
	FTPSourceTest = "test"
)

func validFTPSource(s string) bool {
	return s == FTPSourceManual || s == FTPSourceDetection || s == FTPSourceTest
}

// ftpChangeSchema is the FTP history. Before this table nothing recorded what
// FTP used to be: an auto-applied finding or a typed edit overwrote the
// profile and left nothing behind, so a season review could not say what FTP
// was at the start of a year. It is append-only. The review never invents a
// start: before the first row it says history starts there.
//
// id is a random hex string (or "suggestion-<id>" for a copy of an old
// accepted suggestion, so copying twice is a conflict and not a duplicate).
const ftpChangeSchema = `
CREATE TABLE IF NOT EXISTS ftp_changes (
    id         TEXT PRIMARY KEY,
    rider      TEXT NOT NULL,
    changed_at TEXT NOT NULL,
    date       TEXT NOT NULL,
    from_watts DOUBLE PRECISION NOT NULL DEFAULT 0,
    to_watts   DOUBLE PRECISION NOT NULL,
    source     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ftp_changes_rider_date_idx ON ftp_changes (rider, date)`

// FTPChange is one move of a rider's FTP.
type FTPChange struct {
	ID    string
	Rider string
	// ChangedAt is when it was recorded (RFC 3339, to the nanosecond so two
	// changes in one second keep their order); Date is the local-date
	// string the season review files it under.
	ChangedAt string
	Date      string
	From, To  float64
	Source    string
}

func (d *DB) appendFTPChange(ctx context.Context, rider string, from, to float64, source, date string) error {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("generate ftp change id: %w", err)
	}
	now := d.now().UTC()
	if date == "" {
		date = now.Format("2006-01-02")
	}
	// The id sorts in the order changes were made even when two share an
	// instant (a clock that is fixed, or two replicas in one nanosecond): a
	// counter before the random part. Changes are read in (date, changed_at, id)
	// order, and a from-to chain read out of order would be nonsense.
	id := fmt.Sprintf("%020d-%06d-%s", now.UnixNano(), ftpChangeSeq.Add(1)%1_000_000, hex.EncodeToString(b))
	_, err := d.db.ExecContext(ctx, d.query(`
        INSERT INTO ftp_changes (id, rider, changed_at, date, from_watts, to_watts, source)
        VALUES (?, ?, ?, ?, ?, ?, ?)`),
		id, rider, now.Format(time.RFC3339Nano), date, from, to, source)
	return err
}

// ListFTPChanges returns a rider's FTP changes between two dates, both
// inclusive (an empty bound is open), oldest first.
func (d *DB) ListFTPChanges(ctx context.Context, rider, from, to string) ([]FTPChange, error) {
	args := []any{normalizeRider(rider)}
	clause, args := scopeClause(from, to, args)
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT id, rider, changed_at, date, from_watts, to_watts, source
        FROM ftp_changes WHERE rider = ?`+clause+` ORDER BY date, changed_at, id`), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FTPChange
	for rows.Next() {
		var c FTPChange
		if err := rows.Scan(&c.ID, &c.Rider, &c.ChangedAt, &c.Date, &c.From, &c.To, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReasonNamesTest reports whether a threshold suggestion's reason says it came
// from an FTP test ride ("from Tuesday's ramp test (400 W best minute)"). It
// is the one place that reads the wording, shared by the one-time copy of old
// accepted suggestions and by accepting a new one.
func ReasonNamesTest(reason string) bool {
	return strings.Contains(reason, " test (")
}

// copyAcceptedFTPSuggestions brings the FTP changes the rider accepted before
// this table existed into it, once. A suggestion's accepted time is its
// updated_at, and its date is that day; a suggestion whose day and value are
// already recorded (accepting one writes the change itself, so a start after
// upgrading must not double it) is skipped, and the copy's id is derived from
// the suggestion's own, so a second start finds the conflict and writes
// nothing. Source is test when the reason names a test ride, else detection.
func (d *DB) copyAcceptedFTPSuggestions() error {
	rows, err := d.db.Query(d.query(`
        SELECT id, rider, value, previous, reason, updated_at FROM threshold_suggestions
        WHERE field = 'ftp' AND status = ? ORDER BY updated_at, id`), ThresholdAccepted)
	if err != nil {
		return err
	}
	type accepted struct {
		id, rider, reason, updated string
		to, from                   float64
	}
	var all []accepted
	for rows.Next() {
		var a accepted
		if err := rows.Scan(&a.id, &a.rider, &a.to, &a.from, &a.reason, &a.updated); err != nil {
			_ = rows.Close()
			return err
		}
		all = append(all, a)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	for _, a := range all {
		date := a.updated
		if len(date) >= 10 {
			date = date[:10]
		}
		source := FTPSourceDetection
		if ReasonNamesTest(a.reason) {
			source = FTPSourceTest
		}
		var recorded int
		if err := d.db.QueryRow(d.query(
			`SELECT COUNT(*) FROM ftp_changes WHERE rider = ? AND date = ? AND to_watts = ?`), a.rider, date, a.to).Scan(&recorded); err != nil {
			return err
		}
		if recorded > 0 {
			continue
		}
		if _, err := d.db.Exec(d.query(`
            INSERT INTO ftp_changes (id, rider, changed_at, date, from_watts, to_watts, source)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT (id) DO NOTHING`),
			"suggestion-"+a.id, a.rider, a.updated, date, a.from, a.to, source); err != nil {
			return err
		}
	}
	return nil
}
