// Package pacingpush remembers which course a rider's pacing plan became on
// their own Garmin or Wahoo account, so a second push replaces it instead of
// piling up copies.
//
// It is deliberately separate from sync state. A pacing course is not a route
// in the library: it is derived on request from a route and the rider's FTP,
// pushed once when asked, and never reconciled by the diff engine. Keeping its
// remote ids here means the library route's own course, and its sync state,
// are never touched by it.
//
// The rows are keyed on (rider, provider, key) where key names what was paced:
// "pacing:<goal id>" for a goal's plan, "pacing:<route slug>" for a route's on
// its own. A rider's rows are removed with the rider.
package pacingpush

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// Store holds the remote ids of pushed pacing courses.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

const schema = `
CREATE TABLE IF NOT EXISTS pacing_pushes (
    rider     TEXT NOT NULL,
    provider  TEXT NOT NULL,
    key       TEXT NOT NULL,
    remote_id TEXT NOT NULL,
    pushed_at TEXT NOT NULL,
    PRIMARY KEY (rider, provider, key)
);`

// UseDB puts the table in an already-open database. Safe to run twice.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate pacing_pushes table: %w", err)
	}
	return &Store{db: db, dialect: d}, nil
}

func normalize(rider string) string { return strings.ToLower(strings.TrimSpace(rider)) }

// Get returns the remote id recorded for the course, ok false when none was.
func (s *Store) Get(ctx context.Context, rider, provider, key string) (string, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT remote_id FROM pacing_pushes WHERE rider = ? AND provider = ? AND key = ?`),
		normalize(rider), provider, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// Put records the remote id, replacing any earlier one.
func (s *Store) Put(ctx context.Context, rider, provider, key, remoteID string) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO pacing_pushes (rider, provider, key, remote_id, pushed_at) VALUES (?, ?, ?, ?, ?)
        ON CONFLICT (rider, provider, key) DO UPDATE SET
            remote_id = excluded.remote_id, pushed_at = excluded.pushed_at`),
		normalize(rider), provider, key, remoteID, time.Now().UTC().Format(time.RFC3339))
	return err
}

// RemoteIDs is every remote id recorded for the rider on a provider: the
// courses this app made from pacing plans. Sync-back uses it to leave them out
// of what it offers to import.
func (s *Store) RemoteIDs(ctx context.Context, rider, provider string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(
		`SELECT remote_id FROM pacing_pushes WHERE rider = ? AND provider = ?`), normalize(rider), provider)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Forget drops the record. Forgetting what is not there is not an error.
func (s *Store) Forget(ctx context.Context, rider, provider, key string) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM pacing_pushes WHERE rider = ? AND provider = ? AND key = ?`),
		normalize(rider), provider, key)
	return err
}

// DeleteRider removes every row for a rider and reports how many.
func (s *Store) DeleteRider(ctx context.Context, rider string) (int, error) {
	res, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM pacing_pushes WHERE rider = ?`), normalize(rider))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
