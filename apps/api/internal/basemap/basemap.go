// Package basemap tracks and drives updates to the tiles component's
// basemap.pmtiles — a Kubernetes Job an admin triggers from the UI,
// replacing the pmtiles extract + kubectl cp runbook documented in
// domestique-infra/tiles/templates/deployment.yaml. See job.go for the Job
// itself; this file is the bbox/validation types and the history Store.
package basemap

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// BBox is a bounding box in the same west,south,east,north order
// `pmtiles extract --bbox` takes, so a value round-trips into the Job's own
// command line without reordering.
type BBox struct {
	West, South, East, North float64
}

// maxAreaSqDeg caps how much a single request may extract. 500 comfortably
// covers Western Europe (the crew's own first extract was ~260 sq deg) with
// room to grow, while still rejecting a continent- or planet-scale request
// an admin fat-fingered rather than chose — those are tens of thousands of
// square degrees and, at any usable zoom, tens to hundreds of GB. Nothing
// here can enforce "the region actually has cycling routes in it"; this is
// only a sanity backstop, not a claim the box is well-chosen.
const maxAreaSqDeg = 500

// maxZoomLevel caps how detailed a single request may go. 15 is
// building-level detail — well past what a route-overview basemap needs
// (the crew's own first extract used 14) — and area × 4^zoom is how
// archive size actually scales, so this bound matters as much as the area
// one above.
const maxZoomLevel = 15

// Validate reports whether b is a sane, boundable region — not whether it
// is well-chosen for wherever this crew actually rides.
func (b BBox) Validate() error {
	switch {
	case b.West < -180 || b.West > 180:
		return fmt.Errorf("west %.4f is out of range (-180..180)", b.West)
	case b.East < -180 || b.East > 180:
		return fmt.Errorf("east %.4f is out of range (-180..180)", b.East)
	case b.South < -90 || b.South > 90:
		return fmt.Errorf("south %.4f is out of range (-90..90)", b.South)
	case b.North < -90 || b.North > 90:
		return fmt.Errorf("north %.4f is out of range (-90..90)", b.North)
	case b.West >= b.East:
		return fmt.Errorf("west (%.4f) must be less than east (%.4f)", b.West, b.East)
	case b.South >= b.North:
		return fmt.Errorf("south (%.4f) must be less than north (%.4f)", b.South, b.North)
	}
	area := (b.East - b.West) * (b.North - b.South)
	if area > maxAreaSqDeg {
		return fmt.Errorf("area %.0f square degrees exceeds the %d limit — pick a smaller region",
			area, maxAreaSqDeg)
	}
	return nil
}

// ValidateMaxZoom is separate from BBox.Validate: maxzoom is a sibling
// request field, not part of the box itself.
func ValidateMaxZoom(z int) error {
	if z < 0 || z > maxZoomLevel {
		return fmt.Errorf("maxzoom %d is out of range (0..%d)", z, maxZoomLevel)
	}
	return nil
}

// Status is where a triggered update is in its lifecycle.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Record is one triggered update, from request to outcome.
type Record struct {
	ID          string
	BBox        BBox
	MaxZoom     int
	BuildDate   string // YYYYMMDD, the protomaps build this ran against
	Status      Status
	Error       string // set only when Status is StatusFailed
	JobName     string
	SizeBytes   int64 // set only when Status is StatusSucceeded
	RequestedBy string
	CreatedAt   time.Time
	CompletedAt *time.Time // nil while Status is pending or running
}

// Store persists update history — separate from settings.Store: nothing
// here is a credential, so it does not need that package's sealing, and
// routing plain bbox/status rows through Seal/Open would only add a key
// dependency this data has no reason to need.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

func schema(dialect dbx.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS basemap_updates (
    id            TEXT PRIMARY KEY,
    west          DOUBLE PRECISION NOT NULL,
    south         DOUBLE PRECISION NOT NULL,
    east          DOUBLE PRECISION NOT NULL,
    north         DOUBLE PRECISION NOT NULL,
    max_zoom      INTEGER NOT NULL,
    build_date    TEXT NOT NULL,
    status        TEXT NOT NULL,
    error         TEXT NOT NULL DEFAULT '',
    job_name      TEXT NOT NULL DEFAULT '',
    size_bytes    BIGINT NOT NULL DEFAULT 0,
    requested_by  TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    completed_at  TEXT
);
-- At most one row may be pending or running at a time — an expression
-- index on the constant 1, filtered to those two statuses, so every
-- matching row collides on the same indexed value regardless of which of
-- the two statuses it actually holds. The API's own check-then-create in
-- handleBasemapUpdate handles the ordinary case (an admin double-clicking,
-- or forgetting a previous trigger is still running) with a clean 409, but
-- only this constraint is atomic across concurrent requests — two Jobs
-- racing to place a file on the same tiles pod is not something a
-- check-then-create alone can rule out. Verified on both engines: SQLite
-- (partial + expression indexes since 3.8.0) and PostgreSQL both reject a
-- second INSERT while a matching row exists, and both accept one once
-- every existing row has moved to succeeded/failed.
CREATE UNIQUE INDEX IF NOT EXISTS idx_basemap_updates_one_in_progress
    ON basemap_updates ((1)) WHERE status IN ('pending', 'running');`
}

// newID is 16 random bytes, hex-encoded — unique, not a secret: this record
// is only ever looked up by an already-authenticated admin (PermManageSettings
// gates every basemap endpoint), so unlike sessions.newToken this has no
// unguessability requirement to satisfy, only a collision one.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate basemap update id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// UseDB puts the table in an already-open database.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, dialect: d}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("create basemap_updates table: %w", err)
	}
	return store, nil
}

// timestampFormat is RFC3339 with nanoseconds explicitly zero-padded to a
// fixed width, unlike time.RFC3339Nano (which trims trailing zeros and so
// produces variable-length, non-lexicographically-sortable strings —
// ".5" sorts before ".123" even though 500ms is later than 123ms). Latest
// below relies on plain string ordering matching chronological order, and
// plain time.RFC3339's one-second resolution is not fine enough to tell
// two updates triggered close together apart at all.
const timestampFormat = "2006-01-02T15:04:05.000000000Z07:00"

// ErrAlreadyInProgress means an update is already pending or running —
// idx_basemap_updates_one_in_progress (see schema above) rejected the
// INSERT. The API's own check-then-create in handleBasemapUpdate catches
// the ordinary case before ever calling Create, so this is the backstop
// for two requests that raced past that check at nearly the same instant;
// it is enforced by the database itself, unlike the pre-check, so it holds
// even then.
var ErrAlreadyInProgress = errors.New("a basemap update is already in progress")

// Create records a new update as pending and returns its id.
func (s *Store) Create(bbox BBox, maxZoom int, buildDate, requestedBy string) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(timestampFormat)
	if _, err := s.db.Exec(s.dialect.Rebind(`
INSERT INTO basemap_updates (id, west, south, east, north, max_zoom, build_date, status, requested_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, bbox.West, bbox.South, bbox.East, bbox.North, maxZoom, buildDate, StatusPending, requestedBy, now); err != nil {
		if isUniqueViolation(err) {
			return "", ErrAlreadyInProgress
		}
		return "", fmt.Errorf("record basemap update: %w", err)
	}
	return id, nil
}

// isUniqueViolation reports whether err is a unique-constraint violation —
// dialect-agnostic by checking both engines' own error text, since pgx and
// modernc.org/sqlite each report it in a different shape and this table's
// id (randomly generated, 16 bytes) is never expected to collide, so the
// only constraint an INSERT here could ever actually hit is the partial
// index above.
func isUniqueViolation(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

// SetJobName records which Job a pending record actually became, and moves
// it to running. Separate from Create because the Job's own name (which
// includes a generated suffix) is only known after the Kubernetes API
// accepts the create call.
func (s *Store) SetJobName(id, jobName string) error {
	_, err := s.db.Exec(s.dialect.Rebind(
		`UPDATE basemap_updates SET status = ?, job_name = ? WHERE id = ?`),
		StatusRunning, jobName, id)
	return err
}

// MarkSucceeded completes a record with the resulting archive size.
func (s *Store) MarkSucceeded(id string, sizeBytes int64) error {
	now := time.Now().UTC().Format(timestampFormat)
	_, err := s.db.Exec(s.dialect.Rebind(
		`UPDATE basemap_updates SET status = ?, size_bytes = ?, completed_at = ? WHERE id = ?`),
		StatusSucceeded, sizeBytes, now, id)
	return err
}

// MarkFailed completes a record with why.
func (s *Store) MarkFailed(id, errMsg string) error {
	now := time.Now().UTC().Format(timestampFormat)
	_, err := s.db.Exec(s.dialect.Rebind(
		`UPDATE basemap_updates SET status = ?, error = ?, completed_at = ? WHERE id = ?`),
		StatusFailed, errMsg, now, id)
	return err
}

// ErrNoRecord means no update has ever been triggered.
var ErrNoRecord = errors.New("no basemap update has been triggered")

// Latest returns the most recently created record, regardless of status —
// the UI's own job is to show a pending/running one as in-progress and a
// succeeded/failed one as the last completed update.
func (s *Store) Latest() (Record, error) {
	row := s.db.QueryRow(s.dialect.Rebind(`
SELECT id, west, south, east, north, max_zoom, build_date, status, error, job_name, size_bytes, requested_by, created_at, completed_at
FROM basemap_updates ORDER BY created_at DESC LIMIT 1`))
	return scanRecord(row)
}

// LatestSucceeded returns the most recent update that actually replaced
// the live archive — unlike Latest, a pending/running/failed row never
// changed the file's bytes, so only this one identifies what is currently
// being served. preview.go's cache is keyed against this ID specifically:
// it must go stale exactly when the file an admin is looking at changes,
// not whenever any update is merely attempted.
func (s *Store) LatestSucceeded() (Record, error) {
	row := s.db.QueryRow(s.dialect.Rebind(`
SELECT id, west, south, east, north, max_zoom, build_date, status, error, job_name, size_bytes, requested_by, created_at, completed_at
FROM basemap_updates WHERE status = ? ORDER BY created_at DESC LIMIT 1`), StatusSucceeded)
	return scanRecord(row)
}

func scanRecord(row *sql.Row) (Record, error) {
	var rec Record
	var completedAt sql.NullString
	var createdAt string
	err := row.Scan(&rec.ID, &rec.BBox.West, &rec.BBox.South, &rec.BBox.East, &rec.BBox.North,
		&rec.MaxZoom, &rec.BuildDate, &rec.Status, &rec.Error, &rec.JobName, &rec.SizeBytes,
		&rec.RequestedBy, &createdAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNoRecord
	}
	if err != nil {
		return Record{}, fmt.Errorf("read basemap update: %w", err)
	}
	if t, err := time.Parse(timestampFormat, createdAt); err == nil {
		rec.CreatedAt = t
	}
	if completedAt.Valid {
		if t, err := time.Parse(timestampFormat, completedAt.String); err == nil {
			rec.CompletedAt = &t
		}
	}
	return rec, nil
}
