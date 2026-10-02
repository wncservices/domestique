// Package garminmfa holds a Garmin two-factor challenge between the request
// that hit it and the request that answers it.
//
// A code the rider has to fetch from an app or an inbox cannot arrive with the
// password: there is a real, often multi-minute wait between "sign in" and
// "type this code", and the two requests can land on different replicas. So
// what Garmin's sign-in needs to resume — cookies, the challenge page's CSRF
// token, the email and the MFA method (see garmin.MFAChallenge), never the
// password — is sealed in a table, keyed to the rider that started it,
// short-lived and single-use.
//
// Shaped like internal/sessions and internal/providerlink on purpose: same
// UseDB, same nil-safe CanStore, same secrets.Box, same self-pruning of
// expired rows on write. It is not one of those two because a session seals
// "who is signed in to Domestique" and a provider link seals a finished
// connection; conflating either with mid-flight Garmin credential state would
// make both harder to reason about.
package garminmfa

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

const (
	// DefaultTTL is how long a challenge lives. Garmin does not publish one:
	// long enough to type a six-digit code, short enough that a stale
	// challenge is not sitting around as an unused credential-adjacent blob.
	DefaultTTL = 5 * time.Minute
	// MaxAttempts wrong codes delete the challenge. Not a meaningful obstacle
	// to a script, deliberately more than a human ever needs.
	MaxAttempts = 5
	// MaxLivePerRider bounds how many challenges one rider can hold open:
	// each fresh sign-in gets a fresh MaxAttempts, so the per-challenge cap
	// alone does not bound the total.
	MaxLivePerRider = 3
	// MaxChallengeBytes caps the marshalled state before it is sealed.
	// Cookies come from a third party; an unbounded blob per attempt would be
	// a cheap way to fill the table.
	MaxChallengeBytes = 64 << 10
)

var (
	// ErrNotFound covers an unknown token, another rider's token and a row
	// already consumed or exhausted — deliberately indistinguishable, so a
	// stranger cannot learn a challenge exists.
	ErrNotFound = errors.New("garminmfa: no such challenge")
	// ErrExpired is only ever returned to the rider the challenge belongs to.
	ErrExpired = errors.New("garminmfa: challenge expired")
	// ErrAttemptsExhausted means the last allowed wrong code was just
	// recorded and the challenge is gone.
	ErrAttemptsExhausted = errors.New("garminmfa: too many wrong codes")
	// ErrTooLarge means the challenge exceeded MaxChallengeBytes.
	ErrTooLarge = errors.New("garminmfa: challenge too large")
)

// Store holds the challenges.
type Store struct {
	// Now is the clock, replaceable so TTL assertions do not depend on the
	// wall clock — the same field garmin.Client has. Nil means time.Now.
	Now func() time.Time

	db      *sql.DB
	dialect dbx.Dialect
	box     *secrets.Box
}

// schema returns the DDL as a constant per engine; see providerlink.schema
// for why this is not one Sprintf.
//
// token holds sha256(the opaque id handed to the client), never the id.
func schema(d dbx.Dialect) string {
	const sqlite = `
CREATE TABLE IF NOT EXISTS garmin_mfa_challenges (
    token      TEXT PRIMARY KEY,
    rider      TEXT NOT NULL,
    state      BLOB NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);`
	const postgres = `
CREATE TABLE IF NOT EXISTS garmin_mfa_challenges (
    token      TEXT PRIMARY KEY,
    rider      TEXT NOT NULL,
    state      BYTEA NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);`

	if d.Name == dbx.Postgres.Name {
		return postgres
	}
	return sqlite
}

// UseDB puts the table in an already-open database. The box may be nil, in
// which case no challenge can be created.
func UseDB(db *sql.DB, dsn string, box *secrets.Box) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, dialect: d, box: box}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("create garmin_mfa_challenges table: %w", err)
	}
	return store, nil
}

// CanStore reports whether a challenge can be created at all. Nil-safe on
// purpose, like providerlink.Store.CanStore: a Server without one is a valid
// configuration and handlers call this without a nil check.
func (s *Store) CanStore() bool { return s != nil && s.box != nil }

// tsFormat is fixed-width UTC so a string comparison is a time comparison,
// including for the "oldest first" ordering — RFC3339's one-second resolution
// would tie.
const tsFormat = "2006-01-02T15:04:05.000000000Z"

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func normalise(rider string) string { return strings.ToLower(strings.TrimSpace(rider)) }

// Create seals a challenge for rider and returns the opaque id the client
// presents at step two. The rider must come from the session, never a body.
func (s *Store) Create(ctx context.Context, rider string, ch garmin.MFAChallenge, ttl time.Duration) (string, error) {
	if !s.CanStore() {
		return "", secrets.ErrNoKey
	}
	rider = normalise(rider)
	if rider == "" {
		return "", errors.New("garminmfa: refusing to create a challenge for nobody")
	}

	raw, err := json.Marshal(ch)
	if err != nil {
		return "", fmt.Errorf("garminmfa: encoding challenge: %w", err)
	}
	// Before sealing, and an error rather than a truncation.
	if len(raw) > MaxChallengeBytes {
		return "", fmt.Errorf("%w: %d bytes", ErrTooLarge, len(raw))
	}
	sealed, err := s.box.Seal(string(raw))
	if err != nil {
		return "", err
	}
	tok, err := newToken()
	if err != nil {
		return "", err
	}

	now := s.now()

	// #nosec G701 -- constant statements, bound parameters.
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM garmin_mfa_challenges WHERE expires_at < ?`), now.Format(tsFormat)); err != nil {
		return "", fmt.Errorf("garminmfa: pruning expired rows: %w", err)
	}
	if err := s.evictOldest(ctx, rider); err != nil {
		return "", err
	}

	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`INSERT INTO garmin_mfa_challenges (token, rider, state, attempts, created_at, expires_at)
		 VALUES (?, ?, ?, 0, ?, ?)`),
		hashToken(tok), rider, sealed, now.Format(tsFormat), now.Add(ttl).Format(tsFormat)); err != nil {
		return "", fmt.Errorf("garminmfa: creating challenge: %w", err)
	}
	return tok, nil
}

// evictOldest makes room for one more live challenge for rider.
func (s *Store) evictOldest(ctx context.Context, rider string) error {
	// #nosec G701 -- constant statement, bound parameter.
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(
		`SELECT token FROM garmin_mfa_challenges WHERE rider = ? ORDER BY created_at ASC, token ASC`), rider)
	if err != nil {
		return fmt.Errorf("garminmfa: counting live challenges: %w", err)
	}
	var live []string
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err != nil {
			_ = rows.Close()
			return err
		}
		live = append(live, tok)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	for len(live) >= MaxLivePerRider {
		// #nosec G701 -- constant statement, bound parameter.
		if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(
			`DELETE FROM garmin_mfa_challenges WHERE token = ?`), live[0]); err != nil {
			return fmt.Errorf("garminmfa: evicting the oldest challenge: %w", err)
		}
		live = live[1:]
	}
	return nil
}

// Resolve returns the challenge for rider's token. It does not delete on a
// read, so the caller can decide what an attempt costs; only an expired row is
// removed here, since it can never be used again.
//
// ErrNotFound for anything that is not this rider's live row, ErrExpired only
// to the owner.
func (s *Store) Resolve(ctx context.Context, rider, token string) (garmin.MFAChallenge, error) {
	if !s.CanStore() || token == "" {
		return garmin.MFAChallenge{}, ErrNotFound
	}

	var owner, expires string
	var sealed []byte
	// #nosec G701 -- constant statement, bound parameter.
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT rider, state, expires_at FROM garmin_mfa_challenges WHERE token = ?`), hashToken(token)).
		Scan(&owner, &sealed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return garmin.MFAChallenge{}, ErrNotFound
	}
	if err != nil {
		return garmin.MFAChallenge{}, fmt.Errorf("garminmfa: reading challenge: %w", err)
	}
	if owner != normalise(rider) {
		return garmin.MFAChallenge{}, ErrNotFound
	}
	if s.now().Format(tsFormat) > expires {
		_ = s.Consume(ctx, rider, token)
		return garmin.MFAChallenge{}, ErrExpired
	}

	raw, err := s.box.Open(sealed)
	if err != nil {
		return garmin.MFAChallenge{}, ErrNotFound
	}
	var ch garmin.MFAChallenge
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		return garmin.MFAChallenge{}, ErrNotFound
	}
	return ch, nil
}

// Reserve takes one attempt for rider's challenge, atomically, and returns
// how many have now been used. It is called before Garmin is asked to check a
// code, so N concurrent submissions cannot each get a look: the UPDATE only
// matches while attempts is under the cap, and the database serialises it.
//
// A refused reserve is ErrAttemptsExhausted (the row exists and is spent) or
// ErrNotFound (no such live row for this rider). It does not delete: the
// attempts in flight may yet be refunded, and finishing a spent challenge is
// Consume's job. It does not check expiry either — Resolve first, which does.
func (s *Store) Reserve(ctx context.Context, rider, token string) (int, error) {
	if !s.CanStore() || token == "" {
		return 0, ErrNotFound
	}
	rider = normalise(rider)

	var used int
	// #nosec G701 -- constant statement, bound parameters.
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`UPDATE garmin_mfa_challenges SET attempts = attempts + 1
		 WHERE token = ? AND rider = ? AND attempts < ? RETURNING attempts`),
		hashToken(token), rider, MaxAttempts).Scan(&used)
	if err == nil {
		return used, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("garminmfa: reserving an attempt: %w", err)
	}

	// Nothing matched: either the row is spent or it is not this rider's.
	var one int
	err = s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT 1 FROM garmin_mfa_challenges WHERE token = ? AND rider = ?`),
		hashToken(token), rider).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("garminmfa: reading attempts: %w", err)
	}
	return 0, ErrAttemptsExhausted
}

// Refund gives back an attempt Reserve took, for the outcomes that say nothing
// about the code: Garmin blocked or rate-limited us, or answered a page we do
// not recognise. Never below zero, and only for the owning rider. Callers must
// have Resolved the challenge as this rider first.
func (s *Store) Refund(ctx context.Context, rider, token string) error {
	if !s.CanStore() || token == "" {
		return nil
	}
	// #nosec G701 -- constant statement, bound parameters.
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE garmin_mfa_challenges SET attempts = attempts - 1
		 WHERE token = ? AND rider = ? AND attempts > 0`), hashToken(token), normalise(rider))
	return err
}

// Update re-seals a challenge's state in place — after a rejected code Garmin
// re-renders the page with a fresh CSRF token and the cookies move on, and the
// next attempt on the same challenge needs both. Attempts and expiry are not
// touched. The size cap applies as it does on Create; the rider must own the
// row.
func (s *Store) Update(ctx context.Context, rider, token string, ch garmin.MFAChallenge) error {
	if !s.CanStore() || token == "" {
		return ErrNotFound
	}
	raw, err := json.Marshal(ch)
	if err != nil {
		return fmt.Errorf("garminmfa: encoding challenge: %w", err)
	}
	if len(raw) > MaxChallengeBytes {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, len(raw))
	}
	sealed, err := s.box.Seal(string(raw))
	if err != nil {
		return err
	}
	// #nosec G701 -- constant statement, bound parameters.
	res, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE garmin_mfa_challenges SET state = ? WHERE token = ? AND rider = ?`),
		sealed, hashToken(token), normalise(rider))
	if err != nil {
		return fmt.Errorf("garminmfa: updating challenge: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRider drops every pending challenge a rider has, for when the rider
// is deleted (see the API package's purgeRiderData). Unlike Consume it needs
// no token, and unlike every other method it works without an encryption key:
// the rows are deleted, never read.
func (s *Store) DeleteRider(ctx context.Context, rider string) error {
	if s == nil {
		return nil
	}
	// #nosec G701 -- constant statement, bound parameter.
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM garmin_mfa_challenges WHERE rider = ?`), normalise(rider)); err != nil {
		return fmt.Errorf("garminmfa: remove rider's challenges: %w", err)
	}
	return nil
}

// Consume deletes a challenge: on success, or once it is spent. Deleting one
// that is not there is not an error. Scoped to the rider so a caller that has
// not Resolved as the owner cannot delete someone else's challenge.
func (s *Store) Consume(ctx context.Context, rider, token string) error {
	if !s.CanStore() || token == "" {
		return nil
	}
	// #nosec G701 -- constant statement, bound parameters.
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM garmin_mfa_challenges WHERE token = ? AND rider = ?`), hashToken(token), normalise(rider))
	return err
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("garminmfa: generating token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken is what goes in the table, never the id itself: a database read
// alone must not be replayable. 32 random bytes need no salt.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
