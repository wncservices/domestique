// Package blocklist stops a blocked rider's email from creating a new local
// identity.
//
// Auth0's own blocked flag (see internal/auth0mgmt's SetBlocked) only
// refuses sign-in for the identity an admin actually saw and blocked — it
// does nothing about a fresh signup with the same email, which on this
// tenant creates an entirely separate Auth0 identity (see AGENTS.md's own
// note on google-oauth2|<id> vs auth0|<id> for the same address). This
// package is the other half: a small local table of blocked email
// addresses, checked at the OIDC callback before a session is ever created,
// regardless of which Auth0 identity the token names.
//
// Plaintext, deliberately — the same reasoning accounts.rider,
// crew_members.rider and routes.uploaded_by already carry: an email here
// must be exact-match queryable on every sign-in, and internal/secrets'
// sealing is nondeterministic (a random nonce per call), so a sealed column
// can never be a lookup key without decrypting every row first.
package blocklist

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// ErrNotFound means the email was never blocked.
var ErrNotFound = errors.New("no such block")

// Store holds blocked email addresses.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

func schema(_ dbx.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS blocked_emails (
    email      TEXT PRIMARY KEY,
    blocked_by TEXT NOT NULL DEFAULT '',
    blocked_at TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT ''
);`
}

// UseDB puts the blocklist table in an already-open database — the same one
// holding everything else, so a deployment needs exactly one.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, dialect: d}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("migrate blocklist table: %w", err)
	}
	return store, nil
}

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// BlockedEmail is one entry on the blocklist.
type BlockedEmail struct {
	Email     string
	BlockedBy string
	BlockedAt string
	Reason    string
}

// Block adds email to the blocklist, or replaces an existing entry — an
// admin re-blocking the same address updates who/why/when rather than
// erroring on the duplicate.
func (s *Store) Block(ctx context.Context, email, blockedBy, reason string) error {
	email = normalizeEmail(email)
	if email == "" {
		return errors.New("blocklist: no email to block")
	}
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO blocked_emails (email, blocked_by, blocked_at, reason)
        VALUES (?, ?, ?, ?)
        ON CONFLICT (email) DO UPDATE SET
            blocked_by = excluded.blocked_by,
            blocked_at = excluded.blocked_at,
            reason     = excluded.reason`),
		email, strings.ToLower(strings.TrimSpace(blockedBy)), time.Now().UTC().Format(time.RFC3339), reason)
	if err != nil {
		return fmt.Errorf("block %s: %w", email, err)
	}
	return nil
}

// Unblock removes email from the blocklist. Removing one that is not there
// is not an error — the caller (the admin People page) does not need to
// know or care whether this is the first time.
func (s *Store) Unblock(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM blocked_emails WHERE email = ?`), normalizeEmail(email))
	return err
}

// IsBlocked reports whether email is currently blocked — the one call the
// OIDC callback makes on every sign-in, before a session is created.
func (s *Store) IsBlocked(ctx context.Context, email string) (bool, error) {
	email = normalizeEmail(email)
	if email == "" {
		return false, nil
	}
	var exists int
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT 1 FROM blocked_emails WHERE email = ?`), email).Scan(&exists)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("check blocklist: %w", err)
	default:
		return true, nil
	}
}

// List returns every blocked entry, newest first.
func (s *Store) List(ctx context.Context) ([]BlockedEmail, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT email, blocked_by, blocked_at, reason FROM blocked_emails ORDER BY blocked_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("read blocklist: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []BlockedEmail
	for rows.Next() {
		var b BlockedEmail
		if err := rows.Scan(&b.Email, &b.BlockedBy, &b.BlockedAt, &b.Reason); err != nil {
			return nil, fmt.Errorf("read blocklist: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
