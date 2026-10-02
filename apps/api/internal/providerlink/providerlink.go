// Package providerlink stores each rider's sign-in to an outside service.
//
// A rider connects through the UI: they type the email and password for their
// Komoot or Garmin account, the server signs in once, and **the password is
// discarded**. What the sign-in returns — a session token, an OAuth1 token
// pair — is what the API actually wants afterwards, so that is what is kept,
// encrypted, and the password never reaches the database at all. If the stored
// session expires the rider reconnects, which is a far better failure than
// storing a reusable password to avoid it.
//
// One connection per rider per provider, keyed to the Authelia username taken
// from the session — the same rule as a linked head unit, for the same reason:
// letting the request body name the rider would let somebody connect an
// account on another rider's behalf.
//
// # Why one table and not one per provider
//
// Komoot came first and had its own table. Garmin needs the same six columns,
// the same encryption, the same upsert and the same "reads work without a key
// but writes do not" rule; Wahoo will need them again. Three copies of that is
// three places to fix a mistake in. The provider is a column, and what each
// provider keeps in Secret is opaque here — Komoot stores a token, Garmin a
// JSON-encoded session. This package neither knows nor cares.
package providerlink

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

// ErrNotFound means the rider has not connected that provider.
var ErrNotFound = errors.New("no connection for that rider and provider")

// Link is one rider's connection as it can safely be shown.
//
// The secret is deliberately absent: it leaves this package only through
// Secret, so it cannot be serialised into an API response by accident.
type Link struct {
	Provider    string
	Rider       string
	Email       string
	DisplayName string
	ExternalID  string
	UpdatedAt   time.Time
}

// Connection is what a fresh sign-in produced, on its way to being stored.
//
// A struct rather than four more string parameters: Save already takes a
// provider and a rider, and six positional strings is a transposition waiting
// to happen — swapping Email and DisplayName would compile and be wrong.
type Connection struct {
	// Email is what the rider typed, kept so the UI can show whose account
	// this is. Not a credential.
	Email string
	// DisplayName is the account's own name at the provider, when it says.
	DisplayName string
	// ExternalID is the provider's id for the account, when there is one.
	ExternalID string
	// Secret is whatever must be kept to resume the session. Opaque here and
	// the only field that is encrypted.
	Secret string
}

// Store holds the connections.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
	box     *secrets.Box
}

// schema returns the DDL as a constant per engine rather than formatting the
// column type in. Two near-identical literals read worse than one Sprintf, but
// gosec's taint analysis follows a formatted string into every later query
// built from the same dialect and reports each one as SQL injection. Constants
// end that at the source, and the only difference is the secret column type.
func schema(d dbx.Dialect) string {
	const sqlite = `
CREATE TABLE IF NOT EXISTS provider_links (
    provider     TEXT NOT NULL,
    rider        TEXT NOT NULL,
    email        TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    external_id  TEXT NOT NULL DEFAULT '',
    secret       BLOB NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (provider, rider)
);`
	const postgres = `
CREATE TABLE IF NOT EXISTS provider_links (
    provider     TEXT NOT NULL,
    rider        TEXT NOT NULL,
    email        TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    external_id  TEXT NOT NULL DEFAULT '',
    secret       BYTEA NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (provider, rider)
);`

	if d.Name == dbx.Postgres.Name {
		return postgres
	}
	return sqlite
}

// UseDB puts the table in an already-open database, alongside the routes and
// the accounts.
//
// The box may be nil. Reads still work, so an operator who removes the key
// sees the connections they have rather than a crash — but Secret and Save
// refuse, which is what keeps a session from ever being written in clear.
func UseDB(db *sql.DB, dsn string, box *secrets.Box) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db, dialect: d, box: box}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("create provider_links table: %w", err)
	}
	if err := store.adoptKomootLinks(); err != nil {
		return nil, err
	}
	return store, nil
}

// adoptKomootLinks copies connections made before this table existed.
//
// The rows move as they are: the sealed token was encrypted with the same key
// and is opaque to both tables, so no re-encryption is involved and a rider
// who connected Komoot yesterday does not have to sign in again.
//
// Only rows with nothing already here are copied, so running twice is safe and
// a fresh connection is never overwritten by the fossil. The old table is left
// in place rather than dropped: rolling back to the previous image would
// otherwise lose every Komoot connection, and a spare table costs nothing. A
// later release can drop it once no deployment runs that image.
func (s *Store) adoptKomootLinks() error {
	if !s.tableExists("komoot_links") {
		return nil
	}

	_, err := s.db.Exec(`
INSERT INTO provider_links (provider, rider, email, display_name, external_id, secret, updated_at)
SELECT 'komoot', old.rider, old.email, old.display_name, old.user_id, old.token, old.updated_at
FROM komoot_links AS old
WHERE NOT EXISTS (
    SELECT 1 FROM provider_links AS new
    WHERE new.provider = 'komoot' AND new.rider = old.rider
)`)
	if err != nil {
		return fmt.Errorf("adopt komoot_links: %w", err)
	}
	return nil
}

// tableExists asks the database rather than trying and inspecting the error,
// because "no such table" is worded differently by each driver.
func (s *Store) tableExists(name string) bool {
	var query string
	if s.dialect.Name == dbx.Postgres.Name {
		query = `SELECT 1 FROM information_schema.tables
                 WHERE table_schema = current_schema() AND table_name = ?`
	} else {
		query = `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`
	}

	var found int
	err := s.db.QueryRow(s.dialect.Rebind(query), name).Scan(&found)
	return err == nil
}

// CanStore reports whether a connection can be saved at all. The UI asks
// before offering the form, so a rider is not invited to type a password that
// cannot be kept.
//
// **Nil-safe on purpose**, and handlers rely on it: a Server with no store is
// a valid configuration, and every caller would otherwise need a nil check
// before this one. Do not "simplify" the receiver check away — it reads like a
// redundant guard and is the reason `s.Links.CanStore()` cannot panic.
// TestGarminHandlersSurviveNoStore fails if it goes.
func (s *Store) CanStore() bool { return s != nil && s.box != nil }

// Save records a connection, replacing any the rider already had.
//
// Reconnecting is the documented fix for an expired session, so this is an
// upsert rather than an error on conflict.
func (s *Store) Save(provider, rider string, c Connection) (Link, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	rider = strings.ToLower(strings.TrimSpace(rider))
	if provider == "" {
		return Link{}, errors.New("no provider: a connection is to something")
	}
	if rider == "" {
		return Link{}, errors.New("no rider: a connection belongs to whoever made it")
	}
	if !s.CanStore() {
		return Link{}, secrets.ErrNoKey
	}
	if c.Secret == "" {
		return Link{}, errors.New("the sign-in returned no session to store")
	}

	sealed, err := s.box.Seal(c.Secret)
	if err != nil {
		return Link{}, err
	}

	now := time.Now().UTC()
	// #nosec G701 -- the statement is a constant and every value is a bound
	// parameter; Rebind only swaps ? for $N. gosec's taint analysis follows
	// the secret through Seal into this call and cannot see that. The
	// structurally identical statements in internal/accounts are not flagged.
	_, err = s.db.Exec(s.dialect.Rebind(`
INSERT INTO provider_links (provider, rider, email, display_name, external_id, secret, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider, rider) DO UPDATE SET
    email        = excluded.email,
    display_name = excluded.display_name,
    external_id  = excluded.external_id,
    secret       = excluded.secret,
    updated_at   = excluded.updated_at`),
		provider, rider, strings.TrimSpace(c.Email), strings.TrimSpace(c.DisplayName),
		c.ExternalID, sealed, now.Format(time.RFC3339))
	if err != nil {
		return Link{}, fmt.Errorf("save %s link: %w", provider, err)
	}

	return Link{
		Provider:    provider,
		Rider:       rider,
		Email:       strings.TrimSpace(c.Email),
		DisplayName: strings.TrimSpace(c.DisplayName),
		ExternalID:  c.ExternalID,
		UpdatedAt:   now,
	}, nil
}

// Get returns a rider's connection without its secret.
func (s *Store) Get(provider, rider string) (Link, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	rider = strings.ToLower(strings.TrimSpace(rider))

	var link Link
	var updated string
	err := s.db.QueryRow(s.dialect.Rebind(`
SELECT provider, rider, email, display_name, external_id, updated_at
FROM provider_links WHERE provider = ? AND rider = ?`), provider, rider).
		Scan(&link.Provider, &link.Rider, &link.Email, &link.DisplayName, &link.ExternalID, &updated)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Link{}, ErrNotFound
	case err != nil:
		return Link{}, err
	}

	link.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return link, nil
}

// Secret returns what the provider's client needs to resume a session.
//
// Separate from Get so that reading a connection for display never decrypts
// anything: the secret is fetched only where it is about to be used.
func (s *Store) Secret(provider, rider string) (externalID, secret string, err error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	rider = strings.ToLower(strings.TrimSpace(rider))
	if !s.CanStore() {
		return "", "", secrets.ErrNoKey
	}

	var sealed []byte
	// #nosec G701 -- constant statement, bound parameters; see Save.
	err = s.db.QueryRow(s.dialect.Rebind(
		`SELECT external_id, secret FROM provider_links WHERE provider = ? AND rider = ?`),
		provider, rider).
		Scan(&externalID, &sealed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", ErrNotFound
	case err != nil:
		return "", "", err
	}

	secret, err = s.box.Open(sealed)
	if err != nil {
		return "", "", fmt.Errorf("%s session for %s: %w — reconnect to replace it", provider, rider, err)
	}
	return externalID, secret, nil
}

// ListRiders returns every rider currently connected to the given provider,
// in a stable order — who the auto-import poller has to ask, since there is
// no other index of "who has signed in to Komoot" the way accounts.Store
// tracks push targets for Garmin and Wahoo (Komoot has no accounts.Store
// row at all: it is never a push target, only ever a source to pull from).
func (s *Store) ListRiders(provider string) ([]string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	rows, err := s.db.Query(s.dialect.Rebind(`
        SELECT rider FROM provider_links WHERE provider = ? ORDER BY rider`), provider)
	if err != nil {
		return nil, fmt.Errorf("list %s riders: %w", provider, err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var rider string
		if err := rows.Scan(&rider); err != nil {
			return nil, fmt.Errorf("list %s riders: %w", provider, err)
		}
		out = append(out, rider)
	}
	return out, rows.Err()
}

// DeleteRider removes every connection a rider has, of any provider, for when
// the rider themselves is deleted (see the API package's purgeRiderData).
//
// It also removes the rider from komoot_links, the table adoptKomootLinks
// leaves behind on purpose. Without that, the next start would copy the
// fossil's sealed Komoot token straight back into provider_links — the one
// copy a purge of provider_links alone would resurrect.
func (s *Store) DeleteRider(rider string) error {
	rider = strings.ToLower(strings.TrimSpace(rider))
	if rider == "" {
		return errors.New("providerlink: no rider to delete")
	}
	if _, err := s.db.Exec(s.dialect.Rebind(`DELETE FROM provider_links WHERE rider = ?`), rider); err != nil {
		return err
	}
	if s.tableExists("komoot_links") {
		if _, err := s.db.Exec(s.dialect.Rebind(`DELETE FROM komoot_links WHERE rider = ?`), rider); err != nil {
			return fmt.Errorf("remove legacy komoot_links row: %w", err)
		}
	}
	return nil
}

// Delete removes a rider's connection. Deleting one that is not there is not
// an error: the caller wanted it gone, and it is.
func (s *Store) Delete(provider, rider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	rider = strings.ToLower(strings.TrimSpace(rider))
	_, err := s.db.Exec(s.dialect.Rebind(
		`DELETE FROM provider_links WHERE provider = ? AND rider = ?`), provider, rider)
	return err
}
