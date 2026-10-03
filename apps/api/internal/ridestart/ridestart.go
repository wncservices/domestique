// Package ridestart keeps where a rider's rides start, so a route can be
// generated for a planned ride without asking each time.
//
// It is the one stored location that has to be a real road: a forecast is
// right at a town, a loop is wrong unless it starts in the right street. That
// is why it is not the weather town, and why it is kept at three decimals
// (about 110 m) rather than two: the routing engine snaps to a road anyway,
// and the exact doorstep is never needed or kept. The row is the opt-in:
// no row, no start; deleting the row is the opt-out. The API reports only
// whether a start is set and its town, never the coordinates.
package ridestart

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/weather"
)

// MaxPlaceLen bounds the stored town name, in characters.
const MaxPlaceLen = 80

// ErrInvalid wraps a rejected value; its text is safe to show the rider.
var ErrInvalid = errors.New("ride start: invalid value")

// Point is one rider's start. Lat and Lon were rounded on the way in.
type Point struct {
	Rider    string
	Place    string
	Lat, Lon float64
}

// Store keeps the start points.
type Store struct {
	// Now is the clock, replaceable in tests. Nil means time.Now.
	Now func() time.Time

	db      *sql.DB
	dialect dbx.Dialect
}

// schema is one statement for both engines: nothing here differs between
// SQLite and PostgreSQL.
const schema = `
CREATE TABLE IF NOT EXISTS ride_start_points (
    rider      TEXT PRIMARY KEY,
    place      TEXT NOT NULL,
    lat        DOUBLE PRECISION NOT NULL,
    lon        DOUBLE PRECISION NOT NULL,
    updated_at TEXT NOT NULL
);`

// UseDB creates the table (idempotently) in an already-open database.
func UseDB(db *sql.DB, dsn string) (*Store, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create ride_start_points table: %w", err)
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

// Round keeps a coordinate to three decimals, about 110 m.
func Round(x float64) float64 { return math.Round(x*1000) / 1000 }

// Get returns the rider's start; ok is false when they have not set one.
func (s *Store) Get(ctx context.Context, rider string) (Point, bool, error) {
	var p Point
	// #nosec G701 -- constant statement, bound parameter.
	err := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT rider, place, lat, lon FROM ride_start_points WHERE rider = ?`),
		normalise(rider)).Scan(&p.Rider, &p.Place, &p.Lat, &p.Lon)
	if errors.Is(err, sql.ErrNoRows) {
		return Point{}, false, nil
	}
	if err != nil {
		return Point{}, false, fmt.Errorf("ride start: reading: %w", err)
	}
	return p, true, nil
}

// Set opts a rider in, or moves their start. The client's value is untrusted:
// coordinates are rounded to three decimals before they touch the database,
// and the place is reduced to a town (weather.TownLabel) so a street address
// that happened to be searched for is never kept.
func (s *Store) Set(ctx context.Context, rider, place string, lat, lon float64) error {
	rider = normalise(rider)
	place = weather.TownLabel(place)
	switch {
	case rider == "":
		return fmt.Errorf("%w: no rider", ErrInvalid)
	case place == "":
		return fmt.Errorf("%w: place must name a town", ErrInvalid)
	case math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) ||
		lat < -90 || lat > 90 || lon < -180 || lon > 180:
		return fmt.Errorf("%w: coordinates out of range", ErrInvalid)
	}
	if r := []rune(place); len(r) > MaxPlaceLen {
		place = string(r[:MaxPlaceLen])
	}

	// #nosec G701 -- constant statement, bound parameters.
	if _, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO ride_start_points (rider, place, lat, lon, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (rider) DO UPDATE SET place = excluded.place, lat = excluded.lat,
		   lon = excluded.lon, updated_at = excluded.updated_at`),
		rider, place, Round(lat), Round(lon), s.now().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("ride start: saving: %w", err)
	}
	return nil
}

// Delete removes the rider's row: the opt-out. Deleting nothing is not an
// error.
func (s *Store) Delete(ctx context.Context, rider string) error {
	// #nosec G701 -- constant statement, bound parameter.
	if _, err := s.db.ExecContext(ctx, s.rebind(
		`DELETE FROM ride_start_points WHERE rider = ?`), normalise(rider)); err != nil {
		return fmt.Errorf("ride start: removing: %w", err)
	}
	return nil
}
