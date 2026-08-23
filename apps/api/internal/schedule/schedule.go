// Package schedule holds crew rides — a route a crew member has picked for
// a specific day, for the rest of the crew to see and sync to their own
// devices ahead of time.
//
// A ride is deliberately thin: it names a crew, a route slug, and a date,
// nothing more. It does not carry its own copy of the route (the route
// itself, and who may see it, is internal/source and internal/crew's job —
// a ride just points at one by slug) and it does not place anything on a
// rider's Garmin or Wahoo device's own calendar — neither provider
// integration this app has today (internal/garmin, internal/wahoo) has a
// scheduling concept to place it on. What a ride actually does is make
// "sync now" concrete: the crew's own detail view lists rides, and syncing
// one pushes that route to every approved member's linked accounts right
// away, rather than waiting on the next automatic push. A separate package
// from internal/crew (not folded in) for the same reason internal/crew
// itself is not folded into internal/config — a distinct relationship
// between a crew and a route deserves its own table and its own store,
// even though every write here needs a crew id to exist first.
package schedule

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// ErrNotFound is returned for a ride id nothing matches.
var ErrNotFound = errors.New("no such scheduled ride")

// dateLayout is the only shape a ride's date may take, and the only thing
// this package ever formats or parses one as. A ride names a day, not a
// moment: nothing about scheduling a ride, sharing it with the crew, or
// syncing it to a device needs a time-of-day, and accepting one would just
// be a value nothing ever reads.
const dateLayout = "2006-01-02"

// dateOnly reports whether s is a real calendar date in dateLayout — not
// just shaped like one. time.Parse with this exact layout already rejects
// "2026-13-40" (no such month) the same way it rejects "2026/09/05" (wrong
// separators) or "20260905" (wrong length); Go's reference-layout parsing
// validates both the shape and the value in one pass, so there is no
// separate range check to get wrong.
func dateOnly(s string) bool {
	_, err := time.Parse(dateLayout, s)
	return err == nil
}

// Ride is one crew's plan to ride a specific route on a specific day.
type Ride struct {
	ID        string
	CrewID    string
	Slug      string
	Date      string // YYYY-MM-DD
	CreatedBy string
	CreatedAt string
}

// Store holds crew rides.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
}

// schema takes the dialect for the same reason crew.schema and
// accounts.schema already do — see crew.schema's own doc comment. This
// table has no boolean column of its own, but the pattern of every schema
// function in this codebase taking its dialect (rather than only the ones
// that currently need it) is what accounts.schema's own comment argues for
// keeping consistent, so a future column here does not need a signature
// change to add one.
func schema(_ dbx.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS crew_rides (
    id         TEXT PRIMARY KEY,
    crew_id    TEXT NOT NULL,
    route_slug TEXT NOT NULL,
    date       TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_crew_rides_crew ON crew_rides (crew_id, date);`
}

// UseDB puts the crew_rides table in an already-open database — the same
// one holding crews, accounts, routes and sync state.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, dialect: d}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("migrate crew_rides table: %w", err)
	}
	return store, nil
}

// newID is 16 random bytes, hex-encoded — same shape and same reasoning as
// basemap.newID: unique, not a secret, since every ride endpoint sits
// behind crews:manage and this package's own crew-membership checks.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate ride id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Create records a new ride. It does not check the caller may schedule for
// this crew, that the route exists, or that the route is actually shared
// with the crew — those all need state (crew membership, the route
// library) this package has no access to, so the API layer (rides.go)
// checks them before calling this.
func (s *Store) Create(ctx context.Context, crewID, slug, date, createdBy string) (Ride, error) {
	crewID = strings.TrimSpace(crewID)
	slug = strings.TrimSpace(slug)
	date = strings.TrimSpace(date)
	createdBy = strings.ToLower(strings.TrimSpace(createdBy))
	switch {
	case crewID == "":
		return Ride{}, errors.New("schedule: no crew — for which crew is this ride?")
	case slug == "":
		return Ride{}, errors.New("schedule: no route — which route is being ridden?")
	case !dateOnly(date):
		return Ride{}, fmt.Errorf("schedule: %q is not a date in YYYY-MM-DD form", date)
	case createdBy == "":
		return Ride{}, errors.New("schedule: no rider — who is scheduling this?")
	}

	id, err := newID()
	if err != nil {
		return Ride{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO crew_rides (id, crew_id, route_slug, date, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?)`),
		id, crewID, slug, date, createdBy, now); err != nil {
		return Ride{}, fmt.Errorf("schedule ride: %w", err)
	}

	return Ride{ID: id, CrewID: crewID, Slug: slug, Date: date, CreatedBy: createdBy, CreatedAt: now}, nil
}

// ListForCrew returns every ride a crew has scheduled, soonest first.
func (s *Store) ListForCrew(ctx context.Context, crewID string) ([]Ride, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(`
        SELECT id, crew_id, route_slug, date, created_by, created_at
        FROM crew_rides WHERE crew_id = ? ORDER BY date, created_at`), crewID)
	if err != nil {
		return nil, fmt.Errorf("read crew rides: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Ride
	for rows.Next() {
		var ride Ride
		if err := rows.Scan(&ride.ID, &ride.CrewID, &ride.Slug, &ride.Date, &ride.CreatedBy, &ride.CreatedAt); err != nil {
			return nil, fmt.Errorf("read crew rides: %w", err)
		}
		out = append(out, ride)
	}
	return out, rows.Err()
}

// Get returns one ride.
func (s *Store) Get(ctx context.Context, id string) (Ride, error) {
	var ride Ride
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT id, crew_id, route_slug, date, created_by, created_at
        FROM crew_rides WHERE id = ?`), id).
		Scan(&ride.ID, &ride.CrewID, &ride.Slug, &ride.Date, &ride.CreatedBy, &ride.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Ride{}, ErrNotFound
	}
	return ride, err
}

// Delete removes a ride. It never touches the route or the crew's targets —
// a ride is only ever a plan to ride something already shared, never the
// sharing itself, so deleting one changes nothing about who a route
// reaches.
func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM crew_rides WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteForCrew removes every ride a crew has scheduled — called when the
// crew itself is deleted. Crew ids are reused: crew.Store.uniqueID only
// checks against currently-existing crews, so a deleted "Sunday Club" frees
// "crew:sunday-club" for a brand new, unrelated crew of the same name to
// claim. Without this, that new crew would inherit the old one's scheduled
// rides the moment anyone lists them — a stale-data leak across what the
// app treats as two different crews that only happen to share a
// since-reused id. Zero rides to delete is the ordinary case (most crews
// never schedule one), not an error.
func (s *Store) DeleteForCrew(ctx context.Context, crewID string) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`DELETE FROM crew_rides WHERE crew_id = ?`), crewID)
	return err
}
