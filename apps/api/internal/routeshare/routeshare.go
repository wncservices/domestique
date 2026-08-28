// Package routeshare lets a rider hand one route to somebody who isn't on
// this deployment at all — no crew, no account yet, nothing beyond a link.
//
// Every access-control primitive elsewhere in this app (viewer/rider/admin,
// crew membership) is deployment-wide once granted: a viewer sees every
// route shared to a crew they're in, an admin sees everything. A route
// share is deliberately narrower and shaped differently — a single,
// unguessable, revocable, expiring link that grants exactly one route's
// worth of read access, to whoever holds it, once they're signed in. It is
// not a role and it does not touch internal/crew or config.VisibleTo at
// all: the routes this package's own endpoints serve are fetched by token,
// not folded into the ordinary library listing.
//
// Follows internal/sessions' own shape closely: a share's id in this table
// is sha256(the raw token), never the token itself — see sessions.hashToken
// for the reasoning, which applies unchanged here. The raw token exists
// only in the HTTP response body at creation time, and in whatever channel
// the rider shares it through afterward; a database leak of this table
// leaks no live link.
package routeshare

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

// ErrNotFound covers a token or id nothing matches, and — deliberately
// indistinguishable from that — one that matches a revoked share. A caller
// probing a guessed token must not be able to tell "never existed" apart
// from "existed once." An *expired* share is not folded into this: Lookup
// still returns it (Share.Expired reports true), because unlike existence,
// a link's expiry is not something worth hiding.
var ErrNotFound = errors.New("no such share link")

// Share is one link to one route.
type Share struct {
	// ID is sha256(token) hex — never the token itself. See the package doc.
	ID        string
	RouteSlug string
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
	// RevokedAt is nil until the owner revokes the link early.
	RevokedAt *time.Time
}

// Expired reports whether now is past this share's expiry — independent of
// whether it was also revoked.
func (s Share) Expired(now time.Time) bool { return now.After(s.ExpiresAt) }

// Revoked reports whether the owner ended this share early.
func (s Share) Revoked() bool { return s.RevokedAt != nil }

// Redemption is one rider's access to a share. Recorded — and, on a repeat
// visit, updated — by Touch on every successful view rather than a
// separate one-time "redeem" step: there is no distinct claimed-vs-viewing
// state to reason about, and this same row is both the access grant and
// the "who's seen it" list ListForRoute's own callers show the owner.
type Redemption struct {
	Rider      string
	RedeemedAt time.Time
}

// Store holds share links.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

// allowedTTLs are the only link lifetimes this package accepts — a
// deliberately short, fixed menu rather than an arbitrary duration, so a
// link is never created with no expiry at all. Enforced here, not only in
// the API layer's request validation, the same way schedule.Store enforces
// its own seriesIntervals rather than trusting the caller.
var allowedTTLs = map[time.Duration]bool{
	7 * 24 * time.Hour:  true,
	30 * 24 * time.Hour: true,
	90 * 24 * time.Hour: true,
}

func schema(_ dbx.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS route_shares (
    id         TEXT PRIMARY KEY,
    route_slug TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_route_shares_slug ON route_shares (route_slug);

CREATE TABLE IF NOT EXISTS route_share_redemptions (
    share_id    TEXT NOT NULL,
    rider       TEXT NOT NULL,
    redeemed_at TEXT NOT NULL,
    PRIMARY KEY (share_id, rider)
);`
}

// UseDB puts the route_shares and route_share_redemptions tables in an
// already-open database — the same one holding the routes themselves.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, dialect: d}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("migrate route_shares table: %w", err)
	}
	return store, nil
}

// Create generates a new share link for routeSlug and returns the raw
// token — the only time it is ever available. Only its SHA-256 hash is
// stored; losing the returned token means the link is gone even though the
// row survives (the owner can only see it existed and revoke it, per
// ListForRoute).
func (s *Store) Create(ctx context.Context, routeSlug, createdBy string, ttl time.Duration) (token string, share Share, err error) {
	routeSlug = strings.TrimSpace(routeSlug)
	createdBy = strings.ToLower(strings.TrimSpace(createdBy))
	switch {
	case routeSlug == "":
		return "", Share{}, errors.New("routeshare: no route — what is being shared?")
	case createdBy == "":
		return "", Share{}, errors.New("routeshare: no rider — who is sharing this?")
	case !allowedTTLs[ttl]:
		return "", Share{}, fmt.Errorf("routeshare: %s is not a supported link lifetime (7, 30 or 90 days)", ttl)
	}

	tok, err := newToken()
	if err != nil {
		return "", Share{}, err
	}
	now := time.Now().UTC()
	share = Share{
		ID:        hashToken(tok),
		RouteSlug: routeSlug,
		CreatedBy: createdBy,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO route_shares (id, route_slug, created_by, created_at, expires_at)
        VALUES (?, ?, ?, ?, ?)`),
		share.ID, share.RouteSlug, share.CreatedBy,
		share.CreatedAt.Format(time.RFC3339), share.ExpiresAt.Format(time.RFC3339)); err != nil {
		return "", Share{}, fmt.Errorf("routeshare: create: %w", err)
	}
	return tok, share, nil
}

// Get reads a share by its id (sha256(token) hex) — the owner-facing path,
// via DELETE /api/shares/{id}, where id is safe to expose since it is not
// the bearer secret. Unlike Lookup, a revoked share is still returned, not
// hidden as ErrNotFound: an owner managing their own shares needs to see
// one they already revoked, not have it vanish.
func (s *Store) Get(ctx context.Context, id string) (Share, error) {
	var share Share
	var createdAt, expiresAt string
	var revokedAt sql.NullString
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT id, route_slug, created_by, created_at, expires_at, revoked_at
        FROM route_shares WHERE id = ?`), id).
		Scan(&share.ID, &share.RouteSlug, &share.CreatedBy, &createdAt, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	if share.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return Share{}, fmt.Errorf("routeshare: corrupt created_at: %w", err)
	}
	if share.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt); err != nil {
		return Share{}, fmt.Errorf("routeshare: corrupt expires_at: %w", err)
	}
	if revokedAt.Valid {
		t, err := time.Parse(time.RFC3339, revokedAt.String)
		if err != nil {
			return Share{}, fmt.Errorf("routeshare: corrupt revoked_at: %w", err)
		}
		share.RevokedAt = &t
	}
	return share, nil
}

// Lookup resolves a raw token to its Share — the visitor-facing path, every
// /api/shares/{token}... endpoint. A revoked share (or a token matching
// nothing at all) is ErrNotFound either way; see the package's own doc
// comment for why that has to be indistinguishable. An expired share is
// still returned, so the caller can say "this link has expired"
// specifically rather than a generic not-found.
func (s *Store) Lookup(ctx context.Context, token string) (Share, error) {
	if token == "" {
		return Share{}, ErrNotFound
	}
	share, err := s.Get(ctx, hashToken(token))
	if err != nil {
		return Share{}, err
	}
	if share.Revoked() {
		return Share{}, ErrNotFound
	}
	return share, nil
}

// ListForRoute returns every share ever created for a route, most recently
// created first — active, expired and revoked alike, so an owner sees the
// full history of who a route has been shared with, not just what's live.
func (s *Store) ListForRoute(ctx context.Context, routeSlug string) ([]Share, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(`
        SELECT id, route_slug, created_by, created_at, expires_at, revoked_at
        FROM route_shares WHERE route_slug = ? ORDER BY created_at DESC`), routeSlug)
	if err != nil {
		return nil, fmt.Errorf("routeshare: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Share
	for rows.Next() {
		var share Share
		var createdAt, expiresAt string
		var revokedAt sql.NullString
		if err := rows.Scan(&share.ID, &share.RouteSlug, &share.CreatedBy, &createdAt, &expiresAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("routeshare: list: %w", err)
		}
		if share.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
			return nil, fmt.Errorf("routeshare: corrupt created_at: %w", err)
		}
		if share.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt); err != nil {
			return nil, fmt.Errorf("routeshare: corrupt expires_at: %w", err)
		}
		if revokedAt.Valid {
			t, err := time.Parse(time.RFC3339, revokedAt.String)
			if err != nil {
				return nil, fmt.Errorf("routeshare: corrupt revoked_at: %w", err)
			}
			share.RevokedAt = &t
		}
		out = append(out, share)
	}
	return out, rows.Err()
}

// Redemptions returns everyone who has viewed a share, most recently seen
// first — the "who's seen this" list shown alongside each share on the
// owner's own manage-shares panel.
func (s *Store) Redemptions(ctx context.Context, shareID string) ([]Redemption, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(`
        SELECT rider, redeemed_at FROM route_share_redemptions
        WHERE share_id = ? ORDER BY redeemed_at DESC`), shareID)
	if err != nil {
		return nil, fmt.Errorf("routeshare: redemptions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Redemption
	for rows.Next() {
		var r Redemption
		var redeemedAt string
		if err := rows.Scan(&r.Rider, &redeemedAt); err != nil {
			return nil, fmt.Errorf("routeshare: redemptions: %w", err)
		}
		if r.RedeemedAt, err = time.Parse(time.RFC3339, redeemedAt); err != nil {
			return nil, fmt.Errorf("routeshare: corrupt redeemed_at: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Touch records rider's access to shareID, updating redeemed_at if they've
// seen it before — called on every successful view of a shared route (the
// summary, the track, a download), not a separate explicit "redeem" call.
// See Redemption's own doc comment for why one row serves both as the
// access record and the visibility list.
func (s *Store) Touch(ctx context.Context, shareID, rider string) error {
	rider = strings.ToLower(strings.TrimSpace(rider))
	if rider == "" {
		return errors.New("routeshare: no rider to record")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO route_share_redemptions (share_id, rider, redeemed_at)
        VALUES (?, ?, ?)
        ON CONFLICT (share_id, rider) DO UPDATE SET redeemed_at = excluded.redeemed_at`),
		shareID, rider, now)
	if err != nil {
		return fmt.Errorf("routeshare: touch: %w", err)
	}
	return nil
}

// Revoke ends a share immediately — every subsequent Lookup returns
// ErrNotFound from then on. Revoking one that is already revoked, or that
// does not exist, is not an error: the same rule every other Store in this
// codebase applies to a redundant delete.
func (s *Store) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE route_shares SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`), now, id)
	if err != nil {
		return fmt.Errorf("routeshare: revoke: %w", err)
	}
	return nil
}

// newToken is 32 random bytes, URL-safe base64 — the same shape as
// sessions.newToken, for the same reason: opaque, unguessable at any
// practical scale, and plain enough to sit in a URL path segment with no
// further encoding.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("routeshare: generating token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken is what actually goes in route_shares.id — never the token
// itself. See sessions.hashToken's own doc comment: the reasoning (a
// 256-bit crypto/rand value needs no salt, and storing it verbatim would
// make a database leak equivalent to leaking every live link) applies here
// unchanged.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
