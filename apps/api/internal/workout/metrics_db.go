package workout

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// UpsertSession records a completed session, or updates it if the same
// provider already reported this external id — the idempotent-re-pull
// property CompletedSession's own doc comment describes. id is
// provider:externalID directly rather than a generated slug: that pair is
// already guaranteed unique (the schema's own UNIQUE constraint), so a
// second id scheme on top of it would just be two ways to ask the same
// question.
func (d *DB) UpsertSession(ctx context.Context, req UpsertSessionRequest) (CompletedSession, error) {
	rider := normalizeRider(req.Rider)
	if rider == "" {
		return CompletedSession{}, errors.New("workout: no rider — whose session is this?")
	}
	if req.Provider == "" || req.ExternalID == "" {
		return CompletedSession{}, errors.New("workout: a session needs both a provider and an external id")
	}
	id := req.Provider + ":" + req.ExternalID

	_, err := d.db.ExecContext(ctx, d.query(`
        INSERT INTO completed_sessions (id, rider, provider, external_id, sport, date,
                    duration_seconds, distance_m, avg_hr, avg_power_watts, training_load, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (provider, external_id) DO UPDATE SET
            sport = excluded.sport, date = excluded.date,
            duration_seconds = excluded.duration_seconds, distance_m = excluded.distance_m,
            avg_hr = excluded.avg_hr, avg_power_watts = excluded.avg_power_watts,
            training_load = excluded.training_load`),
		id, rider, req.Provider, req.ExternalID, req.Sport, req.Date,
		req.DurationSeconds, req.DistanceM, req.AvgHR, req.AvgPowerWatts, req.TrainingLoad, timestamp())
	if err != nil {
		return CompletedSession{}, err
	}
	return d.getSession(ctx, id)
}

func (d *DB) getSession(ctx context.Context, id string) (CompletedSession, error) {
	var s CompletedSession
	err := d.db.QueryRowContext(ctx, d.query(`
        SELECT id, rider, provider, external_id, sport, date,
               duration_seconds, distance_m, avg_hr, avg_power_watts, training_load, created_at
        FROM completed_sessions WHERE id = ?`), id).Scan(
		&s.ID, &s.Rider, &s.Provider, &s.ExternalID, &s.Sport, &s.Date,
		&s.DurationSeconds, &s.DistanceM, &s.AvgHR, &s.AvgPowerWatts, &s.TrainingLoad, &s.CreatedAt)
	return s, err
}

// ListSessions returns a rider's completed sessions, most recent first.
func (d *DB) ListSessions(ctx context.Context, rider string) ([]CompletedSession, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT id, rider, provider, external_id, sport, date,
               duration_seconds, distance_m, avg_hr, avg_power_watts, training_load, created_at
        FROM completed_sessions WHERE rider = ? ORDER BY date DESC, id`), normalizeRider(rider))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var sessions []CompletedSession
	for rows.Next() {
		var s CompletedSession
		if err := rows.Scan(&s.ID, &s.Rider, &s.Provider, &s.ExternalID, &s.Sport, &s.Date,
			&s.DurationSeconds, &s.DistanceM, &s.AvgHR, &s.AvgPowerWatts, &s.TrainingLoad, &s.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// RecomputeFitnessSnapshots rebuilds a rider's whole CTL/ATL/TSB history
// from their completed_sessions — cheap enough to do in full each time
// (a rider's real session count is in the hundreds, not millions) and
// simpler and more obviously correct than trying to patch a running
// average incrementally, the same "just recompute it" choice
// RecalculateElevation's own doc comment in internal/source makes for a
// different feature.
//
// Called after UpsertSession lands new sessions — see the API layer's sync
// handler — not on every read, so a fitness chart is a cheap SELECT rather
// than a recompute on every page load.
func (d *DB) RecomputeFitnessSnapshots(ctx context.Context, rider string) error {
	rider = normalizeRider(rider)
	sessions, err := d.ListSessions(ctx, rider)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return nil
	}

	byDate := map[string]float64{}
	var minDate, maxDate string
	for _, s := range sessions {
		byDate[s.Date] += s.TrainingLoad
		if minDate == "" || s.Date < minDate {
			minDate = s.Date
		}
		if s.Date > maxDate {
			maxDate = s.Date
		}
	}
	today := time.Now().UTC().Format("2006-01-02")
	if today > maxDate {
		maxDate = today
	}

	loads, err := fillDailyLoads(byDate, minDate, maxDate)
	if err != nil {
		return err
	}
	snapshots := ComputeFitness(loads)

	// A full rebuild replaces the whole history in one transaction — a
	// half-written recompute (crash or error partway through) must never
	// leave some days on the old numbers and others on the new ones, which
	// would make the fitness chart internally inconsistent in a way no
	// rider reading it could tell was wrong.
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, d.query(`DELETE FROM fitness_snapshots WHERE rider = ?`), rider); err != nil {
		return err
	}
	for _, snap := range snapshots {
		if _, err := tx.ExecContext(ctx, d.query(`
            INSERT INTO fitness_snapshots (rider, date, ctl, atl, tsb) VALUES (?, ?, ?, ?, ?)`),
			rider, snap.Date, snap.CTL, snap.ATL, snap.TSB); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// fillDailyLoads turns a sparse date->load map into the gap-free, sorted
// slice ComputeFitness requires — see DailyLoad's own doc comment for why
// a rest day needs a real 0 entry rather than being absent.
func fillDailyLoads(byDate map[string]float64, minDate, maxDate string) ([]DailyLoad, error) {
	start, err := time.Parse("2006-01-02", minDate)
	if err != nil {
		return nil, fmt.Errorf("workout: bad session date %q: %w", minDate, err)
	}
	end, err := time.Parse("2006-01-02", maxDate)
	if err != nil {
		return nil, fmt.Errorf("workout: bad date %q: %w", maxDate, err)
	}

	var loads []DailyLoad
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		loads = append(loads, DailyLoad{Date: date, Load: byDate[date]})
	}
	sort.Slice(loads, func(i, j int) bool { return loads[i].Date < loads[j].Date })
	return loads, nil
}

// ListFitnessSnapshots returns a rider's whole CTL/ATL/TSB history, oldest
// first — what a fitness chart plots directly.
func (d *DB) ListFitnessSnapshots(ctx context.Context, rider string) ([]FitnessSnapshot, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT rider, date, ctl, atl, tsb FROM fitness_snapshots WHERE rider = ? ORDER BY date`),
		normalizeRider(rider))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var snapshots []FitnessSnapshot
	for rows.Next() {
		var s FitnessSnapshot
		if err := rows.Scan(&s.Rider, &s.Date, &s.CTL, &s.ATL, &s.TSB); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}
