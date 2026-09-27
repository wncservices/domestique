package workout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// SessionAnalysis is rideanalysis's verdict on one completed session,
// stored alongside it. This package deliberately does not import
// rideanalysis — the store stays dependency-free, the same reasoning that
// keeps model.Route free of file paths — so this struct mirrors
// rideanalysis.Result's shape by hand; the API layer converts between the
// two.
type SessionAnalysis struct {
	SessionID  string
	Rider      string
	WorkoutID  string
	Outcome    string
	LoadSource string

	NormalizedPower float64
	IntensityFactor float64
	TSS             float64
	DurationRatio   float64

	PowerZoneSeconds []int
	HRZoneSeconds    []int
	PowerCurve       map[string]float64
	Steps            []AnalysisStep

	AnalysedAt string
}

// AnalysisStep mirrors rideanalysis.StepResult's shape without importing
// that package — see SessionAnalysis's own doc comment.
type AnalysisStep struct {
	Index       int     `json:"index"`
	Name        string  `json:"name"`
	Target      string  `json:"target"`
	Low         float64 `json:"low"`
	High        float64 `json:"high"`
	Actual      float64 `json:"actual"`
	Result      string  `json:"result"`
	InTargetPct float64 `json:"inTargetPct,omitempty"`
}

// SaveAnalysis upserts on session_id — a re-analysed ride (e.g. after a
// rider's FTP changes) replaces the previous verdict rather than
// accumulating a second row, the same upsert shape SaveProfile and
// SavePush already use.
func (d *DB) SaveAnalysis(ctx context.Context, a SessionAnalysis) error {
	if a.SessionID == "" {
		return errors.New("workout: an analysis needs a session id")
	}
	rider := normalizeRider(a.Rider)
	if rider == "" {
		return errors.New("workout: no rider — whose analysis is this?")
	}

	powerZones, err := json.Marshal(a.PowerZoneSeconds)
	if err != nil {
		return fmt.Errorf("workout: encode power zone seconds: %w", err)
	}
	hrZones, err := json.Marshal(a.HRZoneSeconds)
	if err != nil {
		return fmt.Errorf("workout: encode hr zone seconds: %w", err)
	}
	powerCurve, err := json.Marshal(a.PowerCurve)
	if err != nil {
		return fmt.Errorf("workout: encode power curve: %w", err)
	}
	steps, err := json.Marshal(a.Steps)
	if err != nil {
		return fmt.Errorf("workout: encode steps: %w", err)
	}

	analysedAt := a.AnalysedAt
	if analysedAt == "" {
		analysedAt = timestamp()
	}

	_, err = d.db.ExecContext(ctx, d.query(`
        INSERT INTO session_analyses (session_id, rider, workout_id, outcome, load_source,
                    normalized_power, intensity_factor, tss, duration_ratio,
                    power_zone_seconds, hr_zone_seconds, power_curve, steps, analysed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (session_id) DO UPDATE SET
            rider = excluded.rider, workout_id = excluded.workout_id, outcome = excluded.outcome,
            load_source = excluded.load_source, normalized_power = excluded.normalized_power,
            intensity_factor = excluded.intensity_factor, tss = excluded.tss,
            duration_ratio = excluded.duration_ratio, power_zone_seconds = excluded.power_zone_seconds,
            hr_zone_seconds = excluded.hr_zone_seconds, power_curve = excluded.power_curve,
            steps = excluded.steps, analysed_at = excluded.analysed_at`),
		a.SessionID, rider, a.WorkoutID, a.Outcome, a.LoadSource,
		a.NormalizedPower, a.IntensityFactor, a.TSS, a.DurationRatio,
		powerZones, hrZones, powerCurve, steps, analysedAt)
	return err
}

// GetAnalysis returns a session's stored analysis, or ok=false if the
// session has never been analysed (not yet synced, or analysis failed and
// fell back to summary-only — see rideanalysis's own fallback rules).
func (d *DB) GetAnalysis(ctx context.Context, sessionID string) (SessionAnalysis, bool, error) {
	row := d.db.QueryRowContext(ctx, d.query(`
        SELECT session_id, rider, workout_id, outcome, load_source,
               normalized_power, intensity_factor, tss, duration_ratio,
               power_zone_seconds, hr_zone_seconds, power_curve, steps, analysed_at
        FROM session_analyses WHERE session_id = ?`), sessionID)
	a, err := scanAnalysis(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionAnalysis{}, false, nil
	}
	if err != nil {
		return SessionAnalysis{}, false, err
	}
	return a, true, nil
}

// ListAnalyses returns a rider's analyses for sessions on or after
// sinceDate, most recent first — joined to completed_sessions for its date,
// since session_analyses itself carries no date of its own (the ride's date
// is the session's, not the analysis run's).
func (d *DB) ListAnalyses(ctx context.Context, rider, sinceDate string) ([]SessionAnalysis, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT a.session_id, a.rider, a.workout_id, a.outcome, a.load_source,
               a.normalized_power, a.intensity_factor, a.tss, a.duration_ratio,
               a.power_zone_seconds, a.hr_zone_seconds, a.power_curve, a.steps, a.analysed_at
        FROM session_analyses a
        JOIN completed_sessions s ON s.id = a.session_id
        WHERE a.rider = ? AND s.date >= ?
        ORDER BY s.date DESC, a.session_id`), normalizeRider(rider), sinceDate)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var analyses []SessionAnalysis
	for rows.Next() {
		a, err := scanAnalysis(rows)
		if err != nil {
			return nil, err
		}
		analyses = append(analyses, a)
	}
	return analyses, rows.Err()
}

// scanAnalysis serves both GetAnalysis and ListAnalyses, the same
// rowScanner-based sharing scanWorkout uses for Goal/Workout.
func scanAnalysis(row rowScanner) (SessionAnalysis, error) {
	var (
		a                                      SessionAnalysis
		powerZones, hrZones, powerCurve, steps []byte
	)
	if err := row.Scan(&a.SessionID, &a.Rider, &a.WorkoutID, &a.Outcome, &a.LoadSource,
		&a.NormalizedPower, &a.IntensityFactor, &a.TSS, &a.DurationRatio,
		&powerZones, &hrZones, &powerCurve, &steps, &a.AnalysedAt); err != nil {
		return SessionAnalysis{}, err
	}
	if len(powerZones) > 0 {
		if err := json.Unmarshal(powerZones, &a.PowerZoneSeconds); err != nil {
			return SessionAnalysis{}, fmt.Errorf("workout: decode power zone seconds for %s: %w", a.SessionID, err)
		}
	}
	if len(hrZones) > 0 {
		if err := json.Unmarshal(hrZones, &a.HRZoneSeconds); err != nil {
			return SessionAnalysis{}, fmt.Errorf("workout: decode hr zone seconds for %s: %w", a.SessionID, err)
		}
	}
	if len(powerCurve) > 0 {
		if err := json.Unmarshal(powerCurve, &a.PowerCurve); err != nil {
			return SessionAnalysis{}, fmt.Errorf("workout: decode power curve for %s: %w", a.SessionID, err)
		}
	}
	if len(steps) > 0 {
		if err := json.Unmarshal(steps, &a.Steps); err != nil {
			return SessionAnalysis{}, fmt.Errorf("workout: decode steps for %s: %w", a.SessionID, err)
		}
	}
	return a, nil
}

// SetSessionLoad updates a completed session's training_load to the
// analysed figure (rideanalysis's TSS, when available) — a more accurate
// number than the estimate UpsertSession originally recorded from
// duration/power/HR alone. The caller is expected to run
// RecomputeFitnessSnapshots afterwards; this only touches the one row.
func (d *DB) SetSessionLoad(ctx context.Context, sessionID string, load float64) error {
	result, err := d.db.ExecContext(ctx, d.query(
		`UPDATE completed_sessions SET training_load = ? WHERE id = ?`), load, sessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("workout: no such session %q", sessionID)
	}
	return nil
}
