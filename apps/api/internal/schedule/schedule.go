// Package schedule holds crew rides — a route a crew member has picked for
// a specific day (optionally, a specific time of day), for the rest of the
// crew to see and sync to their own devices ahead of time.
//
// A ride is deliberately thin: it names a crew, a route slug, a date and an
// optional time, nothing more. It does not carry its own copy of the route
// (the route itself, and who may see it, is internal/source and
// internal/crew's job — a ride just points at one by slug) and it does not
// place anything on a rider's Garmin or Wahoo device's own calendar —
// neither provider integration this app has today (internal/garmin,
// internal/wahoo) has a scheduling concept to place it on. What a ride
// actually does is make "sync now" concrete: the crew's own detail view
// lists rides, and syncing one pushes that route to every approved
// member's linked accounts right away, rather than waiting on the next
// automatic push. A separate package from internal/crew (not folded in)
// for the same reason internal/crew itself is not folded into
// internal/config — a distinct relationship between a crew and a route
// deserves its own table and its own store, even though every write here
// needs a crew id to exist first.
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
// this package ever formats or parses one as.
const dateLayout = "2006-01-02"

// timeLayout is the only shape a ride's time of day may take — 24-hour
// HH:MM, the same value a plain <input type="time"> already produces
// client-side, so there is no format conversion anywhere on the way in.
// Optional: a ride still only names a day at minimum, the same as before
// this existed — a time narrows that to a moment, it does not require one.
const timeLayout = "15:04"

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

// timeOnly reports whether s is a real time of day in timeLayout, or
// empty — a ride with no time named is exactly as valid as one that
// deliberately clears it.
func timeOnly(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse(timeLayout, s)
	return err == nil
}

// Ride is one crew's plan to ride a specific route on a specific day,
// optionally at a specific time.
type Ride struct {
	ID        string
	CrewID    string
	Slug      string
	Date      string // YYYY-MM-DD
	Time      string // HH:MM, or "" for no specific time
	CreatedBy string
	CreatedAt string
	// SeriesID names the Series that generated this ride, or "" for an
	// ordinary one-off ride — see CreateSeries.
	SeriesID string
}

// Series is a recurring-ride template: a crew, a route, an interval and a
// time of day. It exists so a rider can later cancel "this and every ride
// after it" in one action, and so the recurrence itself survives even if
// every ride it generated is eventually deleted one at a time. The rides
// themselves are ordinary Ride rows — nothing about listing, syncing, or
// deleting a single occurrence needs to know a series exists at all.
type Series struct {
	ID            string
	CrewID        string
	Slug          string
	IntervalWeeks int
	Time          string
	CreatedBy     string
	CreatedAt     string
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
    time       TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    series_id  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_crew_rides_crew ON crew_rides (crew_id, date);
CREATE INDEX IF NOT EXISTS idx_crew_rides_series ON crew_rides (series_id);

CREATE TABLE IF NOT EXISTS ride_series (
    id             TEXT PRIMARY KEY,
    crew_id        TEXT NOT NULL,
    route_slug     TEXT NOT NULL,
    interval_weeks INTEGER NOT NULL,
    time           TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL,
    created_at     TEXT NOT NULL
);`
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
	if err := store.addTimeColumn(); err != nil {
		return nil, fmt.Errorf("migrate crew_rides table: %w", err)
	}
	if err := store.addSeriesIDColumn(); err != nil {
		return nil, fmt.Errorf("migrate crew_rides table: %w", err)
	}
	return store, nil
}

// addTimeColumn adds time to a crew_rides table that predates the column —
// the same idempotent-migration shape crew.Store's own addCanScheduleColumn
// and source.DB's addSportColumn already use: CREATE TABLE IF NOT EXISTS
// above is a no-op against a table that already exists, so a genuinely new
// column needs its own step. Defaulting existing rows to ” is correct,
// not just convenient: every ride scheduled before this column existed was
// scheduled with no time-of-day concept at all, which is exactly what ”
// means going forward too.
func (s *Store) addTimeColumn() error {
	_, err := s.db.Exec(`ALTER TABLE crew_rides ADD COLUMN time TEXT NOT NULL DEFAULT ''`)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
}

// addSeriesIDColumn is addTimeColumn's own twin for series_id — same
// idempotent-ALTER shape, same reasoning: a ride scheduled before
// recurrence existed was never part of a series, which is exactly what the
// ” default means going forward too.
func (s *Store) addSeriesIDColumn() error {
	_, err := s.db.Exec(`ALTER TABLE crew_rides ADD COLUMN series_id TEXT NOT NULL DEFAULT ''`)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
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
func (s *Store) Create(ctx context.Context, crewID, slug, date, timeOfDay, createdBy string) (Ride, error) {
	crewID = strings.TrimSpace(crewID)
	slug = strings.TrimSpace(slug)
	date = strings.TrimSpace(date)
	timeOfDay = strings.TrimSpace(timeOfDay)
	createdBy = strings.ToLower(strings.TrimSpace(createdBy))
	switch {
	case crewID == "":
		return Ride{}, errors.New("schedule: no crew — for which crew is this ride?")
	case slug == "":
		return Ride{}, errors.New("schedule: no route — which route is being ridden?")
	case !dateOnly(date):
		return Ride{}, fmt.Errorf("schedule: %q is not a date in YYYY-MM-DD form", date)
	case !timeOnly(timeOfDay):
		return Ride{}, fmt.Errorf("schedule: %q is not a time in HH:MM form", timeOfDay)
	case createdBy == "":
		return Ride{}, errors.New("schedule: no rider — who is scheduling this?")
	}

	id, err := newID()
	if err != nil {
		return Ride{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO crew_rides (id, crew_id, route_slug, date, time, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`),
		id, crewID, slug, date, timeOfDay, createdBy, now); err != nil {
		return Ride{}, fmt.Errorf("schedule ride: %w", err)
	}

	return Ride{ID: id, CrewID: crewID, Slug: slug, Date: date, Time: timeOfDay, CreatedBy: createdBy, CreatedAt: now}, nil
}

// maxSeriesOccurrences caps how many rides a single CreateSeries call will
// ever generate, regardless of the requested interval and end date. A
// mistyped "until" a decade out must not silently fill the table — nothing
// stops the rider from creating another series later, once the ones near
// the cap have actually passed.
const maxSeriesOccurrences = 52

// seriesIntervals are the only recurrence intervals this package accepts —
// weekly, fortnightly, or every four weeks. A crew's regular ride is the
// case this exists for; anything odder is still just one-off rides
// scheduled by hand.
var seriesIntervals = map[int]bool{1: true, 2: true, 4: true}

// CreateSeries generates one Ride every intervalWeeks starting at
// startDate, up to and including until, capped at maxSeriesOccurrences
// regardless of what that range implies. Every generated ride carries the
// new Series's id in its own SeriesID, so a rider can later cancel "this
// and every ride after it" (DeleteSeries) without the series concept
// having to exist anywhere else — ListForCrew, ListUpcoming, syncing and
// deleting a single occurrence all already work on an ordinary Ride row
// and need no change to handle one that happens to belong to a series.
//
// The series row and every ride it generates commit together in one
// transaction: a caller must never observe a half-created series.
//
// Like Create, this does not check crew membership, route existence, or
// route-sharing — the API layer checks those once, before calling this,
// exactly as it already does for a single ride.
func (s *Store) CreateSeries(ctx context.Context, crewID, slug string, intervalWeeks int, startDate, until, timeOfDay, createdBy string) (Series, []Ride, error) {
	crewID = strings.TrimSpace(crewID)
	slug = strings.TrimSpace(slug)
	startDate = strings.TrimSpace(startDate)
	until = strings.TrimSpace(until)
	timeOfDay = strings.TrimSpace(timeOfDay)
	createdBy = strings.ToLower(strings.TrimSpace(createdBy))
	switch {
	case crewID == "":
		return Series{}, nil, errors.New("schedule: no crew — for which crew is this series?")
	case slug == "":
		return Series{}, nil, errors.New("schedule: no route — which route is being ridden?")
	case !seriesIntervals[intervalWeeks]:
		return Series{}, nil, fmt.Errorf("schedule: %d is not a supported interval (1, 2 or 4 weeks)", intervalWeeks)
	case !dateOnly(startDate):
		return Series{}, nil, fmt.Errorf("schedule: %q is not a date in YYYY-MM-DD form", startDate)
	case !dateOnly(until):
		return Series{}, nil, fmt.Errorf("schedule: %q is not a date in YYYY-MM-DD form", until)
	case !timeOnly(timeOfDay):
		return Series{}, nil, fmt.Errorf("schedule: %q is not a time in HH:MM form", timeOfDay)
	case createdBy == "":
		return Series{}, nil, errors.New("schedule: no rider — who is scheduling this?")
	}

	start, _ := time.Parse(dateLayout, startDate)
	end, _ := time.Parse(dateLayout, until)
	if end.Before(start) {
		return Series{}, nil, errors.New("schedule: the series can't end before it starts")
	}

	var dates []string
	for d := start; !d.After(end) && len(dates) < maxSeriesOccurrences; d = d.AddDate(0, 0, intervalWeeks*7) {
		dates = append(dates, d.Format(dateLayout))
	}
	if len(dates) == 0 {
		return Series{}, nil, errors.New("schedule: that range generates no rides at all")
	}

	seriesID, err := newID()
	if err != nil {
		return Series{}, nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	series := Series{
		ID: seriesID, CrewID: crewID, Slug: slug, IntervalWeeks: intervalWeeks,
		Time: timeOfDay, CreatedBy: createdBy, CreatedAt: now,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Series{}, nil, fmt.Errorf("schedule series: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO ride_series (id, crew_id, route_slug, interval_weeks, time, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`),
		seriesID, crewID, slug, intervalWeeks, timeOfDay, createdBy, now); err != nil {
		return Series{}, nil, fmt.Errorf("schedule series: %w", err)
	}

	rides := make([]Ride, 0, len(dates))
	for _, date := range dates {
		id, err := newID()
		if err != nil {
			return Series{}, nil, err
		}
		if _, err := tx.ExecContext(ctx, s.dialect.Rebind(`
            INSERT INTO crew_rides (id, crew_id, route_slug, date, time, created_by, created_at, series_id)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
			id, crewID, slug, date, timeOfDay, createdBy, now, seriesID); err != nil {
			return Series{}, nil, fmt.Errorf("schedule series: %w", err)
		}
		rides = append(rides, Ride{
			ID: id, CrewID: crewID, Slug: slug, Date: date, Time: timeOfDay,
			CreatedBy: createdBy, CreatedAt: now, SeriesID: seriesID,
		})
	}

	if err := tx.Commit(); err != nil {
		return Series{}, nil, fmt.Errorf("schedule series: %w", err)
	}
	return series, rides, nil
}

// DeleteSeries removes every ride in a series scheduled on or after
// fromDate, leaving earlier ones — rides that have already happened —
// untouched. The same "never touches history" rule Delete already follows
// for a single ride, applied to a whole series at once. Zero rides left to
// delete (every future occurrence was already cancelled one at a time) is
// a valid outcome, not an error.
func (s *Store) DeleteSeries(ctx context.Context, seriesID, fromDate string) (int, error) {
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM crew_rides WHERE series_id = ? AND date >= ?`), seriesID, fromDate)
	if err != nil {
		return 0, fmt.Errorf("delete ride series: %w", err)
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// ListForCrew returns every ride a crew has scheduled, soonest first —
// earliest date, then (within a day) earliest time, ties broken by
// creation order. A ride with no time sorts before a timed one on the same
// day: ” collates before any "HH:MM" string.
func (s *Store) ListForCrew(ctx context.Context, crewID string) ([]Ride, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(`
        SELECT id, crew_id, route_slug, date, time, created_by, created_at, series_id
        FROM crew_rides WHERE crew_id = ? ORDER BY date, time, created_at`), crewID)
	if err != nil {
		return nil, fmt.Errorf("read crew rides: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Ride
	for rows.Next() {
		var ride Ride
		if err := rows.Scan(&ride.ID, &ride.CrewID, &ride.Slug, &ride.Date, &ride.Time, &ride.CreatedBy, &ride.CreatedAt, &ride.SeriesID); err != nil {
			return nil, fmt.Errorf("read crew rides: %w", err)
		}
		out = append(out, ride)
	}
	return out, rows.Err()
}

// ListUpcoming returns every ride, across every crew, scheduled on or after
// from (YYYY-MM-DD, inclusive — a ride scheduled for today still counts),
// soonest first. Unfiltered by crew on purpose: this package has no way to
// know which crews a caller may see, the same reason Create above leaves
// membership checks to the API layer — handleUpcomingRides narrows this
// down to the caller's own crews after the fact. A homelab-scale
// deployment's total ride count is small enough that filtering in Go
// after one simple query is preferable to a parameterized IN clause here.
func (s *Store) ListUpcoming(ctx context.Context, from string) ([]Ride, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(`
        SELECT id, crew_id, route_slug, date, time, created_by, created_at, series_id
        FROM crew_rides WHERE date >= ? ORDER BY date, time, created_at`), from)
	if err != nil {
		return nil, fmt.Errorf("read upcoming rides: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Ride
	for rows.Next() {
		var ride Ride
		if err := rows.Scan(&ride.ID, &ride.CrewID, &ride.Slug, &ride.Date, &ride.Time, &ride.CreatedBy, &ride.CreatedAt, &ride.SeriesID); err != nil {
			return nil, fmt.Errorf("read upcoming rides: %w", err)
		}
		out = append(out, ride)
	}
	return out, rows.Err()
}

// Get returns one ride.
func (s *Store) Get(ctx context.Context, id string) (Ride, error) {
	var ride Ride
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(`
        SELECT id, crew_id, route_slug, date, time, created_by, created_at, series_id
        FROM crew_rides WHERE id = ?`), id).
		Scan(&ride.ID, &ride.CrewID, &ride.Slug, &ride.Date, &ride.Time, &ride.CreatedBy, &ride.CreatedAt, &ride.SeriesID)
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

// ClearCreatedBy blanks created_by on every ride this rider scheduled,
// without deleting the ride — called when the rider themselves is deleted
// (see the API package's purgeRiderData). A scheduled ride is the crew's
// plan, not solely its author's; deleting the author must not take the
// ride down with them the way DeleteForCrew intentionally takes every ride
// down when the crew itself goes.
func (s *Store) ClearCreatedBy(ctx context.Context, rider string) (int, error) {
	rider = strings.ToLower(strings.TrimSpace(rider))
	if rider == "" {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`UPDATE crew_rides SET created_by = '' WHERE created_by = ?`), rider)
	if err != nil {
		return 0, fmt.Errorf("clear ride authorship: %w", err)
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}
