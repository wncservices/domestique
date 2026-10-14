// Package identities holds which issuer account is which rider.
//
// Under auth.mode oidc a person signs in as an issuer account, identified by
// the issuer URL and the ID token's sub claim. The sub is assigned by the
// issuer and cannot be edited by its holder. A name claim can: a Google
// display name is whatever its owner types, so a rider derived from it is a
// rider anyone can impersonate (audit finding H1). This table is the fix: a
// sign-in reaches a rider through a row here, and the name claims are only a
// suggestion for an account that has no row yet.
//
// Several rows may name one rider (the same person through Google and through
// a password), which is why rider is indexed and not unique. What is unique is
// (issuer, sub): one account is one rider.
//
// The verified email is kept as a keyed MAC (secrets.Box.MAC), never as the
// address. It exists for one question: does this new account belong to the
// same person as a rider already bound? The MAC answers that without this
// table being a list of addresses.
package identities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

// EmailPurpose separates the email MACs from every other MAC under the same
// key, so this column can never be joined to another table's stand-ins.
const EmailPurpose = "identity-email"

// resetSessionsMarker names the one-time migration that ends every session.
const resetSessionsMarker = "end-all-sessions"

// pgAdvisoryKey serialises claims across replicas on PostgreSQL. SQLite is one
// process with one writer, which claimMu already covers.
const pgAdvisoryLock = `SELECT pg_advisory_xact_lock(727274)`

// maxSuffix bounds the search for a free "name-N". Reaching it means something
// is wrong, not that a thousand people are called wilant.
const maxSuffix = 1000

// Binding is one issuer account and the rider it signs in as.
type Binding struct {
	Issuer string
	Sub    string
	Rider  string
	// HasEmail is true when a verified address was recorded. The address
	// itself is not stored.
	HasEmail   bool
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// Claim is the outcome of ClaimFree.
type Claim struct {
	Rider string
	// Existing is true when the sub was already bound and nothing was written.
	Existing bool
	// Suffixed is true when the wanted name was taken and a "-N" was used.
	Suffixed bool
}

// TakenColumns maps a table to the columns that hold a rider's name in it. The
// caller builds it from the registry of rider-keyed tables, so a new table is
// covered by registering it.
type TakenColumns map[string][]string

// Store holds the bindings.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
	box     *secrets.Box
	// Clock replaces time.Now, for tests.
	Clock func() time.Time

	claimMu sync.Mutex
}

func schemas(_ dbx.Dialect) []string {
	return []string{`
CREATE TABLE IF NOT EXISTS rider_identities (
    issuer       TEXT NOT NULL,
    sub          TEXT NOT NULL,
    rider        TEXT NOT NULL,
    email_mac    TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    PRIMARY KEY (issuer, sub)
)`,
		`CREATE INDEX IF NOT EXISTS rider_identities_rider ON rider_identities (rider)`,
		`CREATE INDEX IF NOT EXISTS rider_identities_email ON rider_identities (email_mac)`,
		`
CREATE TABLE IF NOT EXISTS identity_migrations (
    name       TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
)`,
	}
}

// UseDB puts the tables in an already-open database and runs the one-time
// session reset.
//
// The box may be nil: no email can then be matched, which only means a second
// sign-in method does not join a rider by itself. Under mode oidc the server
// refuses to start a sign-in without a key at all, so a nil box never serves
// a login.
func UseDB(db *sql.DB, dsn string, box *secrets.Box) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	for _, stmt := range append(schemas(d), flagSchemas()...) {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("identities: migrating: %w", err)
		}
	}
	s := &Store{db: db, dialect: d, box: box}
	if err := s.endSessionsOnce(); err != nil {
		return nil, err
	}
	return s, nil
}

// Exists reports whether the bindings table has been created in this database.
func Exists(db *sql.DB, dsn string) (bool, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return false, err
	}
	return tableExists(db, d, "rider_identities")
}

// Attach opens the store on a database that already has the table, and writes
// nothing: no table, no migration, no ending of sessions. It is for a dry run
// that must look without touching, and fails if the table is not there.
func Attach(db *sql.DB, dsn string, box *secrets.Box) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	have, err := tableExists(db, d, "rider_identities")
	if err != nil {
		return nil, err
	}
	if !have {
		return nil, errors.New("identities: this database has no rider_identities table yet")
	}
	return &Store{db: db, dialect: d, box: box}, nil
}

// endSessionsOnce deletes every session the first time it runs against a
// database, and never again.
//
// Sessions made before this table existed hold a rider that was derived from a
// name claim. Keeping them would keep every such rider signed in as whatever
// that claim said, including an impersonator, until the session expired a
// month later. Deleting them makes everyone sign in again through the binding.
//
// The marker and the delete are one transaction so a crash cannot leave the
// marker set with the sessions still there, and the marker insert is what two
// replicas starting together race on: only one inserts a row.
func (s *Store) endSessionsOnce() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("identities: ending old sessions: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(s.dialect.Rebind(
		`INSERT INTO identity_migrations (name, applied_at) VALUES (?, ?) ON CONFLICT (name) DO NOTHING`),
		resetSessionsMarker, s.now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("identities: recording the session reset: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err // already done on an earlier start
	}
	have, err := tableExists(tx, s.dialect, "sessions")
	if err != nil {
		return err
	}
	if have {
		if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
			return fmt.Errorf("identities: ending old sessions: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// CanMatchEmail reports whether a verified email can be turned into a MAC.
func (s *Store) CanMatchEmail() bool { return s != nil && s.box != nil }

// EmailMAC is the stand-in stored for an email: a keyed hash of the
// lower-cased, trimmed address. Empty for an empty address or without a key.
func (s *Store) EmailMAC(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || s == nil {
		return ""
	}
	return s.box.MAC(EmailPurpose, email)
}

func normalizeRider(rider string) string { return strings.ToLower(strings.TrimSpace(rider)) }

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

const bindingColumns = `issuer, sub, rider, email_mac, created_at, last_seen_at`

type scanner interface{ Scan(dest ...any) error }

func scanBinding(r scanner) (Binding, error) {
	var b Binding
	var mac, created, seen string
	if err := r.Scan(&b.Issuer, &b.Sub, &b.Rider, &mac, &created, &seen); err != nil {
		return Binding{}, err
	}
	b.HasEmail = mac != ""
	b.CreatedAt, _ = time.Parse(time.RFC3339, created)
	b.LastSeenAt, _ = time.Parse(time.RFC3339, seen)
	return b, nil
}

func (s *Store) get(ctx context.Context, q querier, issuer, sub string) (Binding, bool, error) {
	b, err := scanBinding(q.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT `+bindingColumns+` FROM rider_identities WHERE issuer = ? AND sub = ?`), issuer, strings.TrimSpace(sub)))
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, false, nil
	}
	if err != nil {
		return Binding{}, false, fmt.Errorf("identities: reading a binding: %w", err)
	}
	return b, true, nil
}

// Get returns the binding of an issuer account, if it has one.
func (s *Store) Get(ctx context.Context, issuer, sub string) (Binding, bool, error) {
	return s.get(ctx, s.db, issuer, sub)
}

// Seen records a sign-in: last_seen_at, and the verified email the account
// asserts now (empty when it asserts none). Replacing rather than keeping the
// old value is the point: an address the account no longer holds must stop
// matching, or whoever later receives that address could join the rider.
func (s *Store) Seen(ctx context.Context, issuer, sub, emailMAC string) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE rider_identities SET last_seen_at = ?, email_mac = ? WHERE issuer = ? AND sub = ?`),
		s.stamp(), emailMAC, issuer, strings.TrimSpace(sub))
	if err != nil {
		return fmt.Errorf("identities: recording a sign-in: %w", err)
	}
	return nil
}

// RidersByEmail is the distinct riders that have an account bound with this
// email MAC under this issuer, sorted. Empty for an empty MAC, which matches nothing.
func (s *Store) RidersByEmail(ctx context.Context, issuer, emailMAC string) ([]string, error) {
	if emailMAC == "" {
		return nil, nil
	}
	return s.riders(ctx, `SELECT DISTINCT rider FROM rider_identities WHERE issuer = ? AND email_mac = ?`, issuer, emailMAC)
}

// RidersOfSub is every rider a sub is bound to under this issuer.
func (s *Store) RidersOfSub(ctx context.Context, issuer, sub string) ([]string, error) {
	return s.riders(ctx, `SELECT DISTINCT rider FROM rider_identities WHERE issuer = ? AND sub = ?`, issuer, strings.TrimSpace(sub))
}

func (s *Store) riders(ctx context.Context, query string, args ...any) ([]string, error) {
	// #nosec G701 -- constant statements from this file, bound parameter.
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("identities: listing riders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// Bind adds a binding. It reports false, and changes nothing, when the account
// is already bound: a sign-in never moves an existing binding, only an admin
// does (Move).
func (s *Store) Bind(ctx context.Context, issuer, sub, rider, emailMAC string) (bool, error) {
	return s.bind(ctx, s.db, issuer, sub, rider, emailMAC)
}

func (s *Store) bind(ctx context.Context, q querier, issuer, sub, rider, emailMAC string) (bool, error) {
	rider, sub = normalizeRider(rider), strings.TrimSpace(sub)
	if issuer == "" || sub == "" || rider == "" {
		return false, errors.New("identities: a binding needs an issuer, a sub and a rider")
	}
	now := s.stamp()
	res, err := q.ExecContext(ctx, s.dialect.Rebind(
		`INSERT INTO rider_identities (issuer, sub, rider, email_mac, created_at, last_seen_at)
         VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (issuer, sub) DO NOTHING`),
		issuer, sub, rider, emailMAC, now, now)
	if err != nil {
		return false, fmt.Errorf("identities: binding: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ErrNotBound means there is no binding to change.
var ErrNotBound = errors.New("identities: that account has no binding")

// Move points an existing binding at another rider. Admin only; the caller
// ends the account's sessions, which hold the old rider.
func (s *Store) Move(ctx context.Context, issuer, sub, rider string) error {
	rider = normalizeRider(rider)
	if rider == "" {
		return errors.New("identities: no rider")
	}
	res, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE rider_identities SET rider = ? WHERE issuer = ? AND sub = ?`), rider, issuer, strings.TrimSpace(sub))
	if err != nil {
		return fmt.Errorf("identities: moving a binding: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotBound
	}
	return nil
}

// Unbind removes a binding, reporting whether there was one.
func (s *Store) Unbind(ctx context.Context, issuer, sub string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM rider_identities WHERE issuer = ? AND sub = ?`), issuer, strings.TrimSpace(sub))
	if err != nil {
		return false, fmt.Errorf("identities: unbinding: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// List returns every binding, ordered by rider then creation.
func (s *Store) List(ctx context.Context) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+bindingColumns+` FROM rider_identities ORDER BY rider, created_at, sub`)
	if err != nil {
		return nil, fmt.Errorf("identities: listing bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Binding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// RiderBound reports whether any account is bound to rider.
func (s *Store) RiderBound(ctx context.Context, rider string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT COUNT(*) FROM rider_identities WHERE rider = ?`), normalizeRider(rider)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("identities: counting bindings: %w", err)
	}
	return n > 0, nil
}

// DeleteRider removes every binding of a rider, for a purge. The accounts at
// the issuer are not touched.
func (s *Store) DeleteRider(ctx context.Context, rider string) (int, error) {
	res, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM rider_identities WHERE rider = ?`), normalizeRider(rider))
	if err != nil {
		return 0, fmt.Errorf("identities: removing a rider's bindings: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	// A flag names the rider an account was held back from. It goes with the
	// rider, like the bindings do.
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM bootstrap_flags WHERE rider = ?`), normalizeRider(rider)); err != nil {
		return int(n), fmt.Errorf("identities: removing a rider's flags: %w", err)
	}
	return int(n), nil
}

// ClaimFree binds a sub that has no binding yet to the first free rider among
// candidate, candidate-2, candidate-3 and so on, and reports which.
//
// "Free" is checked against every table in taken, which is the registry of
// everything a rider can own, not just the bindings: a rider who has only
// training data, or only a crew membership, is somebody, and a new sign-in
// must not land on them because it carries the same name.
//
// The check and the insert are one transaction behind a lock, so two first
// sign-ins for the same name cannot both find it free. A sub that is already
// bound (two tabs signing in at once) gets its existing binding back.
func (s *Store) ClaimFree(ctx context.Context, issuer, sub, emailMAC, candidate string, taken TakenColumns) (Claim, error) {
	candidate = normalizeRider(candidate)
	if candidate == "" {
		return Claim{}, errors.New("identities: no candidate rider")
	}

	s.claimMu.Lock()
	defer s.claimMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Claim{}, fmt.Errorf("identities: claiming a rider: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if s.dialect.Name == dbx.Postgres.Name {
		if _, err := tx.ExecContext(ctx, pgAdvisoryLock); err != nil {
			return Claim{}, fmt.Errorf("identities: locking: %w", err)
		}
	}

	if b, ok, err := s.get(ctx, tx, issuer, sub); err != nil {
		return Claim{}, err
	} else if ok {
		return Claim{Rider: b.Rider, Existing: true}, nil
	}

	layout, err := s.liveTaken(ctx, tx, taken)
	if err != nil {
		return Claim{}, err
	}
	for n := 1; n <= maxSuffix; n++ {
		rider := candidate
		if n > 1 {
			rider = candidate + "-" + strconv.Itoa(n)
		}
		used, err := s.isTaken(ctx, tx, layout, rider)
		if err != nil {
			return Claim{}, err
		}
		if used {
			continue
		}
		if _, err := s.bind(ctx, tx, issuer, sub, rider, emailMAC); err != nil {
			return Claim{}, err
		}
		if err := tx.Commit(); err != nil {
			return Claim{}, fmt.Errorf("identities: claiming a rider: %w", err)
		}
		return Claim{Rider: rider, Suffixed: n > 1}, nil
	}
	return Claim{}, fmt.Errorf("identities: no free rider name near %q", candidate)
}

// RidersWithData is every rider named in any of the given tables, mapped to the
// earliest row time found for them. The time is an approximation, on purpose
// cheap: per table the created_at column if it has one, else updated_at, and a
// table with neither (or a value that is not a timestamp) still counts the rider
// as having data but contributes no time. Zero means "has data, no usable time".
// Riders are lower-cased; a blank name is no rider.
//
// It reads tables, not bindings: pass the registry's columns without
// rider_identities, or every bound rider has "data".
func (s *Store) RidersWithData(ctx context.Context, tables TakenColumns) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	names := make([]string, 0, len(tables))
	for t := range tables {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, table := range names {
		have, err := s.columns(ctx, s.db, table)
		if err != nil {
			return nil, err
		}
		timeCol := ""
		for _, c := range []string{"created_at", "updated_at"} {
			if have[c] {
				timeCol = c
				break
			}
		}
		for _, col := range tables[table] {
			if !have[col] {
				continue
			}
			sel := `LOWER(` + col + `)`
			if timeCol != "" {
				sel += `, MIN(` + timeCol + `)`
			} else {
				sel += `, NULL`
			}
			// Table and column names are constants from the caller's registry.
			// #nosec G701 G202 -- built only from registry constants.
			rows, err := s.db.QueryContext(ctx, `SELECT `+sel+` FROM `+table+` WHERE `+col+` IS NOT NULL AND `+col+` <> '' GROUP BY LOWER(`+col+`)`)
			if err != nil {
				return nil, fmt.Errorf("identities: reading riders of %s: %w", table, err)
			}
			for rows.Next() {
				var rider string
				var ts sql.NullString
				if err := rows.Scan(&rider, &ts); err != nil {
					_ = rows.Close()
					return nil, err
				}
				rider = strings.TrimSpace(rider)
				if rider == "" {
					continue
				}
				prev, seen := out[rider]
				if !seen {
					out[rider] = time.Time{}
					prev = time.Time{}
				}
				if t, err := time.Parse(time.RFC3339Nano, ts.String); ts.Valid && err == nil {
					if prev.IsZero() || t.Before(prev) {
						out[rider] = t
					}
				}
			}
			err = rows.Err()
			_ = rows.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// liveTaken narrows taken to the tables and columns this database has. A
// deployment that does not use a feature never created its table, and on
// PostgreSQL a query against a missing table would abort the transaction.
func (s *Store) liveTaken(ctx context.Context, q querier, taken TakenColumns) (TakenColumns, error) {
	out := TakenColumns{}
	tables := make([]string, 0, len(taken))
	for t := range taken {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, table := range tables {
		have, err := s.columns(ctx, q, table)
		if err != nil {
			return nil, err
		}
		for _, c := range taken[table] {
			if have[c] {
				out[table] = append(out[table], c)
			}
		}
	}
	return out, nil
}

func (s *Store) columns(ctx context.Context, q querier, table string) (map[string]bool, error) {
	query := `SELECT name FROM pragma_table_info(?)`
	if s.dialect.Name == dbx.Postgres.Name {
		query = `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?`
	}
	// #nosec G701 -- constant statements, bound parameter.
	rows, err := q.QueryContext(ctx, s.dialect.Rebind(query), table)
	if err != nil {
		return nil, fmt.Errorf("identities: reading the %s schema: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out[c] = true
	}
	return out, rows.Err()
}

func (s *Store) isTaken(ctx context.Context, q querier, taken TakenColumns, rider string) (bool, error) {
	for table, cols := range taken {
		for _, col := range cols {
			var one int
			// Table and column names are constants from the caller's registry,
			// never input.
			// #nosec G701 G202 -- built only from registry constants. rider is bound.
			err := q.QueryRowContext(ctx, s.dialect.Rebind(
				`SELECT 1 FROM `+table+` WHERE LOWER(`+col+`) = ? LIMIT 1`), rider).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("identities: checking %s: %w", table, err)
			}
			return true, nil
		}
	}
	return false, nil
}

func tableExists(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, d dbx.Dialect, name string) (bool, error) {
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	if d.Name == dbx.Postgres.Name {
		query = `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`
	}
	var n int
	// #nosec G701 -- constant statements, bound parameter.
	if err := q.QueryRow(d.Rebind(query), name).Scan(&n); err != nil {
		return false, fmt.Errorf("identities: looking for table %s: %w", name, err)
	}
	return n > 0, nil
}
