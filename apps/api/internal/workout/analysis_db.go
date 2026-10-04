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

	// MaxHR and BestSpeed1200/BestSpeed1800 mirror rideanalysis.Analysis's
	// own fields of the same name — the ride's peak heart rate and best
	// 20-/30-minute average speeds, threshold detection's raw material. See
	// that package's own doc comment for why they are 0 for a ride with no
	// decoded FIT activity.
	MaxHR         int
	BestHR1200    int
	BestSpeed1200 float64
	BestSpeed1800 float64

	PowerZoneSeconds []int
	HRZoneSeconds    []int
	PowerCurve       map[string]float64
	Steps            []AnalysisStep

	// Feel is the rider's optional 1-5 "how did it feel" rating (0 = not
	// rated). LevelDelta is the progression-level change this ride applied
	// (internal/progression.Delta's result) — stored so a re-rate can
	// subtract the old change before applying the new one instead of
	// stacking a second adjustment on top. See SetAnalysisFeel.
	Feel       int
	LevelDelta float64

	// Legs ("fresh", "normal", "heavy") and Stress ("low", "normal", "high")
	// are the survey's optional answers next to Feel; "" is unanswered. They
	// are set only by SetAnalysisSurvey and, like Feel, are not touched by a
	// re-analysis.
	Legs   string
	Stress string

	AnalysedAt string
}

// EffectiveOutcome is the outcome the plan should react to: the stored one,
// except that an all-out effort (5 of 5) on a ride that matched a planned
// workout and was scored nailed or completed counts as struggled. A rider's
// own word that it was too much outweighs a power file that says it went to
// plan. An unplanned ride, an incomplete one and an already-struggled one are
// untouched, and effort 1-4 never changes anything. This is the only place
// that rule lives; everything that reads how a ride went asks here.
func (a SessionAnalysis) EffectiveOutcome() string {
	if a.Feel == 5 && a.WorkoutID != "" && (a.Outcome == "nailed" || a.Outcome == "completed") {
		return "struggled"
	}
	return a.Outcome
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
	// Hard mirrors rideanalysis.StepResult.Hard — see that field's own doc
	// comment for why a scored step is not necessarily a "hard" one (a
	// generated interval session's Recovery step carries a real power
	// target, not TargetOpen, so it gets scored too).
	Hard bool `json:"hard,omitempty"`
}

// SaveAnalysis upserts on session_id — a re-analysed ride (e.g. after a
// rider's FTP changes) replaces the previous verdict rather than
// accumulating a second row, the same upsert shape SaveProfile and
// SavePush already use.
//
// feel and level_delta are deliberately excluded from the conflict
// update: they are set only by SetAnalysisFeel, once a rider actually
// rates the ride. A re-analysis (or a re-sync of the same ride) must not
// wipe a rating that already happened — a.Feel/a.LevelDelta are only used
// on the first insert of a session that has never been analysed before,
// where there is nothing to preserve yet.
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
                    max_hr, best_hr_1200, best_speed_1200, best_speed_1800,
                    power_zone_seconds, hr_zone_seconds, power_curve, steps,
                    feel, level_delta, analysed_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (session_id) DO UPDATE SET
            rider = excluded.rider, workout_id = excluded.workout_id, outcome = excluded.outcome,
            load_source = excluded.load_source, normalized_power = excluded.normalized_power,
            intensity_factor = excluded.intensity_factor, tss = excluded.tss,
            duration_ratio = excluded.duration_ratio,
            max_hr = excluded.max_hr, best_hr_1200 = excluded.best_hr_1200, best_speed_1200 = excluded.best_speed_1200,
            best_speed_1800 = excluded.best_speed_1800,
            power_zone_seconds = excluded.power_zone_seconds,
            hr_zone_seconds = excluded.hr_zone_seconds, power_curve = excluded.power_curve,
            steps = excluded.steps, analysed_at = excluded.analysed_at`),
		a.SessionID, rider, a.WorkoutID, a.Outcome, a.LoadSource,
		a.NormalizedPower, a.IntensityFactor, a.TSS, a.DurationRatio,
		a.MaxHR, a.BestHR1200, a.BestSpeed1200, a.BestSpeed1800,
		powerZones, hrZones, powerCurve, steps, a.Feel, a.LevelDelta, analysedAt)
	return err
}

// GetAnalysis returns a session's stored analysis, or ok=false if the
// session has never been analysed (not yet synced, or analysis failed and
// fell back to summary-only — see rideanalysis's own fallback rules).
func (d *DB) GetAnalysis(ctx context.Context, sessionID string) (SessionAnalysis, bool, error) {
	row := d.db.QueryRowContext(ctx, d.query(`
        SELECT session_id, rider, workout_id, outcome, load_source,
               normalized_power, intensity_factor, tss, duration_ratio,
               max_hr, best_hr_1200, best_speed_1200, best_speed_1800,
               power_zone_seconds, hr_zone_seconds, power_curve, steps,
               feel, level_delta, legs, stress, analysed_at
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
               a.max_hr, a.best_hr_1200, a.best_speed_1200, a.best_speed_1800,
               a.power_zone_seconds, a.hr_zone_seconds, a.power_curve, a.steps,
               a.feel, a.level_delta, a.legs, a.stress, a.analysed_at
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
		&a.MaxHR, &a.BestHR1200, &a.BestSpeed1200, &a.BestSpeed1800,
		&powerZones, &hrZones, &powerCurve, &steps,
		&a.Feel, &a.LevelDelta, &a.Legs, &a.Stress, &a.AnalysedAt); err != nil {
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

// SetAnalysisFeel records a rider's "how did it feel" rating and the
// progression-level delta it produced, replacing whatever this session's
// analysis previously recorded — a re-rate overwrites levelDelta rather
// than the caller stacking a second adjustment on top of the first. The
// caller (internal/progression plus the level store) is responsible for
// working out levelDelta and for undoing the old delta before applying the
// new one; this just persists the pair.
func (d *DB) SetAnalysisFeel(ctx context.Context, sessionID string, feel int, levelDelta float64) error {
	result, err := d.db.ExecContext(ctx, d.query(
		`UPDATE session_analyses SET feel = ?, level_delta = ? WHERE session_id = ?`),
		feel, levelDelta, sessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("workout: no such session %q", sessionID)
	}
	return nil
}

// SetAnalysisSurvey records the whole post-ride survey and the level delta it
// produced, replacing what was there: an omitted legs or stress ("") clears
// it, so the rider's last tap is the whole truth. Feel is required by the
// caller; this only persists. Like SetAnalysisFeel it does not work out
// levelDelta.
func (d *DB) SetAnalysisSurvey(ctx context.Context, sessionID string, feel int, legs, stress string, levelDelta float64) error {
	result, err := d.db.ExecContext(ctx, d.query(
		`UPDATE session_analyses SET feel = ?, legs = ?, stress = ?, level_delta = ? WHERE session_id = ?`),
		feel, legs, stress, levelDelta, sessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("workout: no such session %q", sessionID)
	}
	return nil
}

// SetAnalysisLoadSource records which basis a session's load now rests on,
// for the one case that changes after analysis: an effort rating standing in
// for the flat estimate ("session_rpe").
func (d *DB) SetAnalysisLoadSource(ctx context.Context, sessionID, source string) error {
	_, err := d.db.ExecContext(ctx, d.query(`UPDATE session_analyses SET load_source = ? WHERE session_id = ?`), source, sessionID)
	return err
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

// HasAnalysis reports whether a session has a saved analysis.
func (d *DB) HasAnalysis(ctx context.Context, sessionID string) (bool, error) {
	var one int
	err := d.db.QueryRowContext(ctx, d.query(`SELECT 1 FROM session_analyses WHERE session_id = ?`), sessionID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
