package weather

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// The default ride window is a common weekend-morning ride, and what a rider
// who never touches the setting is judged on.
const (
	DefaultWindowStart = 9
	DefaultWindowEnd   = 12

	// MaxPlaceLen bounds the stored town name, in characters.
	MaxPlaceLen = 80
)

var (
	// ErrNoLocation means a window was set for a rider who has not chosen a
	// town: the row is the opt-in, so there is nothing to attach it to.
	ErrNoLocation = errors.New("weather: choose a town first")
	// ErrInvalid wraps a rejected value; its text is safe to show the rider.
	ErrInvalid = errors.New("weather: invalid value")
)

// Preference is one rider's opt-in: a town at about 1 km precision and the
// hours they usually ride. Lat and Lon are rounded on the way in and are
// never returned by the API.
type Preference struct {
	Rider                  string
	Place                  string
	Lat, Lon               float64
	WindowStart, WindowEnd int // local hours, start inclusive, end exclusive
}

// Location is what a forecast is fetched for.
func (p Preference) Location() Location { return Location{Lat: p.Lat, Lon: p.Lon} }

// Store keeps the preferences. A rider has a row if and only if they opted in:
// deleting the row is "Stop using weather".
type Store struct {
	// Now is the clock, replaceable in tests. Nil means time.Now.
	Now func() time.Time

	db      *sql.DB
	dialect dbx.Dialect
}

// schema is one statement for both engines: nothing here (no blob, no boolean)
// differs between SQLite and PostgreSQL. lat and lon are already rounded when
// they get here.
//
// A separate table, not profile columns: saving the profile form has
// "confirmed value" semantics that do not fit, and dropping the row is the
// opt-out.
const schema = `
CREATE TABLE IF NOT EXISTS weather_locations (
    rider        TEXT PRIMARY KEY,
    place        TEXT NOT NULL,
    lat          DOUBLE PRECISION NOT NULL,
    lon          DOUBLE PRECISION NOT NULL,
    window_start INTEGER NOT NULL DEFAULT 9,
    window_end   INTEGER NOT NULL DEFAULT 12,
    updated_at   TEXT NOT NULL
);`

// UseDB creates the table (idempotently) in an already-open database.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create weather_locations table: %w", err)
	}
	return &Store{db: db, dialect: d}, nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func normalise(rider string) string { return strings.ToLower(strings.TrimSpace(rider)) }

func (s *Store) rebind(q string) string { return s.dialect.Rebind(q) }

// Get returns the rider's preference; ok is false when they have not opted in.
func (s *Store) Get(ctx context.Context, rider string) (Preference, bool, error) {
	var p Preference
	// #nosec G701 -- constant statement, bound parameter.
	err := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT rider, place, lat, lon, window_start, window_end FROM weather_locations WHERE rider = ?`),
		normalise(rider)).Scan(&p.Rider, &p.Place, &p.Lat, &p.Lon, &p.WindowStart, &p.WindowEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return Preference{}, false, nil
	}
	if err != nil {
		return Preference{}, false, fmt.Errorf("weather: reading location: %w", err)
	}
	return p, true, nil
}

// SetLocation opts a rider in (or moves them). Coordinates are rounded to two
// decimals before they touch the database, so the precise value the browser
// sent is never kept; the place is reduced to a town (TownLabel) and cut to MaxPlaceLen characters.
// An existing window is left alone.
func (s *Store) SetLocation(ctx context.Context, rider, place string, lat, lon float64) error {
	rider = normalise(rider)
	// The client's value is untrusted: keep only a town-level label, never a
	// street address that happened to be searched for.
	place = TownLabel(place)
	switch {
	case rider == "":
		return fmt.Errorf("%w: no rider", ErrInvalid)
	case place == "":
		return fmt.Errorf("%w: place must name a town", ErrInvalid)
	case math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180:
		return fmt.Errorf("%w: coordinates out of range", ErrInvalid)
	}
	if r := []rune(place); len(r) > MaxPlaceLen {
		place = string(r[:MaxPlaceLen])
	}
	loc := RoundLocation(Location{Lat: lat, Lon: lon})

	// #nosec G701 -- constant statement, bound parameters.
	if _, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO weather_locations (rider, place, lat, lon, window_start, window_end, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (rider) DO UPDATE SET place = excluded.place, lat = excluded.lat,
		   lon = excluded.lon, updated_at = excluded.updated_at`),
		rider, place, loc.Lat, loc.Lon, DefaultWindowStart, DefaultWindowEnd,
		s.now().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("weather: saving location: %w", err)
	}
	return nil
}

// SetWindow sets the hours the rider usually rides: start 0 to 23, end after
// start and at most 23. It needs a saved location (ErrNoLocation).
func (s *Store) SetWindow(ctx context.Context, rider string, start, end int) error {
	if start < 0 || start > 23 || end < 0 || end > 23 || start >= end {
		return fmt.Errorf("%w: the window must be hours 0 to 23 with the start before the end", ErrInvalid)
	}
	// #nosec G701 -- constant statement, bound parameters.
	res, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE weather_locations SET window_start = ?, window_end = ?, updated_at = ? WHERE rider = ?`),
		start, end, s.now().Format(time.RFC3339), normalise(rider))
	if err != nil {
		return fmt.Errorf("weather: saving window: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNoLocation
	}
	return nil
}

// Delete removes the rider's row: the opt-out. Deleting nothing is not an error.
func (s *Store) Delete(ctx context.Context, rider string) error {
	// #nosec G701 -- constant statement, bound parameter.
	if _, err := s.db.ExecContext(ctx, s.rebind(
		`DELETE FROM weather_locations WHERE rider = ?`), normalise(rider)); err != nil {
		return fmt.Errorf("weather: removing location: %w", err)
	}
	return nil
}

// List returns every opted-in rider's preference, for the sync's cache warm-up.
func (s *Store) List(ctx context.Context) ([]Preference, error) {
	// #nosec G701 -- constant statement.
	rows, err := s.db.QueryContext(ctx,
		`SELECT rider, place, lat, lon, window_start, window_end FROM weather_locations ORDER BY rider`)
	if err != nil {
		return nil, fmt.Errorf("weather: listing locations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Preference
	for rows.Next() {
		var p Preference
		if err := rows.Scan(&p.Rider, &p.Place, &p.Lat, &p.Lon, &p.WindowStart, &p.WindowEnd); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
