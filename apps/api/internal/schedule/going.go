package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// "I'm going". A row in crew_ride_going says one rider is going to one ride;
// there is no maybe and no "not going" — absence is not going. The store holds
// only the fact. Whether the rider may say it (an approved member of the ride's
// crew, a ride dated today or later, a route that still exists) needs state this
// package cannot see, so internal/api checks that before calling Go, exactly as
// it does for Create.
//
// The rider on every row came from the session, never from a request body: only
// a rider writes their own row, and nothing here lets anyone write another's.

// goingSchema is the going table, applied by UseDB with the rest.
const goingSchema = `
CREATE TABLE IF NOT EXISTS crew_ride_going (
    ride_id    TEXT NOT NULL,
    rider      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (ride_id, rider)
)`

func normalizeRider(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Go records that rider is going to the ride. Idempotent: a second call changes
// nothing and is not an error.
func (s *Store) Go(ctx context.Context, rideID, rider string) error {
	rider = normalizeRider(rider)
	if rideID == "" || rider == "" {
		return fmt.Errorf("schedule: going needs a ride and a rider")
	}
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        INSERT INTO crew_ride_going (ride_id, rider, created_at) VALUES (?, ?, ?)
        ON CONFLICT (ride_id, rider) DO NOTHING`),
		rideID, rider, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("mark going: %w", err)
	}
	return nil
}

// Leave removes rider's row for the ride. Not being there is not an error.
func (s *Store) Leave(ctx context.Context, rideID, rider string) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM crew_ride_going WHERE ride_id = ? AND rider = ?`), rideID, normalizeRider(rider))
	if err != nil {
		return fmt.Errorf("unmark going: %w", err)
	}
	return nil
}

// IsGoing reports whether rider has a going row for the ride.
func (s *Store) IsGoing(ctx context.Context, rideID, rider string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT COUNT(*) FROM crew_ride_going WHERE ride_id = ? AND rider = ?`),
		rideID, normalizeRider(rider)).Scan(&n)
	return n > 0, err
}

// Going is who is going to one ride, sorted by name.
func (s *Store) Going(ctx context.Context, rideID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(
		`SELECT rider FROM crew_ride_going WHERE ride_id = ? ORDER BY rider`), rideID)
	if err != nil {
		return nil, fmt.Errorf("read who is going: %w", err)
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
	return out, rows.Err()
}

// GoingFrom is who is going, per ride id, for every ride dated on or after from.
// One query for a whole list of rides, never one per ride.
func (s *Store) GoingFrom(ctx context.Context, from string) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.Rebind(`
        SELECT g.ride_id, g.rider FROM crew_ride_going g
        JOIN crew_rides r ON r.id = g.ride_id
        WHERE r.date >= ? ORDER BY g.ride_id, g.rider`), from)
	if err != nil {
		return nil, fmt.Errorf("read who is going: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var id, rider string
		if err := rows.Scan(&id, &rider); err != nil {
			return nil, err
		}
		out[id] = append(out[id], rider)
	}
	return out, rows.Err()
}

// RemoveRider drops rider's going rows for every ride of one crew: called when
// the crew removes them. Their own plan is not touched: the fixed session they
// hold stays, and reads mark it orphaned until the rider confirms an update.
func (s *Store) RemoveRider(ctx context.Context, crewID, rider string) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(`
        DELETE FROM crew_ride_going WHERE rider = ?
        AND ride_id IN (SELECT id FROM crew_rides WHERE crew_id = ?)`), normalizeRider(rider), crewID)
	if err != nil {
		return fmt.Errorf("remove rider from crew rides: %w", err)
	}
	return nil
}

// DeleteRider drops every going row the rider has: part of purging a rider.
func (s *Store) DeleteRider(ctx context.Context, rider string) error {
	_, err := s.db.ExecContext(ctx, s.dialect.Rebind(
		`DELETE FROM crew_ride_going WHERE rider = ?`), normalizeRider(rider))
	if err != nil {
		return fmt.Errorf("delete going rows: %w", err)
	}
	return nil
}
