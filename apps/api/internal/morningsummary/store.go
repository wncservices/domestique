package morningsummary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// Preference is one rider's opt-in. The address is held only while the rider
// is opted in, and only ever one the identity provider vouched for (see
// api.morningSummaryEmail): it is stored because nothing is signed in at 06:30
// to read it from.
type Preference struct {
	Rider        string
	Enabled      bool
	Email        string
	LastSentDate string
}

// Store holds who has opted in and the last date a summary went out.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

func schema(d dbx.Dialect) string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS morning_summaries (
    rider          TEXT PRIMARY KEY,
    enabled        %s NOT NULL DEFAULT FALSE,
    email          TEXT NOT NULL DEFAULT '',
    last_sent_date TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL
)`, d.Boolean)
}

// UseDB puts the morning_summaries table in an already-open database.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("migrate morning_summaries table: %w", err)
	}
	return &Store{db: db, dialect: d}, nil
}

func normalize(rider string) string { return strings.ToLower(strings.TrimSpace(rider)) }

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Get returns rider's preference; ok is false when they never touched it.
func (s *Store) Get(ctx context.Context, rider string) (Preference, bool, error) {
	var p Preference
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT rider, enabled, email, last_sent_date FROM morning_summaries WHERE rider = ?`), normalize(rider)).
		Scan(&p.Rider, &p.Enabled, &p.Email, &p.LastSentDate)
	if errors.Is(err, sql.ErrNoRows) {
		return Preference{}, false, nil
	}
	if err != nil {
		return Preference{}, false, fmt.Errorf("morningsummary: get: %w", err)
	}
	return p, true, nil
}

// Enable opts rider in, sending to email. last_sent_date is left alone, so
// switching it off and on again within a day does not send twice.
func (s *Store) Enable(ctx context.Context, rider, email string, now time.Time) error {
	rider, email = normalize(rider), strings.TrimSpace(email)
	if rider == "" {
		return errors.New("morningsummary: no rider")
	}
	if email == "" {
		return errors.New("morningsummary: no address to send to")
	}
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO morning_summaries (rider, enabled, email, last_sent_date, updated_at)
        VALUES (?, ?, ?, '', ?)
        ON CONFLICT (rider) DO UPDATE SET enabled = excluded.enabled, email = excluded.email, updated_at = excluded.updated_at`),
		rider, true, email, stamp(now))
	if err != nil {
		return fmt.Errorf("morningsummary: enable: %w", err)
	}
	return nil
}

// Disable opts rider out and forgets the address: there is no reason to keep
// an email for someone who asked not to be written to. Disabling a rider who
// never opted in is not an error.
func (s *Store) Disable(ctx context.Context, rider string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE morning_summaries SET enabled = ?, email = '', updated_at = ? WHERE rider = ?`),
		false, stamp(now), normalize(rider))
	if err != nil {
		return fmt.Errorf("morningsummary: disable: %w", err)
	}
	return nil
}

// SetEmail changes the address of an existing row and creates none.
func (s *Store) SetEmail(ctx context.Context, rider, email string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE morning_summaries SET email = ?, updated_at = ? WHERE rider = ?`),
		strings.TrimSpace(email), stamp(now), normalize(rider))
	if err != nil {
		return fmt.Errorf("morningsummary: set email: %w", err)
	}
	return nil
}

// Claim takes rider's summary for date, and reports whether this caller got it.
// It is one compare-and-set statement, so two replicas, or a pass and its
// restart, cannot both win; the claim is made before the send, so a crash or a
// failed send never leads to a second email. Only an enabled rider's day can
// be claimed.
func (s *Store) Claim(ctx context.Context, rider, date string) (bool, error) {
	// #nosec G701 -- constant statement, every value bound.
	res, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE morning_summaries SET last_sent_date = ? WHERE rider = ? AND enabled = ? AND last_sent_date <> ?`),
		date, normalize(rider), true, date)
	if err != nil {
		return false, fmt.Errorf("morningsummary: claim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("morningsummary: claim: %w", err)
	}
	return n == 1, nil
}

// ListEnabled is every opted-in rider, in name order.
func (s *Store) ListEnabled(ctx context.Context) ([]Preference, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(
		`SELECT rider, enabled, email, last_sent_date FROM morning_summaries WHERE enabled = ? ORDER BY rider`), true)
	if err != nil {
		return nil, fmt.Errorf("morningsummary: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Preference
	for rows.Next() {
		var p Preference
		if err := rows.Scan(&p.Rider, &p.Enabled, &p.Email, &p.LastSentDate); err != nil {
			return nil, fmt.Errorf("morningsummary: list: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RidersByEmail is the riders opted in to this address, compared without
// case. It lets an admin action keyed by an email (blocking) find a rider whose
// name the admin did not give.
func (s *Store) RidersByEmail(ctx context.Context, email string) ([]string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(
		`SELECT rider FROM morning_summaries WHERE enabled = ? AND LOWER(email) = ? ORDER BY rider`), true, email)
	if err != nil {
		return nil, fmt.Errorf("morningsummary: riders by email: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, fmt.Errorf("morningsummary: riders by email: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRider removes a rider's row and with it their address, for rider
// deletion: no mail goes to someone who has left.
func (s *Store) DeleteRider(ctx context.Context, rider string) error {
	rider = normalize(rider)
	if rider == "" {
		return errors.New("morningsummary: no rider to delete")
	}
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM morning_summaries WHERE rider = ?`), rider); err != nil {
		return fmt.Errorf("morningsummary: delete rider: %w", err)
	}
	return nil
}
