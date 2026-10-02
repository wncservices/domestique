// Package sessions holds a rider's OIDC login: a server-side session behind
// an opaque cookie value, not a JWT in the cookie itself.
//
// The difference matters for one reason — logout. A JWT in a cookie is valid
// until it expires no matter what the app does; "signing out" can only ever
// discard the browser's copy, not end the session, because there is no
// session, only a self-contained token nobody but the issuer can revoke. A
// row in this table is the session, so deleting it is signing out for real.
//
// Sealed with the same key as everything else a rider connects — Komoot,
// Garmin — via internal/secrets, following internal/providerlink and
// internal/settings' shape closely enough to be a copy of it: same nil-safe
// CanStore, same schema-as-constant-per-dialect.
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

// Store holds sessions.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
	box     *secrets.Box
}

// schema returns the DDL as a constant per engine — see providerlink.schema
// for why this is not one Sprintf: gosec's taint analysis follows a formatted
// string into every later query built from the same dialect.
//
// token holds sha256(the real cookie value), not the bearer credential
// itself — see hashToken. The column name stays "token" rather than
// "token_hash": renaming it would need a migration for a purely cosmetic
// gain, since nothing outside this file ever reads the column directly.
func schema(d dbx.Dialect) string {
	const sqlite = `
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    identity   BLOB NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    rider_key  TEXT NOT NULL DEFAULT '',
    sub_key    TEXT NOT NULL DEFAULT ''
);`
	const postgres = `
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    identity   BYTEA NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    rider_key  TEXT NOT NULL DEFAULT '',
    sub_key    TEXT NOT NULL DEFAULT ''
);`

	if d.Name == dbx.Postgres.Name {
		return postgres
	}
	return sqlite
}

// riderKeyPurpose and subKeyPurpose separate the two stand-in columns, so a
// rider_key can never equal a sub_key for the same text.
const (
	riderKeyPurpose = "sessions.rider"
	subKeyPurpose   = "sessions.sub"
)

// normaliseRider is the form a rider is compared in everywhere else (purge
// lower-cases and trims), so an admin's capitalisation cannot miss a session.
func normaliseRider(rider string) string { return strings.ToLower(strings.TrimSpace(rider)) }

// riderKey and subKey are what the table holds instead of the rider and the
// OIDC subject. The identity stays sealed (the table's whole design), but
// "end every session of this rider" needs an index, and decrypting every row
// to find them does not scale and cannot be done in SQL. A keyed HMAC is
// findable without being readable: a copy of the table shows which sessions
// share a rider, never who, and the low-entropy name cannot be brute-forced
// without the key. See secrets.Box.MAC. An empty input has no key: "" is
// "unkeyed", which DeleteRider/DeleteSub refuse to match on.
func (s *Store) riderKey(rider string) string {
	rider = normaliseRider(rider)
	if rider == "" {
		return ""
	}
	return s.box.MAC(riderKeyPurpose, rider)
}

func (s *Store) subKey(sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return ""
	}
	return s.box.MAC(subKeyPurpose, sub)
}

// UseDB puts the table in an already-open database.
//
// The box may be nil, in which case no session can be created — the same
// rule as providerlink and settings, for the same reason: a deployment
// without an encryption key has nowhere safe to put one.
func UseDB(db *sql.DB, dsn string, box *secrets.Box) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db, dialect: d, box: box}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("create sessions table: %w", err)
	}
	if err := store.migrate(); err != nil {
		return nil, err
	}
	return store, nil
}

// migrate brings a table that predates the rider and sub keys up to date, and
// is safe to run on every start.
//
// CREATE TABLE IF NOT EXISTS leaves an existing table alone, so the columns
// are added separately. Rows from before have no key and so cannot be found by
// DeleteRider: a removed rider's old login would outlive the removal until it
// expired, up to a month. Rather than expiring everyone, the keys are
// backfilled by opening each such row once with the key the server already
// holds. A row that will not open (wrong key, tampered) is deleted: nobody
// could use it, and leaving it unkeyed would leave a row no removal can reach.
// With no key nothing can be opened, so nothing is touched.
func (s *Store) migrate() error {
	for _, col := range []string{"rider_key", "sub_key"} {
		// #nosec G701 -- col comes from the literal list above.
		_, err := s.db.Exec(`ALTER TABLE sessions ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "already exists") {
				return fmt.Errorf("sessions: adding %s: %w", col, err)
			}
		}
	}
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS sessions_rider_key ON sessions (rider_key)`,
		`CREATE INDEX IF NOT EXISTS sessions_sub_key ON sessions (sub_key)`,
	} {
		if _, err := s.db.Exec(idx); err != nil {
			return fmt.Errorf("sessions: creating index: %w", err)
		}
	}
	if !s.CanStore() {
		return nil
	}

	// Unkeyed rows: every legacy row, plus any whose sealed identity names no
	// rider (which Create refuses to make, so only a corrupt one).
	rows, err := s.db.Query(`SELECT token, identity FROM sessions WHERE rider_key = ''`)
	if err != nil {
		return fmt.Errorf("sessions: reading unkeyed rows: %w", err)
	}
	type unkeyed struct {
		token  string
		sealed []byte
	}
	var pending []unkeyed
	for rows.Next() {
		var u unkeyed
		if err := rows.Scan(&u.token, &u.sealed); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sessions: reading unkeyed rows: %w", err)
		}
		pending = append(pending, u)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, u := range pending {
		var stored storedIdentity
		raw, err := s.box.Open(u.sealed)
		if err == nil {
			err = json.Unmarshal([]byte(raw), &stored)
		}
		if err != nil || s.riderKey(stored.User) == "" {
			// #nosec G701 -- constant statement, bound parameter.
			if _, err := s.db.Exec(s.dialect.Rebind(`DELETE FROM sessions WHERE token = ?`), u.token); err != nil {
				return fmt.Errorf("sessions: dropping an unreadable row: %w", err)
			}
			continue
		}
		// #nosec G701 -- constant statement, bound parameters.
		if _, err := s.db.Exec(s.dialect.Rebind(`UPDATE sessions SET rider_key = ?, sub_key = ? WHERE token = ?`),
			s.riderKey(stored.User), s.subKey(stored.Sub), u.token); err != nil {
			return fmt.Errorf("sessions: keying a legacy row: %w", err)
		}
	}
	return nil
}

// CanStore reports whether a session can be created at all.
//
// Nil-safe on purpose, same rule as providerlink.Store.CanStore and
// settings.Store.CanStore: a Server built before its sessions store exists,
// or in a mode that never needs one, is a valid configuration. Do not
// "simplify" the receiver check away.
func (s *Store) CanStore() bool { return s != nil && s.box != nil }

// storedIdentity is what actually gets sealed — deliberately not the whole
// auth.Identity. Role is recomputed from Groups on every Lookup by
// auth.Authenticator.identifyFromSession, never trusted from storage, so it
// has no business being in the ciphertext in the first place.
type storedIdentity struct {
	User   string   `json:"user"`
	Name   string   `json:"name,omitempty"`
	Email  string   `json:"email,omitempty"`
	Groups []string `json:"groups,omitempty"`
	Sub    string   `json:"sub,omitempty"`
}

// Create issues a session for id and returns the opaque cookie value.
//
// Opportunistically deletes expired rows first, so the table self-prunes
// without a background goroutine — nothing else in this codebase runs a
// ticker, and a personal deployment's session count never justifies adding
// one.
func (s *Store) Create(id auth.Identity, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	if !s.CanStore() {
		return "", time.Time{}, secrets.ErrNoKey
	}
	if id.User == "" {
		return "", time.Time{}, fmt.Errorf("sessions: refusing to create a session for nobody")
	}

	now := time.Now().UTC()
	expiresAt = now.Add(ttl)

	raw, err := json.Marshal(storedIdentity{
		User: id.User, Name: id.Name, Email: id.Email, Groups: id.Groups, Sub: id.Sub,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sessions: encoding identity: %w", err)
	}
	sealed, err := s.box.Seal(string(raw))
	if err != nil {
		return "", time.Time{}, err
	}

	tok, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}

	// #nosec G701 -- constant statement, bound parameters; see the same
	// pattern and reasoning in settings.Store.Set.
	if _, err := s.db.Exec(s.dialect.Rebind(
		`DELETE FROM sessions WHERE expires_at < ?`), now.Format(time.RFC3339)); err != nil {
		return "", time.Time{}, fmt.Errorf("sessions: pruning expired rows: %w", err)
	}

	// #nosec G701 -- constant statement, bound parameters.
	if _, err := s.db.Exec(s.dialect.Rebind(
		`INSERT INTO sessions (token, identity, created_at, expires_at, rider_key, sub_key) VALUES (?, ?, ?, ?, ?, ?)`),
		hashToken(tok), sealed, now.Format(time.RFC3339), expiresAt.Format(time.RFC3339),
		s.riderKey(id.User), s.subKey(id.Sub)); err != nil {
		return "", time.Time{}, fmt.Errorf("sessions: creating session: %w", err)
	}
	return tok, expiresAt, nil
}

// Lookup resolves a session token to who it belongs to. Satisfies
// auth.SessionLookup.
//
// A DB error, a corrupt or tampered ciphertext, and an expired row are all
// just "no session" — Identify has no channel to surface an error through,
// so this can never be allowed to be one the caller has to check. An expired
// row found here is deleted opportunistically rather than left for the next
// Create to clean up, since a token that outlives its own row would
// otherwise keep answering true for as long as nothing else logs in.
func (s *Store) Lookup(token string) (auth.Identity, bool) {
	if !s.CanStore() || token == "" {
		return auth.Identity{}, false
	}

	var sealed []byte
	var expires string
	// #nosec G701 -- constant statement, bound parameter.
	err := s.db.QueryRow(s.dialect.Rebind(
		`SELECT identity, expires_at FROM sessions WHERE token = ?`), hashToken(token)).
		Scan(&sealed, &expires)
	if err != nil {
		return auth.Identity{}, false
	}

	expiresAt, err := time.Parse(time.RFC3339, expires)
	if err != nil || time.Now().UTC().After(expiresAt) {
		_ = s.Delete(token)
		return auth.Identity{}, false
	}

	raw, err := s.box.Open(sealed)
	if err != nil {
		return auth.Identity{}, false
	}
	var stored storedIdentity
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return auth.Identity{}, false
	}
	return auth.Identity{
		User: stored.User, Name: stored.Name, Email: stored.Email, Groups: stored.Groups, Sub: stored.Sub,
	}, true
}

// UpdateName rewrites the display name held in an existing session, so a
// rider who changes their name through Settings sees it reflected right
// away rather than waiting up to sessionTTL for a fresh login to pick it up
// from the ID token again. A no-op, not an error, when the token does not
// resolve to a live session — the Management API write this follows has
// already succeeded either way, and the next real login will carry the new
// name regardless.
func (s *Store) UpdateName(token, name string) error {
	if !s.CanStore() || token == "" {
		return nil
	}
	id, ok := s.Lookup(token)
	if !ok {
		return nil
	}

	raw, err := json.Marshal(storedIdentity{
		User: id.User, Name: name, Email: id.Email, Groups: id.Groups, Sub: id.Sub,
	})
	if err != nil {
		return fmt.Errorf("sessions: encoding identity: %w", err)
	}
	sealed, err := s.box.Seal(string(raw))
	if err != nil {
		return err
	}

	// #nosec G701 -- constant statement, bound parameters.
	_, err = s.db.Exec(s.dialect.Rebind(`UPDATE sessions SET identity = ? WHERE token = ?`), sealed, hashToken(token))
	return err
}

// Delete ends a session. Deleting one that is not there is not an error —
// same rule as providerlink.Delete and settings.Delete: a rider signing out
// twice, or a client replaying a stale cookie, is not a failure.
func (s *Store) Delete(token string) error {
	if s == nil || token == "" {
		return nil
	}
	// #nosec G701 -- constant statement, bound parameter.
	_, err := s.db.Exec(s.dialect.Rebind(`DELETE FROM sessions WHERE token = ?`), hashToken(token))
	return err
}

// DeleteRider ends every session a rider holds, on every device, and says how
// many it ended. This is what makes removing a rider log them out now rather
// than whenever their cookie expires (a month): the session is the credential,
// and the rider's data being gone does not stop a live cookie authenticating
// as them.
//
// Not an error when there are none, and safe to repeat: purge is retried
// after a partial failure. Matching is on the rider key (see riderKey), never
// by opening rows. Nil-safe, like Delete: a deployment with no sessions store
// has none to end.
func (s *Store) DeleteRider(ctx context.Context, rider string) (int, error) {
	if !s.CanStore() {
		return 0, nil
	}
	key := s.riderKey(rider)
	if key == "" {
		return 0, nil
	}
	// #nosec G701 -- constant statement, bound parameter.
	res, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM sessions WHERE rider_key = ?`), key)
	if err != nil {
		return 0, fmt.Errorf("sessions: ending a rider's sessions: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteSub ends every session of one OIDC identity (the issuer's subject).
// The rider name an admin removes someone under is a guess the UI offers and
// can be blank or wrong; the subject is the id of the very identity being
// deleted, so it is the reliable key for removal, and for forcing a fresh
// login after a role change. An empty sub matches nothing: it would
// otherwise match every session made without one.
func (s *Store) DeleteSub(ctx context.Context, sub string) error {
	if !s.CanStore() {
		return nil
	}
	key := s.subKey(sub)
	if key == "" {
		return nil
	}
	// #nosec G701 -- constant statement, bound parameter.
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM sessions WHERE sub_key = ?`), key); err != nil {
		return fmt.Errorf("sessions: ending an identity's sessions: %w", err)
	}
	return nil
}

// RiderKey is the value DeleteRider matches on, exposed so a test can look for
// a rider's rows by it.
func (s *Store) RiderKey(rider string) string { return s.riderKey(rider) }

// newToken is 32 random bytes, URL-safe base64 — opaque, unguessable, and
// plain enough to be a cookie value with no further encoding.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("sessions: generating token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken is what actually goes in the sessions table — never the token
// itself. The cookie value is the bearer credential for a live session;
// storing it verbatim would mean anyone with read access to a DB backup, a
// replica, or a future SQL-injection bug elsewhere in the stack could set
// that cookie and become whoever it belonged to, with the encryption key on
// `identity` buying nothing (the server holds that key too, so it protects
// against reading the row, not against replaying it). The token is 32
// bytes of crypto/rand, so a plain, unsalted SHA-256 is enough — there is
// no low-entropy secret here for a rainbow table to help with, unlike a
// password hash.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
