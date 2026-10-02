package workout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// schema is every table this package owns, in whichever types the engine
// uses. steps is JSON, read and written as a whole — never queried by
// individual step, the same reasoning model.Route gives for not splitting
// RouteStats into its own table (see this package's own doc comment).
func schema(d dbx.Dialect) string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS goals (
    id                 TEXT PRIMARY KEY,
    rider              TEXT NOT NULL,
    name               TEXT NOT NULL,
    sport              TEXT NOT NULL DEFAULT 'cycling',
    event_date         TEXT NOT NULL DEFAULT '',
    priority           TEXT NOT NULL DEFAULT 'B',
    target_distance_m  DOUBLE PRECISION NOT NULL DEFAULT 0,
    target_elevation_m DOUBLE PRECISION NOT NULL DEFAULT 0,
    notes              TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS goals_rider_idx ON goals (rider);

CREATE TABLE IF NOT EXISTS rider_profiles (
    rider                      TEXT PRIMARY KEY,
    ftp_watts                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    ftp_estimated              %[2]s NOT NULL DEFAULT FALSE,
    estimated_fields           TEXT NOT NULL DEFAULT '',
    auto_push_workouts         %[2]s NOT NULL DEFAULT FALSE,
    ftp_levels_calibrated_watts DOUBLE PRECISION NOT NULL DEFAULT 0,
    threshold_pace_sec_per_km  DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_hr                     INTEGER NOT NULL DEFAULT 0,
    threshold_hr               INTEGER NOT NULL DEFAULT 0,
    resting_hr                 INTEGER NOT NULL DEFAULT 0,
    available_days             TEXT NOT NULL DEFAULT '',
    hours_per_available_day    DOUBLE PRECISION NOT NULL DEFAULT 0,
    experience_level           TEXT NOT NULL DEFAULT '',
    ftp_verified_at            TEXT NOT NULL DEFAULT '',
    ftp_test_snoozed_until     TEXT NOT NULL DEFAULT '',
    updated_at                 TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workouts (
    id           TEXT PRIMARY KEY,
    rider        TEXT NOT NULL,
    sport        TEXT NOT NULL DEFAULT 'cycling',
    name         TEXT NOT NULL,
    goal_id      TEXT NOT NULL DEFAULT '',
    date         TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    steps        %[1]s NOT NULL,
    zone         TEXT NOT NULL DEFAULT '',
    level        DOUBLE PRECISION NOT NULL DEFAULT 0,
    test_protocol     TEXT NOT NULL DEFAULT '',
    test_result_watts DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS workouts_rider_idx ON workouts (rider);
CREATE INDEX IF NOT EXISTS workouts_goal_idx ON workouts (goal_id);

CREATE TABLE IF NOT EXISTS workout_pushes (
    workout_id     TEXT NOT NULL,
    provider       TEXT NOT NULL,
    remote_id      TEXT NOT NULL,
    schedule_id    TEXT NOT NULL DEFAULT '',
    scheduled_date TEXT NOT NULL DEFAULT '',
    content_hash   TEXT NOT NULL,
    pushed_at      TEXT NOT NULL,
    PRIMARY KEY (workout_id, provider)
);

CREATE TABLE IF NOT EXISTS completed_sessions (
    id                TEXT PRIMARY KEY,
    rider             TEXT NOT NULL,
    provider          TEXT NOT NULL,
    external_id       TEXT NOT NULL,
    sport             TEXT NOT NULL DEFAULT 'cycling',
    date              TEXT NOT NULL,
    duration_seconds  DOUBLE PRECISION NOT NULL DEFAULT 0,
    distance_m        DOUBLE PRECISION NOT NULL DEFAULT 0,
    avg_hr            INTEGER NOT NULL DEFAULT 0,
    avg_power_watts   DOUBLE PRECISION NOT NULL DEFAULT 0,
    training_load     DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL,
    UNIQUE (provider, external_id)
);
CREATE INDEX IF NOT EXISTS completed_sessions_rider_idx ON completed_sessions (rider, date);

CREATE TABLE IF NOT EXISTS fitness_snapshots (
    rider      TEXT NOT NULL,
    date       TEXT NOT NULL,
    ctl        DOUBLE PRECISION NOT NULL DEFAULT 0,
    atl        DOUBLE PRECISION NOT NULL DEFAULT 0,
    tsb        DOUBLE PRECISION NOT NULL DEFAULT 0,
    PRIMARY KEY (rider, date)
);

-- session_analyses holds rideanalysis's verdict on one completed session:
-- one row per analysed ride, upserted on re-analysis. No REFERENCES to
-- completed_sessions — this package's other tables (workouts.goal_id) skip
-- real FK constraints the same way, unlinking rather than relying on the
-- engine to cascade. JSON columns are TEXT on both engines, read and
-- written as a whole, never queried by individual element — the same
-- reasoning workouts.steps documents at the top of this file.
CREATE TABLE IF NOT EXISTS session_analyses (
    session_id          TEXT PRIMARY KEY,
    rider                TEXT NOT NULL,
    workout_id           TEXT NOT NULL DEFAULT '',
    outcome              TEXT NOT NULL DEFAULT '',
    load_source          TEXT NOT NULL DEFAULT '',
    normalized_power     DOUBLE PRECISION NOT NULL DEFAULT 0,
    intensity_factor     DOUBLE PRECISION NOT NULL DEFAULT 0,
    tss                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration_ratio       DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_hr               INTEGER NOT NULL DEFAULT 0,
    best_hr_1200         INTEGER NOT NULL DEFAULT 0,
    best_speed_1200      DOUBLE PRECISION NOT NULL DEFAULT 0,
    best_speed_1800      DOUBLE PRECISION NOT NULL DEFAULT 0,
    power_zone_seconds   TEXT NOT NULL DEFAULT '',
    hr_zone_seconds      TEXT NOT NULL DEFAULT '', -- basis (LTHR or max HR) known at analysis time; never re-analysed
    power_curve          TEXT NOT NULL DEFAULT '',
    steps                TEXT NOT NULL DEFAULT '',
    analysed_at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS session_analyses_rider_idx ON session_analyses (rider);

-- progression_levels holds each rider's current 1-10 level per sport/zone
-- (internal/progression computes the numbers; this table just stores the
-- result). One row per rider/sport/zone, upserted on every level change —
-- the current value and the reason it last moved (see
-- SessionAnalysis.LevelDelta for how a re-rate finds and undoes the specific
-- change it is replacing). Every move is also kept in progression_history,
-- read only by the chart.
CREATE TABLE IF NOT EXISTS progression_levels (
    rider      TEXT NOT NULL,
    sport      TEXT NOT NULL,
    zone       TEXT NOT NULL,
    level      DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason     TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (rider, sport, zone)
);

-- progression_history is every value a level has held, one row per move,
-- so the Progression card can draw a level over time rather than only where
-- it sits now. progression_levels stays the current value (what every
-- reader of "the rider's level" uses); this table is written alongside it
-- by SaveLevel and only read for the chart. at is RFC 3339.
CREATE TABLE IF NOT EXISTS progression_history (
    rider  TEXT NOT NULL,
    sport  TEXT NOT NULL,
    zone   TEXT NOT NULL,
    level  DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS progression_history_rider_idx ON progression_history (rider, at);

-- daily_wellness holds one row per rider per calendar day: the daily
-- aggregates internal/garmin's Wellness call reads (HRV, sleep, Garmin's
-- own Training Readiness, resting heart rate) for internal/readiness to
-- assess. Daily aggregates only, deliberately — no sleep stages, no raw
-- HRV samples — health data stays to the minimum this feature needs. One
-- row per (rider, date), upserted on every fetch so a re-sync corrects a
-- reading Connect had not finished processing yet rather than duplicating
-- the day.
-- threshold_suggestions holds internal/thresholds's non-auto findings — a
-- detected FTP/max-HR/threshold-pace change for a field the rider has
-- typed in themselves, which a sync never overwrites on its own (see
-- AGENTS.md's "a rider-typed FTP is never changed by a sync, only
-- suggested"). id is a random hex string (this package's schedule.newID
-- shape), not a slug: there is no rider-chosen name to slugify, and two
-- suggestions for the same rider/field over time are meant to keep their
-- own distinct ids rather than colliding on one. At most one row per
-- (rider, field) may be status='pending' at a time — enforced in Go
-- (CreateSuggestion), not by a partial unique index, since SQLite and
-- PostgreSQL spell "unique except when dismissed/accepted" differently and
-- this table's write volume never justifies the engine doing that work.
CREATE TABLE IF NOT EXISTS threshold_suggestions (
    id                 TEXT PRIMARY KEY,
    rider              TEXT NOT NULL,
    field              TEXT NOT NULL,
    value              DOUBLE PRECISION NOT NULL DEFAULT 0,
    previous           DOUBLE PRECISION NOT NULL DEFAULT 0,
    direction          TEXT NOT NULL DEFAULT 'up',
    source_session_id  TEXT NOT NULL DEFAULT '',
    source_date        TEXT NOT NULL DEFAULT '',
    reason             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'pending',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS threshold_suggestions_rider_field_idx ON threshold_suggestions (rider, field, status);

CREATE TABLE IF NOT EXISTS daily_wellness (
    rider            TEXT NOT NULL,
    date             TEXT NOT NULL,
    hrv_last_night   DOUBLE PRECISION NOT NULL DEFAULT 0,
    hrv_weekly_avg   DOUBLE PRECISION NOT NULL DEFAULT 0,
    hrv_status       TEXT NOT NULL DEFAULT '',
    sleep_seconds    INTEGER NOT NULL DEFAULT 0,
    sleep_score      INTEGER NOT NULL DEFAULT 0,
    readiness_score  INTEGER NOT NULL DEFAULT 0,
    readiness_level  TEXT NOT NULL DEFAULT '',
    resting_hr       INTEGER NOT NULL DEFAULT 0,
    updated_at       TEXT NOT NULL,
    PRIMARY KEY (rider, date)
);

-- scheduled_weeks remembers that a goal's plan week has been filled once, so
-- a session the rider then deleted, moved or rewrote is not read as a gap and
-- refilled by the next auto-schedule tick. week_start is that week's Monday.
-- session_links records a rider's own answer to "which planned session was
-- this ride?", for when the automatic match (same date, same sport, closest
-- duration) got it wrong or found nothing. workout_id '' means "this ride was
-- not a planned session". No row means the automatic match decides.
CREATE TABLE IF NOT EXISTS session_links (
    session_id TEXT PRIMARY KEY,
    rider      TEXT NOT NULL,
    workout_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS session_links_rider_idx ON session_links (rider);

CREATE TABLE IF NOT EXISTS scheduled_weeks (
    goal_id    TEXT NOT NULL,
    week_start TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (goal_id, week_start)
);

-- adjustments is the structured reason behind every automatic change to a
-- rider's plan (internal/why owns the rules and the inputs). subject_kind is
-- 'workout' (subject_id is the workout id) or 'level' (subject_id is
-- 'sport:zone'). The unique key includes rider because a level subject is
-- only unique within one rider: without it two riders' rows for the same zone
-- on the same day would overwrite each other. inputs is JSON, read and
-- written as a whole, like workouts.steps.
CREATE TABLE IF NOT EXISTS adjustments (
    id           TEXT PRIMARY KEY,
    rider        TEXT NOT NULL,
    subject_kind TEXT NOT NULL,
    subject_id   TEXT NOT NULL,
    rule         TEXT NOT NULL,
    inputs       TEXT NOT NULL DEFAULT '',
    text         TEXT NOT NULL DEFAULT '',
    day          TEXT NOT NULL,
    created_at   TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS adjustments_subject_day_rule_idx ON adjustments (rider, subject_kind, subject_id, day, rule);
CREATE INDEX IF NOT EXISTS adjustments_rider_subject_idx ON adjustments (rider, subject_kind, subject_id);`, d.Blob, d.Boolean)
}

// DeleteRider removes everything this package holds about rider, in one
// transaction, and returns how many rows that was. Used when a rider is
// deleted (see the API package's purgeRiderData): goals, profile, workouts and
// their push records, completed sessions and their analyses, fitness
// snapshots, HRV and sleep, progression levels and history, threshold
// suggestions and session links are all health and training data about one
// person, and none of it has a use once they are gone.
//
// workout_pushes and scheduled_weeks carry no rider of their own — they hang
// off a workout and a goal — so they go first, while the rows that say whose
// they are still exist. A table added to schema that holds rider data has to be
// added here too; TestEveryRiderKeyedTableIsRegistered in the API package
// fails until it is registered, and its purge test until it is deleted.
//
// All or nothing: a failure part-way rolls back, so a retry starts from the
// same place rather than from a half-purged rider. Deleting a rider with no
// rows is not an error.
func (d *DB) DeleteRider(ctx context.Context, rider string) (int, error) {
	rider = strings.ToLower(strings.TrimSpace(rider))
	if rider == "" {
		return 0, errors.New("workout: no rider to delete")
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("workout: purge rider: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Every statement is a constant with the rider bound, in dependency order.
	stmts := []string{
		`DELETE FROM workout_pushes WHERE workout_id IN (SELECT id FROM workouts WHERE rider = ?)`,
		`DELETE FROM scheduled_weeks WHERE goal_id IN (SELECT id FROM goals WHERE rider = ?)`,
		`DELETE FROM workouts WHERE rider = ?`,
		`DELETE FROM goals WHERE rider = ?`,
		`DELETE FROM session_links WHERE rider = ?`,
		`DELETE FROM session_analyses WHERE rider = ?`,
		`DELETE FROM completed_sessions WHERE rider = ?`,
		`DELETE FROM fitness_snapshots WHERE rider = ?`,
		`DELETE FROM daily_wellness WHERE rider = ?`,
		`DELETE FROM progression_history WHERE rider = ?`,
		`DELETE FROM progression_levels WHERE rider = ?`,
		`DELETE FROM threshold_suggestions WHERE rider = ?`,
		`DELETE FROM rider_profiles WHERE rider = ?`,
	}
	total := 0
	for _, stmt := range stmts {
		// #nosec G701 -- constant statement, bound parameter.
		res, err := tx.ExecContext(ctx, d.query(stmt), rider)
		if err != nil {
			return 0, fmt.Errorf("workout: purge rider (%s): %w", stmt, err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("workout: purge rider: %w", err)
	}
	return total, nil
}

// WeekScheduled reports whether goalID's plan week starting weekStart (a
// Monday, "YYYY-MM-DD") has been filled before.
func (d *DB) WeekScheduled(ctx context.Context, goalID, weekStart string) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx, d.query(
		`SELECT COUNT(*) FROM scheduled_weeks WHERE goal_id = ? AND week_start = ?`),
		goalID, weekStart).Scan(&n)
	return n > 0, err
}

// MarkWeekScheduled records that a week has been filled. Idempotent.
func (d *DB) MarkWeekScheduled(ctx context.Context, goalID, weekStart string) error {
	_, err := d.db.ExecContext(ctx, d.query(`
INSERT INTO scheduled_weeks (goal_id, week_start, created_at) VALUES (?, ?, ?)
ON CONFLICT (goal_id, week_start) DO NOTHING`),
		goalID, weekStart, time.Now().UTC().Format(time.RFC3339))
	return err
}

// ScheduledWeeks lists goalID's recorded weeks: week_start (a Monday) to
// whether that week has been refreshed for roll-over (see MarkWeekRefreshed).
// One query for the whole season, so a pass over 27 weeks does not ask 27
// times.
func (d *DB) ScheduledWeeks(ctx context.Context, goalID string) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, d.query(
		`SELECT week_start, refreshed_at FROM scheduled_weeks WHERE goal_id = ?`), goalID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var week, refreshed string
		if err := rows.Scan(&week, &refreshed); err != nil {
			return nil, err
		}
		out[week] = refreshed != ""
	}
	return out, rows.Err()
}

// DeleteScheduledWeeksAfter forgets goalID's recorded weeks that start after
// lastWeekStart (a Monday): the weeks a plan that now ends earlier no longer
// has.
func (d *DB) DeleteScheduledWeeksAfter(ctx context.Context, goalID, lastWeekStart string) error {
	_, err := d.db.ExecContext(ctx, d.query(
		`DELETE FROM scheduled_weeks WHERE goal_id = ? AND week_start > ?`), goalID, lastWeekStart)
	return err
}

// MarkWeekRefreshed records that a recorded week's untouched sessions have
// been rebuilt from the plan and levels as they stand now (or were built from
// them in the first place), so it is not done again. It never records a week
// that was not filled: an unrecorded week has nothing to refresh, and the
// refresh must not be what makes the fill think the week is done.
func (d *DB) MarkWeekRefreshed(ctx context.Context, goalID, weekStart string) error {
	_, err := d.db.ExecContext(ctx, d.query(
		`UPDATE scheduled_weeks SET refreshed_at = ? WHERE goal_id = ? AND week_start = ? AND refreshed_at = ''`),
		time.Now().UTC().Format(time.RFC3339), goalID, weekStart)
	return err
}

// addScheduledWeekRefreshColumn adds refreshed_at to a scheduled_weeks table
// that predates it. Rows that already exist were all filled when their week
// was this week or next (that was all the tick filled), so they are stamped
// as refreshed at the moment the column appears — otherwise the first start
// after this ships would rebuild every rider's current sessions. Stamped only
// when the ALTER actually adds the column, never on a later start, or a week
// filled months ahead would be marked refreshed before it ever was.
func (d *DB) addScheduledWeekRefreshColumn() error {
	_, err := d.db.Exec(`ALTER TABLE scheduled_weeks ADD COLUMN refreshed_at TEXT NOT NULL DEFAULT ''`)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
			return nil
		}
		return err
	}
	_, err = d.db.Exec(`UPDATE scheduled_weeks SET refreshed_at = created_at`)
	return err
}

// addPushOriginColumn adds workout_pushes.origin. A push made before the
// column existed carries no record of who made it, so the migration decides
// once, when the column appears: a plan-made workout (it has a goal) of a
// rider who has auto-push on was almost certainly put there by the automatic
// pass, which sent every plan-made workout of the next fortnight; everything
// else (a hand-built workout, or any push by a rider who never opted in) can
// only have come from the rider pressing the button. Guessing "auto" for the
// first group is what lets the fortnight already sitting on a rider's device
// be cleared down to today; the cost is that a plan-made workout such a rider
// also sent by hand loses that protection and would have to be sent again.
// Stamped only when the ALTER adds the column, never on a later start.
func (d *DB) addPushOriginColumn() error {
	_, err := d.db.Exec(`ALTER TABLE workout_pushes ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual'`)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
			return nil
		}
		return err
	}
	_, err = d.db.Exec(d.query(`
        UPDATE workout_pushes SET origin = 'auto'
        WHERE workout_id IN (
            SELECT w.id FROM workouts w
            WHERE w.goal_id <> '' AND w.rider IN (SELECT rider FROM rider_profiles WHERE auto_push_workouts = ?)
        )`), true)
	return err
}

// DB stores goals, rider profiles and workouts as rows. The one
// implementation, no separate interface — the same choice internal/crew and
// internal/schedule make for their own stores (unlike source.Library, which
// is an interface specifically so acceptance tests can substitute fake
// provider push; nothing here needs that). Acceptance tests use a real,
// temporary SQLite database the same way they already do for Crew and
// Schedule.
type DB struct {
	db      *sql.DB
	dialect dbx.Dialect
}

// UseDB migrates this package's tables onto an already-open connection and
// wraps it — the same shape crew.UseDB and schedule.UseDB use, sharing
// source's own *sql.DB rather than opening a second connection pool to the
// same database. A goal or a workout has no data dependency on which routes
// exist (unlike sync state, which is meaningless without the routes it
// tracks), but there is still nothing to gain from a second pool: one
// process, one database, one connection to it.
func UseDB(db *sql.DB, dsn string) (*DB, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema(d)); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	store := &DB{db: db, dialect: d}
	if err := store.addEstimatedColumns(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	if err := store.addZoneLevelColumns(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	if err := store.addAnalysisFeelColumns(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	if err := store.addThresholdColumns(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	if err := store.addThresholdSuggestionDirectionColumn(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	if err := store.addScheduledWeekRefreshColumn(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	if err := store.addPushOriginColumn(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	if err := store.backfillProgressionHistory(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	if err := store.rescoreFTPTestAnalyses(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	if err := store.backfillFTPVerified(); err != nil {
		return nil, fmt.Errorf("migrate workout tables: %w", err)
	}
	return store, nil
}

// rescoreFTPTestAnalyses brings rides analysed against an FTP test before the
// analysis learned what a test is into line with it: outcome completed, no
// step results. A ramp is ridden to failure, so scored like a session it read
// "struggled" with "0 of 1 efforts on target", and the adapter took that as
// fatigue. Idempotent: it only touches rows still in the old shape.
func (d *DB) rescoreFTPTestAnalyses() error {
	_, err := d.db.Exec(d.query(`
UPDATE session_analyses SET outcome = ?, steps = ''
WHERE workout_id IN (SELECT id FROM workouts WHERE test_protocol <> '')
  AND (outcome IN (?, ?) OR steps NOT IN ('', 'null', '[]'))`),
		"completed", "struggled", "incomplete")
	return err
}

// backfillFTPVerified dates every FTP already on file to today, only where
// no date exists. Without it every existing rider would read as "FTP never
// checked" and be nagged to test the moment this ships. It is safe to run on
// every start: once a profile has an FTP it always has a date (SaveProfile
// sets one when FTP changes), so it only ever finds the rows that predate
// the column.
func (d *DB) backfillFTPVerified() error {
	_, err := d.db.Exec(d.query(
		`UPDATE rider_profiles SET ftp_verified_at = ? WHERE ftp_verified_at = '' AND ftp_watts > 0`),
		time.Now().UTC().Format("2006-01-02"))
	return err
}

// addEstimatedColumns adds the columns recording which profile values were
// filled in automatically to a rider_profiles table that predates them —
// CREATE TABLE IF NOT EXISTS above is a no-op against a table that already
// exists, so a genuinely new column needs its own step, the same pattern
// internal/crew's addAutoShareColumn already uses for exactly this.
// Defaulting to "not estimated" is correct for every pre-existing row: any
// value already on file before these columns existed was rider-entered
// through the only form that existed.
func (d *DB) addEstimatedColumns() error {
	for _, stmt := range []string{
		fmt.Sprintf(`ALTER TABLE rider_profiles ADD COLUMN ftp_estimated %s NOT NULL DEFAULT FALSE`, d.dialect.Boolean),
		`ALTER TABLE rider_profiles ADD COLUMN estimated_fields TEXT NOT NULL DEFAULT ''`,
		fmt.Sprintf(`ALTER TABLE rider_profiles ADD COLUMN auto_push_workouts %s NOT NULL DEFAULT FALSE`, d.dialect.Boolean),
		`ALTER TABLE rider_profiles ADD COLUMN ftp_levels_calibrated_watts DOUBLE PRECISION NOT NULL DEFAULT 0`,
		`ALTER TABLE rider_profiles ADD COLUMN threshold_hr INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE rider_profiles ADD COLUMN ftp_verified_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rider_profiles ADD COLUMN ftp_test_snoozed_until TEXT NOT NULL DEFAULT ''`,
		fmt.Sprintf(`ALTER TABLE rider_profiles ADD COLUMN smart_trainer %s NOT NULL DEFAULT FALSE`, d.dialect.Boolean),
	} {
		_, err := d.db.Exec(stmt)
		if err == nil {
			continue
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
			continue
		}
		return err
	}
	return nil
}

// addZoneLevelColumns adds zone/level to a workouts table that predates
// them — the same "table exists, column doesn't" situation
// addEstimatedColumns already handles for rider_profiles, using the same
// tolerant-of-either-engine's-already-exists-wording approach. Defaulting
// to ""/0 is correct for every pre-existing row: scheduler.IsKeySession and
// IsHardSession fall back to the legacy name tables for exactly that case.
func (d *DB) addZoneLevelColumns() error {
	for _, stmt := range []string{
		`ALTER TABLE workouts ADD COLUMN zone TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE workouts ADD COLUMN level DOUBLE PRECISION NOT NULL DEFAULT 0`,
		`ALTER TABLE workouts ADD COLUMN test_protocol TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE workouts ADD COLUMN test_result_watts DOUBLE PRECISION NOT NULL DEFAULT 0`,
		fmt.Sprintf(`ALTER TABLE workouts ADD COLUMN indoor %s NOT NULL DEFAULT FALSE`, d.dialect.Boolean),
		// The steps before the workout became indoor; NULL unless it is.
		`ALTER TABLE workouts ADD COLUMN outdoor_steps TEXT`,
		// The session as the plan made it, JSON, before the first swap for an
		// alternate; NULL until then.
		`ALTER TABLE workouts ADD COLUMN planned_snapshot TEXT`,
	} {
		_, err := d.db.Exec(stmt)
		if err == nil {
			continue
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
			continue
		}
		return err
	}
	return nil
}

// addAnalysisFeelColumns adds feel/level_delta to a session_analyses table
// that predates them — the same "table exists, column doesn't" situation
// addZoneLevelColumns already handles for workouts. Defaulting to 0/0 is
// correct for every pre-existing row: a ride analysed before feel ratings
// existed was never rated and never moved a level under this scheme
// (progression levels themselves ship in this same change).
func (d *DB) addAnalysisFeelColumns() error {
	for _, stmt := range []string{
		`ALTER TABLE session_analyses ADD COLUMN feel INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE session_analyses ADD COLUMN level_delta DOUBLE PRECISION NOT NULL DEFAULT 0`,
		// The post-ride survey's two optional answers next to feel: how the
		// legs were and how the rest of life was. '' is unanswered.
		`ALTER TABLE session_analyses ADD COLUMN legs TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE session_analyses ADD COLUMN stress TEXT NOT NULL DEFAULT ''`,
	} {
		_, err := d.db.Exec(stmt)
		if err == nil {
			continue
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
			continue
		}
		return err
	}
	return nil
}

// addThresholdColumns adds max_hr/best_hr_1200/best_speed_1200/best_speed_1800 to a
// session_analyses table that predates them — the same "table exists,
// column doesn't" situation addAnalysisFeelColumns already handles.
// Defaulting to 0 is correct for every pre-existing row: a ride analysed
// before threshold detection existed never had these computed, and 0 is
// exactly what rideanalysis reports for a ride with no HR/speed data, so a
// re-analysis (the same 42-day window this feature already sweeps) is what
// backfills them rather than a data migration.
func (d *DB) addThresholdColumns() error {
	for _, stmt := range []string{
		`ALTER TABLE session_analyses ADD COLUMN max_hr INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE session_analyses ADD COLUMN best_hr_1200 INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE session_analyses ADD COLUMN best_speed_1200 DOUBLE PRECISION NOT NULL DEFAULT 0`,
		`ALTER TABLE session_analyses ADD COLUMN best_speed_1800 DOUBLE PRECISION NOT NULL DEFAULT 0`,
	} {
		_, err := d.db.Exec(stmt)
		if err == nil {
			continue
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
			continue
		}
		return err
	}
	return nil
}

// addThresholdSuggestionDirectionColumn adds direction to a
// threshold_suggestions table that predates it — the same "table exists,
// column doesn't" situation addThresholdColumns already handles. Defaulting
// to 'up' is correct for the near-totality of pre-existing rows (Auto
// findings, which are always up-direction, were never stored here at all;
// every stored suggestion up to this point came overwhelmingly from an up
// finding), and safe even for the rare pre-migration down suggestion: it is
// only briefly mis-scoped for the dismissed-suggestion gate, corrected the
// next time that field produces a fresh finding.
func (d *DB) addThresholdSuggestionDirectionColumn() error {
	_, err := d.db.Exec(`ALTER TABLE threshold_suggestions ADD COLUMN direction TEXT NOT NULL DEFAULT 'up'`)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
}

func (d *DB) query(q string) string { return d.dialect.Rebind(q) }

// --- Goals ---

func (d *DB) ListGoals(ctx context.Context, rider string) ([]Goal, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT id, rider, name, sport, event_date, priority,
               target_distance_m, target_elevation_m, notes, created_at, updated_at
        FROM goals WHERE rider = ? ORDER BY event_date, name`), normalizeRider(rider))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var goals []Goal
	for rows.Next() {
		var (
			g        Goal
			sport    string
			priority string
		)
		if err := rows.Scan(&g.ID, &g.Rider, &g.Name, &sport, &g.EventDate, &priority,
			&g.TargetDistanceM, &g.TargetElevationM, &g.Notes, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		g.Sport = model.Sport(sport)
		g.Priority = Priority(priority)
		goals = append(goals, g)
	}
	return goals, rows.Err()
}

// ListAllGoals returns every goal, across every rider — the input the
// automated weekly scheduler (see api.AutoScheduleTick) needs. Dated goals
// come first, soonest event first, and goals with no date (general fitness,
// see periodization.BuildRollingPlan) last: when a rider has both, the
// scheduler must reach the one with a real deadline before the one that
// merely fills the gaps, because whichever schedules a given day first
// claims it.
func (d *DB) ListAllGoals(ctx context.Context) ([]Goal, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT id, rider, name, sport, event_date, priority,
               target_distance_m, target_elevation_m, notes, created_at, updated_at
        FROM goals ORDER BY (event_date = ''), event_date, name`))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var goals []Goal
	for rows.Next() {
		var (
			g        Goal
			sport    string
			priority string
		)
		if err := rows.Scan(&g.ID, &g.Rider, &g.Name, &sport, &g.EventDate, &priority,
			&g.TargetDistanceM, &g.TargetElevationM, &g.Notes, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		g.Sport = model.Sport(sport)
		g.Priority = Priority(priority)
		goals = append(goals, g)
	}
	return goals, rows.Err()
}

func (d *DB) GetGoal(ctx context.Context, id string) (Goal, error) {
	var (
		g        Goal
		sport    string
		priority string
	)
	err := d.db.QueryRowContext(ctx, d.query(`
        SELECT id, rider, name, sport, event_date, priority,
               target_distance_m, target_elevation_m, notes, created_at, updated_at
        FROM goals WHERE id = ?`), id).Scan(
		&g.ID, &g.Rider, &g.Name, &sport, &g.EventDate, &priority,
		&g.TargetDistanceM, &g.TargetElevationM, &g.Notes, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Goal{}, ErrGoalNotFound
	}
	if err != nil {
		return Goal{}, err
	}
	g.Sport = model.Sport(sport)
	g.Priority = Priority(priority)
	return g, nil
}

func (d *DB) CreateGoal(ctx context.Context, req CreateGoalRequest) (Goal, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Goal{}, errors.New("workout: goal name is required")
	}
	rider := normalizeRider(req.Rider)
	if rider == "" {
		return Goal{}, errors.New("workout: no rider — who is this goal for?")
	}

	sport := req.Sport
	if sport == "" {
		sport = model.SportCycling
	}
	priority := req.Priority
	if priority == "" {
		priority = PriorityB
	}

	id, err := d.uniqueID(ctx, "goals", slugify(name))
	if err != nil {
		return Goal{}, err
	}

	ts := timestamp()
	_, err = d.db.ExecContext(ctx, d.query(`
        INSERT INTO goals (id, rider, name, sport, event_date, priority,
                            target_distance_m, target_elevation_m, notes, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, rider, name, string(sport), req.EventDate, string(priority),
		req.TargetDistanceM, req.TargetElevationM, req.Notes, ts, ts)
	if err != nil {
		return Goal{}, err
	}
	return d.GetGoal(ctx, id)
}

func (d *DB) UpdateGoal(ctx context.Context, id string, req UpdateGoalRequest) (Goal, error) {
	current, err := d.GetGoal(ctx, id)
	if err != nil {
		return Goal{}, err
	}

	if req.Name != nil {
		current.Name = strings.TrimSpace(*req.Name)
	}
	if req.Sport != nil {
		current.Sport = *req.Sport
	}
	if req.EventDate != nil {
		current.EventDate = *req.EventDate
	}
	if req.Priority != nil {
		current.Priority = *req.Priority
	}
	if req.TargetDistanceM != nil {
		current.TargetDistanceM = *req.TargetDistanceM
	}
	if req.TargetElevationM != nil {
		current.TargetElevationM = *req.TargetElevationM
	}
	if req.Notes != nil {
		current.Notes = *req.Notes
	}

	_, err = d.db.ExecContext(ctx, d.query(`
        UPDATE goals SET name=?, sport=?, event_date=?, priority=?,
               target_distance_m=?, target_elevation_m=?, notes=?, updated_at=?
        WHERE id=?`),
		current.Name, string(current.Sport), current.EventDate, string(current.Priority),
		current.TargetDistanceM, current.TargetElevationM, current.Notes, timestamp(), id)
	if err != nil {
		return Goal{}, err
	}
	return d.GetGoal(ctx, id)
}

func (d *DB) DeleteGoal(ctx context.Context, id string) error {
	result, err := d.db.ExecContext(ctx, d.query(`DELETE FROM goals WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrGoalNotFound
	}
	// A workout that pointed at this goal is not deleted with it — a
	// planned session stays real even if the race it was built for falls
	// through — just unlinked, the same "make it orphaned, not gone"
	// choice routes make when the crew they were shared to disappears.
	if _, err = d.db.ExecContext(ctx, d.query(`UPDATE workouts SET goal_id = '' WHERE goal_id = ?`), id); err != nil {
		return err
	}
	// Its filled-week markers go too: they mean nothing without the goal, and
	// a later goal that reuses the id must start with every week unfilled.
	_, err = d.db.ExecContext(ctx, d.query(`DELETE FROM scheduled_weeks WHERE goal_id = ?`), id)
	return err
}

// --- Rider profile ---

func (d *DB) GetProfile(ctx context.Context, rider string) (RiderProfile, bool, error) {
	var (
		p         RiderProfile
		days      string
		estimated string
	)
	err := d.db.QueryRowContext(ctx, d.query(`
        SELECT rider, ftp_watts, ftp_estimated, threshold_pace_sec_per_km, max_hr, threshold_hr, resting_hr,
               available_days, hours_per_available_day, experience_level, estimated_fields,
               auto_push_workouts, ftp_levels_calibrated_watts, ftp_verified_at, ftp_test_snoozed_until, smart_trainer, updated_at
        FROM rider_profiles WHERE rider = ?`), normalizeRider(rider)).Scan(
		&p.Rider, &p.FTPWatts, &p.FTPEstimated, &p.ThresholdPaceSecPerKM, &p.MaxHR, &p.ThresholdHR, &p.RestingHR,
		&days, &p.HoursPerAvailableDay, &p.ExperienceLevel, &estimated, &p.AutoPushWorkouts, &p.FTPLevelsCalibratedAt,
		&p.FTPVerifiedAt, &p.FTPTestSnoozedUntil, &p.SmartTrainer, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RiderProfile{}, false, nil
	}
	if err != nil {
		return RiderProfile{}, false, err
	}
	p.AvailableDays = splitList(days)
	p.Estimated = splitList(estimated)
	return p, true, nil
}

// SetFTPCalibrated is a compare-and-set on the FTP the rider's levels were
// last calibrated against: it writes to only if the stored value is still
// from, and reports whether it did. SaveProfile deliberately never writes
// this column — a whole-row save carrying a marker loaded earlier could
// revert one a concurrent save had just moved, and that is what lets two
// racing saves of the same FTP rise both lower the levels. Routing every
// marker write through here means exactly one of them wins. A missing rider
// is a clean loss, not an error.
func (d *DB) SetFTPCalibrated(ctx context.Context, rider string, from, to float64) (bool, error) {
	result, err := d.db.ExecContext(ctx, d.query(`
        UPDATE rider_profiles SET ftp_levels_calibrated_watts = ?
        WHERE rider = ? AND ftp_levels_calibrated_watts = ?`), to, normalizeRider(rider), from)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// SaveProfile upserts — a rider has exactly one profile, and setting it up
// or editing it later are the same write, the same shape
// internal/settings's own encrypted config rows use for a single
// deployment-wide value.
func (d *DB) SaveProfile(ctx context.Context, profile RiderProfile) (RiderProfile, error) {
	rider := normalizeRider(profile.Rider)
	if rider == "" {
		return RiderProfile{}, errors.New("workout: no rider — whose profile is this?")
	}
	profile.Rider = rider
	profile.UpdatedAt = timestamp()

	// Whether this save changes FTP is decided against what is stored now,
	// before the upsert overwrites it.
	previous, _, err := d.GetProfile(ctx, rider)
	if err != nil {
		return RiderProfile{}, err
	}

	// ON CONFLICT ... DO UPDATE works on both engines without a dialect
	// branch — the same upsert shape internal/settings, internal/blocklist,
	// internal/providerlink and internal/state's own sync_state table
	// already rely on.
	_, err = d.db.ExecContext(ctx, d.query(`
        INSERT INTO rider_profiles (rider, ftp_watts, ftp_estimated, threshold_pace_sec_per_km, max_hr,
                    threshold_hr, resting_hr, available_days, hours_per_available_day, experience_level, estimated_fields,
                    auto_push_workouts, smart_trainer, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (rider) DO UPDATE SET
            ftp_watts = excluded.ftp_watts, ftp_estimated = excluded.ftp_estimated,
            threshold_pace_sec_per_km = excluded.threshold_pace_sec_per_km,
            max_hr = excluded.max_hr, threshold_hr = excluded.threshold_hr, resting_hr = excluded.resting_hr,
            available_days = excluded.available_days, hours_per_available_day = excluded.hours_per_available_day,
            experience_level = excluded.experience_level, estimated_fields = excluded.estimated_fields,
            auto_push_workouts = excluded.auto_push_workouts, smart_trainer = excluded.smart_trainer,
            updated_at = excluded.updated_at`),
		profile.Rider, profile.FTPWatts, profile.FTPEstimated, profile.ThresholdPaceSecPerKM, profile.MaxHR, profile.ThresholdHR, profile.RestingHR,
		joinList(profile.AvailableDays), profile.HoursPerAvailableDay, profile.ExperienceLevel,
		joinList(profile.Estimated), profile.AutoPushWorkouts, profile.SmartTrainer, profile.UpdatedAt)
	if err != nil {
		return RiderProfile{}, err
	}

	// Any change to FTP, by whatever path (the form, an accepted suggestion, a
	// sync's auto-fill), is a verification: someone or something just
	// established the number. ftp_verified_at is not part of the upsert above
	// so an old copy of the profile cannot overwrite a newer date.
	if profile.FTPWatts > 0 && profile.FTPWatts != previous.FTPWatts {
		if err := d.MarkFTPVerified(ctx, rider, time.Now().UTC().Format("2006-01-02")); err != nil {
			return RiderProfile{}, err
		}
	}

	saved, _, err := d.GetProfile(ctx, rider)
	return saved, err
}

// MarkFTPVerified moves ftp_verified_at forward to date. It never moves it
// back (ISO dates compare correctly as strings, and "" is before all of them),
// so a test, a confirming ride and a changed FTP can arrive in any order and
// the latest wins. A rider with no profile row is a no-op: there is no FTP to
// have verified.
func (d *DB) MarkFTPVerified(ctx context.Context, rider, date string) error {
	_, err := d.db.ExecContext(ctx, d.query(`
        UPDATE rider_profiles SET ftp_verified_at = ? WHERE rider = ? AND ftp_verified_at < ?`),
		date, normalizeRider(rider), date)
	return err
}

// SnoozeFTPTest silences the FTP test suggestion until date. Its own writer,
// like the calibration marker, so a whole-profile save can never revert it.
// It creates the profile row when there is none: a rider with power rides but
// no saved profile is offered a "set your FTP" test, and dismissing it has to
// stick.
func (d *DB) SnoozeFTPTest(ctx context.Context, rider, until string) error {
	_, err := d.db.ExecContext(ctx, d.query(`
        INSERT INTO rider_profiles (rider, ftp_test_snoozed_until, updated_at) VALUES (?, ?, ?)
        ON CONFLICT (rider) DO UPDATE SET ftp_test_snoozed_until = excluded.ftp_test_snoozed_until`),
		normalizeRider(rider), until, timestamp())
	return err
}

// SetTestResult records the FTP a test ride measured on its workout, once. It
// reports whether it wrote: false means a result was already there, which is
// what makes a repeated sync a no-op instead of a second toast and a second
// suggestion.
func (d *DB) SetTestResult(ctx context.Context, workoutID string, watts float64) (bool, error) {
	result, err := d.db.ExecContext(ctx, d.query(`
        UPDATE workouts SET test_result_watts = ? WHERE id = ? AND test_result_watts = 0`), watts, workoutID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// --- Workouts ---

func (d *DB) ListWorkouts(ctx context.Context, rider string) ([]Workout, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT id, rider, sport, name, goal_id, date, description, steps, zone, level, test_protocol, test_result_watts, indoor, outdoor_steps, planned_snapshot, created_at, updated_at
        FROM workouts WHERE rider = ? ORDER BY date, name`), normalizeRider(rider))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var workouts []Workout
	for rows.Next() {
		w, err := scanWorkout(rows)
		if err != nil {
			return nil, err
		}
		workouts = append(workouts, w)
	}
	return workouts, rows.Err()
}

func (d *DB) GetWorkout(ctx context.Context, id string) (Workout, error) {
	row := d.db.QueryRowContext(ctx, d.query(`
        SELECT id, rider, sport, name, goal_id, date, description, steps, zone, level, test_protocol, test_result_watts, indoor, outdoor_steps, planned_snapshot, created_at, updated_at
        FROM workouts WHERE id = ?`), id)
	w, err := scanWorkout(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Workout{}, ErrWorkoutNotFound
	}
	return w, err
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, so scanWorkout
// serves GetWorkout and ListWorkouts without duplicating the column list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanWorkout(row rowScanner) (Workout, error) {
	var (
		w       Workout
		sport   string
		steps   []byte
		zone    string
		outdoor sql.NullString
		planned sql.NullString
	)
	if err := row.Scan(&w.ID, &w.Rider, &sport, &w.Name, &w.GoalID, &w.Date, &w.Description,
		&steps, &zone, &w.Level, &w.TestProtocol, &w.TestResultWatts, &w.Indoor, &outdoor, &planned, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return Workout{}, err
	}
	w.Sport = model.Sport(sport)
	w.Zone = Zone(zone)
	if len(steps) > 0 {
		if err := json.Unmarshal(steps, &w.Steps); err != nil {
			return Workout{}, fmt.Errorf("workout: decode steps for %s: %w", w.ID, err)
		}
	}
	if outdoor.Valid {
		original := []WorkoutStep{}
		if err := json.Unmarshal([]byte(outdoor.String), &original); err != nil {
			return Workout{}, fmt.Errorf("workout: decode outdoor steps for %s: %w", w.ID, err)
		}
		if original == nil {
			original = []WorkoutStep{}
		}
		w.OutdoorSteps = &original
	}
	if planned.Valid {
		var snap PlannedSnapshot
		if err := json.Unmarshal([]byte(planned.String), &snap); err != nil {
			return Workout{}, fmt.Errorf("workout: decode planned snapshot for %s: %w", w.ID, err)
		}
		w.PlannedSnapshot = &snap
	}
	return w, nil
}

func (d *DB) CreateWorkout(ctx context.Context, req CreateWorkoutRequest) (Workout, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Workout{}, errors.New("workout: name is required")
	}
	rider := normalizeRider(req.Rider)
	if rider == "" {
		return Workout{}, errors.New("workout: no rider — who is this workout for?")
	}
	if err := validateSteps(req.Steps); err != nil {
		return Workout{}, err
	}

	sport := req.Sport
	if sport == "" {
		sport = model.SportCycling
	}

	steps, err := json.Marshal(req.Steps)
	if err != nil {
		return Workout{}, fmt.Errorf("workout: encode steps: %w", err)
	}

	id, err := d.uniqueID(ctx, "workouts", slugify(name))
	if err != nil {
		return Workout{}, err
	}

	ts := timestamp()
	_, err = d.db.ExecContext(ctx, d.query(`
        INSERT INTO workouts (id, rider, sport, name, goal_id, date, description, steps, zone, level, test_protocol, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, rider, string(sport), name, req.GoalID, req.Date, req.Description, steps, string(req.Zone), req.Level, req.TestProtocol, ts, ts)
	if err != nil {
		return Workout{}, err
	}
	return d.GetWorkout(ctx, id)
}

func (d *DB) UpdateWorkout(ctx context.Context, id string, req UpdateWorkoutRequest) (Workout, error) {
	current, err := d.GetWorkout(ctx, id)
	if err != nil {
		return Workout{}, err
	}

	if req.Sport != nil {
		current.Sport = *req.Sport
	}
	if req.Name != nil {
		current.Name = strings.TrimSpace(*req.Name)
	}
	if req.GoalID != nil {
		current.GoalID = *req.GoalID
	}
	if req.Date != nil {
		current.Date = *req.Date
	}
	if req.Description != nil {
		current.Description = *req.Description
	}
	if req.Steps != nil {
		if err := validateSteps(*req.Steps); err != nil {
			return Workout{}, err
		}
		current.Steps = *req.Steps
	}
	if req.Zone != nil {
		current.Zone = *req.Zone
	}
	if req.Level != nil {
		current.Level = *req.Level
	}
	if req.Indoor != nil {
		current.Indoor = *req.Indoor
	}
	if req.OutdoorSteps != nil {
		if *req.OutdoorSteps == nil {
			current.OutdoorSteps = nil
		} else {
			if err := validateSteps(*req.OutdoorSteps); err != nil {
				return Workout{}, err
			}
			original := *req.OutdoorSteps
			current.OutdoorSteps = &original
		}
	}

	switch {
	case req.ClearPlannedSnapshot:
		current.PlannedSnapshot = nil
	case req.PlannedSnapshot != nil && current.PlannedSnapshot == nil:
		snap := *req.PlannedSnapshot
		current.PlannedSnapshot = &snap
	}

	steps, err := json.Marshal(current.Steps)
	if err != nil {
		return Workout{}, fmt.Errorf("workout: encode steps: %w", err)
	}
	// NULL, not "null": nothing stored must stay distinguishable from a stored
	// original that happens to have no steps.
	var outdoor any
	if current.OutdoorSteps != nil {
		encoded, err := json.Marshal(*current.OutdoorSteps)
		if err != nil {
			return Workout{}, fmt.Errorf("workout: encode outdoor steps: %w", err)
		}
		outdoor = string(encoded)
	}
	var planned any
	if current.PlannedSnapshot != nil {
		encoded, err := json.Marshal(current.PlannedSnapshot)
		if err != nil {
			return Workout{}, fmt.Errorf("workout: encode planned snapshot: %w", err)
		}
		planned = string(encoded)
	}

	_, err = d.db.ExecContext(ctx, d.query(`
        UPDATE workouts SET sport=?, name=?, goal_id=?, date=?, description=?, steps=?, zone=?, level=?,
               indoor=?, outdoor_steps=?, planned_snapshot=?, updated_at=?
        WHERE id=?`),
		string(current.Sport), current.Name, current.GoalID, current.Date, current.Description,
		steps, string(current.Zone), current.Level, current.Indoor, outdoor, planned, timestamp(), id)
	if err != nil {
		return Workout{}, err
	}
	return d.GetWorkout(ctx, id)
}

func (d *DB) DeleteWorkout(ctx context.Context, id string) error {
	result, err := d.db.ExecContext(ctx, d.query(`DELETE FROM workouts WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrWorkoutNotFound
	}
	// The push record describes a copy on a provider's account; with the
	// workout gone there is nothing left for it to describe. (Removing that
	// copy is the API layer's job, which has the session to do it with — by
	// this point it has already read what it needs.)
	if _, err = d.db.ExecContext(ctx, d.query(`DELETE FROM workout_pushes WHERE workout_id = ?`), id); err != nil {
		return err
	}
	// A ride linked by hand to this workout goes back to the automatic match,
	// rather than pointing at nothing forever.
	if _, err = d.db.ExecContext(ctx, d.query(`DELETE FROM session_links WHERE workout_id = ?`), id); err != nil {
		return err
	}
	// Its reasons went with it: they explain a session that no longer exists.
	// The workout id is unique across riders, so this needs no rider filter.
	_, err = d.db.ExecContext(ctx, d.query(`DELETE FROM adjustments WHERE subject_kind = ? AND subject_id = ?`), SubjectWorkout, id)
	return err
}

// maxStepDepth stops a maliciously or accidentally self-nested repeat block
// (a step repeating itself) from recursing forever — a manual builder has
// no legitimate reason to nest repeat blocks more than a couple of levels
// deep (an interval set inside a bigger block is as far as any real
// workout goes).
const maxStepDepth = 5

func validateSteps(steps []WorkoutStep) error {
	return validateStepDepth(steps, 0)
}

func validateStepDepth(steps []WorkoutStep, depth int) error {
	if depth > maxStepDepth {
		return fmt.Errorf("workout: steps nested more than %d levels deep", maxStepDepth)
	}
	for _, s := range steps {
		if s.Repeat > 1 {
			if len(s.Steps) == 0 {
				return errors.New("workout: a repeat block needs at least one step")
			}
			if err := validateStepDepth(s.Steps, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// uniqueID appends -2, -3, … so two goals or workouts with the same name
// don't collide — the same shape source.uniqueSlug and crew.uniqueID both
// already use for their own tables; duplicated locally rather than shared
// across packages for the same reason crew.go gives for not reusing
// source's copy: three near-identical, single-table-scoped functions cost
// less to keep separate than the coupling a shared one would add.
func (d *DB) uniqueID(ctx context.Context, table, base string) (string, error) {
	if base == "" {
		base = "item"
	}
	candidate := base
	for attempt := 2; attempt < 1000; attempt++ {
		var exists int
		// #nosec G201 -- table is one of two fixed string literals this
		// package passes in, never external input.
		q := fmt.Sprintf(`SELECT COUNT(1) FROM %s WHERE id = ?`, table)
		if err := d.db.QueryRowContext(ctx, d.query(q), candidate).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, attempt)
	}
	return "", fmt.Errorf("workout: could not find a free id for %q", base)
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339) }

func normalizeRider(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func joinList(items []string) string { return strings.Join(items, ",") }
