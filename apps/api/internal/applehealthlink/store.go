// Package applehealthlink holds the hashed per-rider token of the Apple Health
// shortcut: the private URL an iPhone shortcut posts a rider's recovery
// signals to. It is a sibling of internal/calendarfeed and follows its rules.
package applehealthlink

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// Status is what a rider's profile shows about their shortcut. It never holds
// the token, which exists only in the response that creates it.
type Status struct {
	Linked bool
	// CreatedAt and LastReceivedAt are RFC 3339 strings, empty when unset.
	// LastReceivedAt stays empty until the shortcut has posted once.
	CreatedAt      string
	LastReceivedAt string
}

// Store holds one shortcut token per rider, hashed.
//
// The column holds sha256(token), never the token: a 256-bit crypto/rand value
// needs no salt, and a leaked table must not be a list of live posting URLs.
// The price is that a lost URL cannot be shown again; the rider regenerates.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

func schema(_ dbx.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS apple_health_links (
    rider            TEXT PRIMARY KEY,
    token_hash       TEXT NOT NULL UNIQUE,
    created_at       TEXT NOT NULL,
    last_received_at TEXT NOT NULL DEFAULT ''
)`
}

// UseDB puts the apple_health_links table in an already-open database.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("migrate apple_health_links table: %w", err)
	}
	return &Store{db: db, dialect: d}, nil
}

func normalize(rider string) string { return strings.ToLower(strings.TrimSpace(rider)) }

// Regenerate gives rider a new shortcut token, replacing any earlier one in a
// single upsert: the old URL stops working at once. The returned token is the
// only time it exists.
func (s *Store) Regenerate(ctx context.Context, rider string) (string, error) {
	rider = normalize(rider)
	if rider == "" {
		return "", errors.New("applehealthlink: no rider")
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO apple_health_links (rider, token_hash, created_at, last_received_at)
        VALUES (?, ?, ?, '')
        ON CONFLICT (rider) DO UPDATE SET
            token_hash = excluded.token_hash,
            created_at = excluded.created_at,
            last_received_at = ''`),
		rider, hashToken(token), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("applehealthlink: regenerate: %w", err)
	}
	return token, nil
}

// Lookup resolves a raw token to the rider who owns it. An unknown, replaced
// or revoked token is "" with a nil error, so the caller answers every such
// request the same way.
func (s *Store) Lookup(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", nil
	}
	var rider string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT rider FROM apple_health_links WHERE token_hash = ?`), hashToken(token)).Scan(&rider)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("applehealthlink: lookup: %w", err)
	}
	return rider, nil
}

// Status reports whether rider has a shortcut and when it last posted.
func (s *Store) Status(ctx context.Context, rider string) (Status, error) {
	var created, received string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT created_at, last_received_at FROM apple_health_links WHERE rider = ?`), normalize(rider)).Scan(&created, &received)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("applehealthlink: status: %w", err)
	}
	return Status{Linked: true, CreatedAt: created, LastReceivedAt: received}, nil
}

// Linked reports whether rider has a shortcut token.
func (s *Store) Linked(ctx context.Context, rider string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT 1 FROM apple_health_links WHERE rider = ?`), normalize(rider)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("applehealthlink: linked: %w", err)
	}
	return true, nil
}

// Revoke deletes rider's shortcut row; the URL stops working at once. Revoking
// what is not there is not an error, as with every other delete here.
func (s *Store) Revoke(ctx context.Context, rider string) error {
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM apple_health_links WHERE rider = ?`), normalize(rider)); err != nil {
		return fmt.Errorf("applehealthlink: revoke: %w", err)
	}
	return nil
}

// DeleteRider is what removing a rider calls; the same as Revoke, named for
// the purge that registers it.
func (s *Store) DeleteRider(ctx context.Context, rider string) error {
	if normalize(rider) == "" {
		return errors.New("applehealthlink: no rider to delete")
	}
	return s.Revoke(ctx, rider)
}

// Touch records that a post was accepted for rider. It writes on every call:
// a shortcut posts about once a day, so there is nothing to throttle.
func (s *Store) Touch(ctx context.Context, rider string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        UPDATE apple_health_links SET last_received_at = ? WHERE rider = ?`),
		now.UTC().Format(time.RFC3339), normalize(rider)); err != nil {
		return fmt.Errorf("applehealthlink: touch: %w", err)
	}
	return nil
}

// newToken is 32 random bytes, base64url: 43 characters, the same shape as a
// calendar feed token.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("applehealthlink: generating token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
