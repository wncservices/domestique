package calendarfeed

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

// ErrNotFound covers a token nothing matches and, deliberately
// indistinguishable from that, one that was revoked or replaced. A caller
// probing guessed tokens must not be able to tell the two apart.
var ErrNotFound = errors.New("no such calendar feed")

// touchEvery is how rarely last_fetched_at is written: a calendar app that
// polls every few minutes must not turn into a database write per poll.
const touchEvery = time.Hour

// Status is what a rider's profile shows about their feed. It never holds the
// token, which exists only in the response that creates it.
type Status struct {
	Active    bool
	CreatedAt time.Time
	// LastFetchedAt is zero until a calendar app has fetched the feed.
	LastFetchedAt time.Time
}

// Store holds one feed token per rider, hashed.
//
// Follows internal/sessions and internal/routeshare: the column holds
// sha256(token), never the token. A 256-bit crypto/rand value needs no salt,
// and storing it verbatim would make a leaked table a list of live URLs. The
// price is that a lost URL cannot be shown again; the rider regenerates.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

func schema(_ dbx.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS calendar_feeds (
    rider           TEXT PRIMARY KEY,
    token_hash      TEXT NOT NULL UNIQUE,
    created_at      TEXT NOT NULL,
    last_fetched_at TEXT NOT NULL DEFAULT ''
)`
}

// UseDB puts the calendar_feeds table in an already-open database.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("migrate calendar_feeds table: %w", err)
	}
	return &Store{db: db, dialect: d}, nil
}

func normalize(rider string) string { return strings.ToLower(strings.TrimSpace(rider)) }

// Regenerate gives rider a new feed token, replacing any earlier one in a
// single upsert: the old URL stops working at once, so a leaked link is one
// click to kill. The returned token is the only time it exists.
func (s *Store) Regenerate(ctx context.Context, rider string) (string, error) {
	rider = normalize(rider)
	if rider == "" {
		return "", errors.New("calendarfeed: no rider")
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO calendar_feeds (rider, token_hash, created_at, last_fetched_at)
        VALUES (?, ?, ?, '')
        ON CONFLICT (rider) DO UPDATE SET
            token_hash = excluded.token_hash,
            created_at = excluded.created_at,
            last_fetched_at = ''`),
		rider, hashToken(token), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("calendarfeed: regenerate: %w", err)
	}
	return token, nil
}

// Lookup resolves a raw token to the rider who owns it. ErrNotFound for an
// unknown, replaced or revoked one, all the same.
func (s *Store) Lookup(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrNotFound
	}
	var rider string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT rider FROM calendar_feeds WHERE token_hash = ?`), hashToken(token)).Scan(&rider)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("calendarfeed: lookup: %w", err)
	}
	return rider, nil
}

// Status reports whether rider has a feed and when it was last fetched.
func (s *Store) Status(ctx context.Context, rider string) (Status, error) {
	var created, fetched string
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT created_at, last_fetched_at FROM calendar_feeds WHERE rider = ?`), normalize(rider)).Scan(&created, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("calendarfeed: status: %w", err)
	}
	st := Status{Active: true}
	if st.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return Status{}, fmt.Errorf("calendarfeed: corrupt created_at: %w", err)
	}
	if fetched != "" {
		if st.LastFetchedAt, err = time.Parse(time.RFC3339, fetched); err != nil {
			return Status{}, fmt.Errorf("calendarfeed: corrupt last_fetched_at: %w", err)
		}
	}
	return st, nil
}

// Revoke deletes rider's feed; the URL stops working at once. Revoking what is
// not there is not an error, as with every other delete in this codebase.
func (s *Store) Revoke(ctx context.Context, rider string) error {
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM calendar_feeds WHERE rider = ?`), normalize(rider)); err != nil {
		return fmt.Errorf("calendarfeed: revoke: %w", err)
	}
	return nil
}

// DeleteRider is what removing a rider calls; the same as Revoke, named for
// the purge that registers it.
func (s *Store) DeleteRider(ctx context.Context, rider string) error {
	if normalize(rider) == "" {
		return errors.New("calendarfeed: no rider to delete")
	}
	return s.Revoke(ctx, rider)
}

// Touch records that rider's feed was fetched, writing at most once an hour:
// a fetch inside the hour changes nothing. The condition is in the statement,
// so replicas cannot both write.
func (s *Store) Touch(ctx context.Context, rider string, now time.Time) error {
	now = now.UTC()
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        UPDATE calendar_feeds SET last_fetched_at = ?
        WHERE rider = ? AND (last_fetched_at = '' OR last_fetched_at <= ?)`),
		now.Format(time.RFC3339), normalize(rider), now.Add(-touchEvery).Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("calendarfeed: touch: %w", err)
	}
	return nil
}

// newToken is 32 random bytes, base64url: 43 characters, the same shape as a
// session or a route share.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("calendarfeed: generating token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
