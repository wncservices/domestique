// Package accounts stores the head units routes get pushed to.
//
// An account is not a user. Users come from Authelia and are never stored:
// Remote-User says who you are, Remote-Groups says what you may do, and that
// is the whole story. An account is a *connection to a provider* — a Garmin
// Connect or Wahoo account, with the label shown in the UI and, once the
// adapters exist, the credential to reach it.
//
// Accounts belong to the rider who linked them. The rider is the Authelia
// username, taken from the session at link time rather than configured, so
// there is no second place where somebody's name is written down and no way
// for the two to disagree.
package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// ErrNotFound is returned for an account id nothing matches.
var ErrNotFound = errors.New("no such account")

// ErrExists is returned when a rider already linked that provider.
var ErrExists = errors.New("that provider is already linked")

// Store holds the linked accounts.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

// schema takes the dialect because auto_push needs a real per-engine boolean
// type — see crew.schema's identical comment for why. DEFAULT TRUE, not
// FALSE: this column is an opt-*out*, not an opt-in — auto-sync already
// pushed to every linked account before this existed, so a pre-existing
// account (and any newly linked one) keeps that behavior until its own rider
// turns it off, rather than every account silently going quiet the moment
// this migrates in.
func schema(d dbx.Dialect) string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS accounts (
    id         TEXT PRIMARY KEY,
    provider   TEXT NOT NULL,
    rider      TEXT NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    auto_push  %s NOT NULL DEFAULT TRUE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);`, d.Boolean)
}

// UseDB puts the accounts table in an already-open database — the same one
// holding the routes and the sync state, so a deployment needs exactly one.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db, dialect: d}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("migrate accounts table: %w", err)
	}
	if err := store.addAutoPushColumn(); err != nil {
		return nil, fmt.Errorf("migrate accounts table: %w", err)
	}
	return store, nil
}

// addAutoPushColumn adds auto_push to an accounts table that predates the
// column — CREATE TABLE IF NOT EXISTS above is a no-op against a table that
// already exists, so a genuinely new column needs its own step. This runs
// every startup; once the column is there, the ALTER fails with a
// database-specific "it's already there" error that is the expected steady
// state, not a real failure — anything else still surfaces. Same pattern as
// crew.Store.addAutoShareColumn.
func (s *Store) addAutoPushColumn() error {
	_, err := s.db.Exec(fmt.Sprintf(
		`ALTER TABLE accounts ADD COLUMN auto_push %s NOT NULL DEFAULT TRUE`, s.dialect.Boolean))
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
}

// ID is how an account is named everywhere else: "garmin:wilant".
//
// One account per rider per provider, which is what makes this a safe primary
// key: nobody has two Garmin accounts on one head unit.
func ID(provider model.Provider, rider string) string {
	return fmt.Sprintf("%s:%s", provider, strings.ToLower(strings.TrimSpace(rider)))
}

// RiderPattern is what a rider string is allowed to be — it lands in an
// account id used across the API and in a URL, so it has to survive both.
// Exported so anything else that has to validate a rider string (the
// rename-rider CLI command, notably) shares this one definition rather than
// duplicating the regex and risking the two drifting apart.
//
// Admits `|`: an OIDC subject from an issuer like Auth0 is commonly shaped
// "auth0|64f2a1b2c3d4e5f6" — that pipe would otherwise be rejected the first
// time such a rider tried to link an account, one step after signing in
// worked fine.
var RiderPattern = regexp.MustCompile(`^[a-zA-Z0-9._@|-]+$`)

// Link records a rider's connection to a provider.
func (s *Store) Link(ctx context.Context, provider model.Provider, rider, label string) (model.Account, error) {
	rider = strings.TrimSpace(rider)
	if rider == "" {
		return model.Account{}, errors.New("accounts: no rider — who is linking this?")
	}
	// The rider comes from Authelia or an OIDC issuer, but it lands in an id
	// used across the API, so keep it to something that survives a URL.
	if !RiderPattern.MatchString(rider) {
		return model.Account{}, fmt.Errorf("accounts: rider %q has characters that cannot appear in an id", rider)
	}
	switch provider {
	case model.ProviderGarmin, model.ProviderWahoo:
	default:
		return model.Account{}, fmt.Errorf("accounts: unknown provider %q", provider)
	}

	id := ID(provider, rider)
	if _, err := s.Get(ctx, id); err == nil {
		return model.Account{}, fmt.Errorf("%w: %s", ErrExists, id)
	}

	if strings.TrimSpace(label) == "" {
		label = fmt.Sprintf("%s's %s", rider, providerLabel(provider))
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO accounts (id, provider, rider, label, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?)`),
		id, string(provider), rider, label, now, now)
	if err != nil {
		return model.Account{}, err
	}

	return s.Get(ctx, id)
}

// Relabel changes the name shown in the UI. Nothing else about an account is
// editable: the provider and rider are what make it that account.
func (s *Store) Relabel(ctx context.Context, id, label string) (model.Account, error) {
	if strings.TrimSpace(label) == "" {
		return model.Account{}, errors.New("accounts: label cannot be empty")
	}

	result, err := s.db.ExecContext(ctx,
		s.dialect.Rebind(`UPDATE accounts SET label = ?, updated_at = ? WHERE id = ?`),
		label, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return model.Account{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.Account{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Unlink removes an account.
//
// The sync state for it is left alone deliberately. Re-linking the same
// provider gives the same id, and the recorded remote ids are still true —
// the routes really are still on the device.
func (s *Store) Unlink(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM accounts WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns one account.
func (s *Store) Get(ctx context.Context, id string) (model.Account, error) {
	var a model.Account
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT id, provider, rider, label, auto_push FROM accounts WHERE id = ?`), id).
		Scan(&a.ID, &a.Provider, &a.Rider, &a.Label, &a.AutoPush)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Account{}, ErrNotFound
	}
	return a, err
}

// List returns every linked account, in a stable order.
func (s *Store) List(ctx context.Context) ([]model.Account, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, provider, rider, label, auto_push FROM accounts ORDER BY rider, provider`)
	if err != nil {
		return nil, fmt.Errorf("read accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []model.Account
	for rows.Next() {
		var a model.Account
		if err := rows.Scan(&a.ID, &a.Provider, &a.Rider, &a.Label, &a.AutoPush); err != nil {
			return nil, fmt.Errorf("read accounts: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAutoPush flips whether auto-sync's background push includes this
// account. A manual "Push to devices" click ignores it entirely — this only
// governs the unattended path (autoSyncIfEnabled, and the auto-import
// poller's own push afterward), never a push the rider triggered themselves.
func (s *Store) SetAutoPush(ctx context.Context, id string, enabled bool) error {
	result, err := s.db.ExecContext(ctx,
		s.dialect.Rebind(`UPDATE accounts SET auto_push = ?, updated_at = ? WHERE id = ?`),
		enabled, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func providerLabel(p model.Provider) string {
	switch p {
	case model.ProviderGarmin:
		return "Garmin"
	case model.ProviderWahoo:
		return "Wahoo"
	default:
		return string(p)
	}
}
