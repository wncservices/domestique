// Package settings stores deployment-wide configuration that an admin sets
// from the UI, encrypted the same way a rider's sign-in is.
//
// # Why this exists at all
//
// Most configuration belongs in the config file or the environment, where it
// is version-controlled and arrives from Vault. This is for the one shape that
// does not fit: a credential the *deployment* needs, which an operator would
// otherwise have to put in an env file by hand before the app is usable at
// all. The Garmin OAuth1 consumer is exactly that — without it the sign-in
// form cannot be offered, and requiring a file edit to get a login button is a
// poor first five minutes.
//
// Values are sealed with the same key as everything else, so there is no path
// that writes one in clear, and they never leave the process: the API reports
// *whether* a setting is configured and where it came from, never what it is.
package settings

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

// ErrNotFound means the setting has never been set.
var ErrNotFound = errors.New("no such setting")

// Store holds the settings.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
	box     *secrets.Box
}

// schema returns the DDL as a constant per engine — see providerlink.schema
// for why this is not one Sprintf.
func schema(d dbx.Dialect) string {
	const sqlite = `
CREATE TABLE IF NOT EXISTS settings (
    name       TEXT PRIMARY KEY,
    value      BLOB NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);`
	const postgres = `
CREATE TABLE IF NOT EXISTS settings (
    name       TEXT PRIMARY KEY,
    value      BYTEA NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);`

	if d.Name == dbx.Postgres.Name {
		return postgres
	}
	return sqlite
}

// flagsSchema is a separate, unencrypted table from settings above — a flag
// is a plain on/off switch (auto-sync, so far the only one), not a
// credential, and routing it through Set/Get's sealing would needlessly
// block a deployment with no encryption key from using it at all. Same
// dialect-aware boolean reasoning crew.schema's own auto_share column
// already gives.
func flagsSchema(d dbx.Dialect) string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS flags (
    name       TEXT PRIMARY KEY,
    enabled    %s NOT NULL DEFAULT FALSE,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);`, d.Boolean)
}

// UseDB puts the table in an already-open database.
//
// The box may be nil, in which case nothing can be set — the same rule as a
// rider's sign-in, for the same reason.
func UseDB(db *sql.DB, dsn string, box *secrets.Box) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db, dialect: d, box: box}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("create settings table: %w", err)
	}
	if _, err := db.Exec(flagsSchema(d)); err != nil {
		return nil, fmt.Errorf("create flags table: %w", err)
	}
	return store, nil
}

// CanStore reports whether a setting can be saved at all.
//
// Nil-safe on purpose — a Server with no settings store is a valid
// configuration, and callers rely on being able to ask without a nil check.
func (s *Store) CanStore() bool { return s != nil && s.box != nil }

// Set records a value, replacing any there was.
//
// updatedBy is the admin who set it. Kept because a deployment-wide credential
// that stops working is a thing somebody will want to trace back to a person
// and a time.
func (s *Store) Set(name, value, updatedBy string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a setting needs a name")
	}
	if !s.CanStore() {
		return secrets.ErrNoKey
	}
	if value == "" {
		return errors.New("refusing to store an empty value: delete it instead")
	}

	sealed, err := s.box.Seal(value)
	if err != nil {
		return err
	}

	// #nosec G701 -- constant statement, bound parameters. gosec follows the
	// value through Seal into this call and cannot see that.
	_, err = s.db.Exec(s.dialect.Rebind(`
INSERT INTO settings (name, value, updated_by, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET
    value      = excluded.value,
    updated_by = excluded.updated_by,
    updated_at = excluded.updated_at`),
		name, sealed, strings.ToLower(strings.TrimSpace(updatedBy)),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("save setting %s: %w", name, err)
	}
	return nil
}

// Get returns a value. ErrNotFound when it was never set.
func (s *Store) Get(name string) (string, error) {
	if !s.CanStore() {
		return "", secrets.ErrNoKey
	}

	var sealed []byte
	// #nosec G701 -- constant statement, bound parameter; see Set.
	err := s.db.QueryRow(s.dialect.Rebind(`SELECT value FROM settings WHERE name = ?`),
		strings.TrimSpace(name)).Scan(&sealed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", ErrNotFound
	case err != nil:
		return "", err
	}

	value, err := s.box.Open(sealed)
	if err != nil {
		return "", fmt.Errorf("setting %s: %w — set it again to replace it", name, err)
	}
	return value, nil
}

// Meta is what can safely be shown about a setting: that it is there, who put
// it there and when. Never the value.
type Meta struct {
	UpdatedBy string
	UpdatedAt time.Time
}

// Describe reports whether a setting exists, without decrypting it.
func (s *Store) Describe(name string) (Meta, error) {
	if s == nil {
		return Meta{}, ErrNotFound
	}

	var meta Meta
	var updated string
	err := s.db.QueryRow(s.dialect.Rebind(
		`SELECT updated_by, updated_at FROM settings WHERE name = ?`),
		strings.TrimSpace(name)).Scan(&meta.UpdatedBy, &updated)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Meta{}, ErrNotFound
	case err != nil:
		return Meta{}, err
	}

	meta.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return meta, nil
}

// Delete removes a setting. Deleting one that is not there is not an error.
func (s *Store) Delete(name string) error {
	if s == nil {
		return nil
	}
	_, err := s.db.Exec(s.dialect.Rebind(`DELETE FROM settings WHERE name = ?`),
		strings.TrimSpace(name))
	return err
}

// Flag reads a plain on/off switch — false for one never set, so a fresh
// deployment starts with every flag off rather than needing an explicit row
// to mean "off". Nil-safe like Describe: a Server built without a Settings
// store (most tests) reads every flag as off rather than panicking.
func (s *Store) Flag(name string) (bool, error) {
	if s == nil {
		return false, nil
	}
	var enabled bool
	err := s.db.QueryRow(s.dialect.Rebind(`SELECT enabled FROM flags WHERE name = ?`),
		strings.TrimSpace(name)).Scan(&enabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return enabled, nil
}

// DescribeFlag is Describe for a flag — who last changed it and when, from
// the separate flags table rather than the encrypted settings one.
func (s *Store) DescribeFlag(name string) (Meta, error) {
	if s == nil {
		return Meta{}, ErrNotFound
	}

	var meta Meta
	var updated string
	err := s.db.QueryRow(s.dialect.Rebind(
		`SELECT updated_by, updated_at FROM flags WHERE name = ?`),
		strings.TrimSpace(name)).Scan(&meta.UpdatedBy, &updated)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Meta{}, ErrNotFound
	case err != nil:
		return Meta{}, err
	}

	meta.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return meta, nil
}

// SetFlag records a plain on/off switch — no encryption key needed, unlike
// Set/Get: a flag is not a credential, and there is no reason a deployment
// with nothing to encrypt yet should be unable to use one.
func (s *Store) SetFlag(name string, enabled bool, updatedBy string) error {
	return s.SetFlagAt(name, enabled, updatedBy, time.Now())
}

// SetFlagAt is SetFlag with the timestamp supplied, for a flag row used as a
// "last happened at" marker (DescribeFlag's UpdatedAt) where the caller's
// clock — a fixed one in tests — is the one that counts.
func (s *Store) SetFlagAt(name string, enabled bool, updatedBy string, at time.Time) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a flag needs a name")
	}
	_, err := s.db.Exec(s.dialect.Rebind(`
INSERT INTO flags (name, enabled, updated_by, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET
    enabled    = excluded.enabled,
    updated_by = excluded.updated_by,
    updated_at = excluded.updated_at`),
		name, enabled, strings.ToLower(strings.TrimSpace(updatedBy)),
		at.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("save flag %s: %w", name, err)
	}
	return nil
}
