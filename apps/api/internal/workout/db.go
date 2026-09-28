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
    threshold_pace_sec_per_km  DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_hr                     INTEGER NOT NULL DEFAULT 0,
    threshold_hr               INTEGER NOT NULL DEFAULT 0,
    resting_hr                 INTEGER NOT NULL DEFAULT 0,
    available_days             TEXT NOT NULL DEFAULT '',
    hours_per_available_day    DOUBLE PRECISION NOT NULL DEFAULT 0,
    experience_level           TEXT NOT NULL DEFAULT '',
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
    hr_zone_seconds      TEXT NOT NULL DEFAULT '',
    power_curve          TEXT NOT NULL DEFAULT '',
    steps                TEXT NOT NULL DEFAULT '',
    analysed_at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS session_analyses_rider_idx ON session_analyses (rider);

-- progression_levels holds each rider's current 1-10 level per sport/zone
-- (internal/progression computes the numbers; this table just stores the
-- result). One row per rider/sport/zone, upserted on every level change —
-- there is no history table, just the current value and the reason it last
-- moved (see SessionAnalysis.LevelDelta for how a re-rate finds and undoes
-- the specific change it is replacing).
CREATE TABLE IF NOT EXISTS progression_levels (
    rider      TEXT NOT NULL,
    sport      TEXT NOT NULL,
    zone       TEXT NOT NULL,
    level      DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason     TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (rider, sport, zone)
);

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
);`, d.Blob, d.Boolean)
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
	return store, nil
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
		`ALTER TABLE rider_profiles ADD COLUMN threshold_hr INTEGER NOT NULL DEFAULT 0`,
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
	_, err = d.db.ExecContext(ctx, d.query(`UPDATE workouts SET goal_id = '' WHERE goal_id = ?`), id)
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
               auto_push_workouts, updated_at
        FROM rider_profiles WHERE rider = ?`), normalizeRider(rider)).Scan(
		&p.Rider, &p.FTPWatts, &p.FTPEstimated, &p.ThresholdPaceSecPerKM, &p.MaxHR, &p.ThresholdHR, &p.RestingHR,
		&days, &p.HoursPerAvailableDay, &p.ExperienceLevel, &estimated, &p.AutoPushWorkouts, &p.UpdatedAt)
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

	// ON CONFLICT ... DO UPDATE works on both engines without a dialect
	// branch — the same upsert shape internal/settings, internal/blocklist,
	// internal/providerlink and internal/state's own sync_state table
	// already rely on.
	_, err := d.db.ExecContext(ctx, d.query(`
        INSERT INTO rider_profiles (rider, ftp_watts, ftp_estimated, threshold_pace_sec_per_km, max_hr,
                    threshold_hr, resting_hr, available_days, hours_per_available_day, experience_level, estimated_fields,
                    auto_push_workouts, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (rider) DO UPDATE SET
            ftp_watts = excluded.ftp_watts, ftp_estimated = excluded.ftp_estimated,
            threshold_pace_sec_per_km = excluded.threshold_pace_sec_per_km,
            max_hr = excluded.max_hr, threshold_hr = excluded.threshold_hr, resting_hr = excluded.resting_hr,
            available_days = excluded.available_days, hours_per_available_day = excluded.hours_per_available_day,
            experience_level = excluded.experience_level, estimated_fields = excluded.estimated_fields,
            auto_push_workouts = excluded.auto_push_workouts, updated_at = excluded.updated_at`),
		profile.Rider, profile.FTPWatts, profile.FTPEstimated, profile.ThresholdPaceSecPerKM, profile.MaxHR, profile.ThresholdHR, profile.RestingHR,
		joinList(profile.AvailableDays), profile.HoursPerAvailableDay, profile.ExperienceLevel,
		joinList(profile.Estimated), profile.AutoPushWorkouts, profile.UpdatedAt)
	if err != nil {
		return RiderProfile{}, err
	}

	saved, _, err := d.GetProfile(ctx, rider)
	return saved, err
}

// --- Workouts ---

func (d *DB) ListWorkouts(ctx context.Context, rider string) ([]Workout, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT id, rider, sport, name, goal_id, date, description, steps, zone, level, created_at, updated_at
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
        SELECT id, rider, sport, name, goal_id, date, description, steps, zone, level, created_at, updated_at
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
		w     Workout
		sport string
		steps []byte
		zone  string
	)
	if err := row.Scan(&w.ID, &w.Rider, &sport, &w.Name, &w.GoalID, &w.Date, &w.Description,
		&steps, &zone, &w.Level, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return Workout{}, err
	}
	w.Sport = model.Sport(sport)
	w.Zone = Zone(zone)
	if len(steps) > 0 {
		if err := json.Unmarshal(steps, &w.Steps); err != nil {
			return Workout{}, fmt.Errorf("workout: decode steps for %s: %w", w.ID, err)
		}
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
        INSERT INTO workouts (id, rider, sport, name, goal_id, date, description, steps, zone, level, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, rider, string(sport), name, req.GoalID, req.Date, req.Description, steps, string(req.Zone), req.Level, ts, ts)
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

	steps, err := json.Marshal(current.Steps)
	if err != nil {
		return Workout{}, fmt.Errorf("workout: encode steps: %w", err)
	}

	_, err = d.db.ExecContext(ctx, d.query(`
        UPDATE workouts SET sport=?, name=?, goal_id=?, date=?, description=?, steps=?, zone=?, level=?, updated_at=?
        WHERE id=?`),
		string(current.Sport), current.Name, current.GoalID, current.Date, current.Description,
		steps, string(current.Zone), current.Level, timestamp(), id)
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
	_, err = d.db.ExecContext(ctx, d.query(`DELETE FROM workout_pushes WHERE workout_id = ?`), id)
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
