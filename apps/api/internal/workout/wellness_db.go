package workout

import (
	"context"
	"errors"
)

// DailyWellness is one rider's recovery signals for one calendar date, as
// read from garmin.Wellness and stored for internal/readiness to assess.
// This package deliberately does not import garmin — the store stays
// dependency-free, the same reasoning ProgressionLevel's own doc comment
// gives for not importing internal/progression — so this is a plain value
// the API layer fills in from garmin.Wellness.
type DailyWellness struct {
	Rider string
	Date  string

	HRVLastNight float64
	HRVWeeklyAvg float64
	HRVStatus    string

	SleepSeconds int
	SleepScore   int

	ReadinessScore int
	ReadinessLevel string

	RestingHR int

	UpdatedAt string
}

// SaveWellness upserts on (rider, date) — a re-sync of the same day
// corrects the stored reading rather than accumulating a second row, the
// same upsert shape SaveLevel already uses for progression_levels.
func (d *DB) SaveWellness(ctx context.Context, w DailyWellness) error {
	rider := normalizeRider(w.Rider)
	if rider == "" {
		return errors.New("workout: no rider — whose wellness reading is this?")
	}
	if w.Date == "" {
		return errors.New("workout: a wellness reading needs a date")
	}

	updatedAt := w.UpdatedAt
	if updatedAt == "" {
		updatedAt = timestamp()
	}

	_, err := d.db.ExecContext(ctx, d.query(`
        INSERT INTO daily_wellness (rider, date, hrv_last_night, hrv_weekly_avg, hrv_status,
                    sleep_seconds, sleep_score, readiness_score, readiness_level, resting_hr, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (rider, date) DO UPDATE SET
            hrv_last_night = excluded.hrv_last_night, hrv_weekly_avg = excluded.hrv_weekly_avg,
            hrv_status = excluded.hrv_status, sleep_seconds = excluded.sleep_seconds,
            sleep_score = excluded.sleep_score, readiness_score = excluded.readiness_score,
            readiness_level = excluded.readiness_level, resting_hr = excluded.resting_hr,
            updated_at = excluded.updated_at`),
		rider, w.Date, w.HRVLastNight, w.HRVWeeklyAvg, w.HRVStatus,
		w.SleepSeconds, w.SleepScore, w.ReadinessScore, w.ReadinessLevel, w.RestingHR, updatedAt)
	return err
}

// ListWellness returns one rider's daily wellness rows from sinceDate
// (inclusive) onward, ascending by date — the shape internal/readiness
// needs to look back over the last few nights, oldest first. An empty
// sinceDate returns every row on file for that rider.
func (d *DB) ListWellness(ctx context.Context, rider, sinceDate string) ([]DailyWellness, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT rider, date, hrv_last_night, hrv_weekly_avg, hrv_status,
               sleep_seconds, sleep_score, readiness_score, readiness_level, resting_hr, updated_at
        FROM daily_wellness WHERE rider = ? AND date >= ? ORDER BY date`),
		normalizeRider(rider), sinceDate)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []DailyWellness
	for rows.Next() {
		var w DailyWellness
		if err := rows.Scan(&w.Rider, &w.Date, &w.HRVLastNight, &w.HRVWeeklyAvg, &w.HRVStatus,
			&w.SleepSeconds, &w.SleepScore, &w.ReadinessScore, &w.ReadinessLevel, &w.RestingHR, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
