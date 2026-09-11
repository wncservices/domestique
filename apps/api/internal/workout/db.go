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
    threshold_pace_sec_per_km  DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_hr                     INTEGER NOT NULL DEFAULT 0,
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
    steps        %s NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS workouts_rider_idx ON workouts (rider);
CREATE INDEX IF NOT EXISTS workouts_goal_idx ON workouts (goal_id);

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
);`, d.Blob)
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
	return &DB{db: db, dialect: d}, nil
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
		p    RiderProfile
		days string
	)
	err := d.db.QueryRowContext(ctx, d.query(`
        SELECT rider, ftp_watts, threshold_pace_sec_per_km, max_hr, resting_hr,
               available_days, hours_per_available_day, experience_level, updated_at
        FROM rider_profiles WHERE rider = ?`), normalizeRider(rider)).Scan(
		&p.Rider, &p.FTPWatts, &p.ThresholdPaceSecPerKM, &p.MaxHR, &p.RestingHR,
		&days, &p.HoursPerAvailableDay, &p.ExperienceLevel, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RiderProfile{}, false, nil
	}
	if err != nil {
		return RiderProfile{}, false, err
	}
	p.AvailableDays = splitList(days)
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
        INSERT INTO rider_profiles (rider, ftp_watts, threshold_pace_sec_per_km, max_hr,
                    resting_hr, available_days, hours_per_available_day, experience_level, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (rider) DO UPDATE SET
            ftp_watts = excluded.ftp_watts, threshold_pace_sec_per_km = excluded.threshold_pace_sec_per_km,
            max_hr = excluded.max_hr, resting_hr = excluded.resting_hr,
            available_days = excluded.available_days, hours_per_available_day = excluded.hours_per_available_day,
            experience_level = excluded.experience_level, updated_at = excluded.updated_at`),
		profile.Rider, profile.FTPWatts, profile.ThresholdPaceSecPerKM, profile.MaxHR, profile.RestingHR,
		joinList(profile.AvailableDays), profile.HoursPerAvailableDay, profile.ExperienceLevel, profile.UpdatedAt)
	if err != nil {
		return RiderProfile{}, err
	}

	saved, _, err := d.GetProfile(ctx, rider)
	return saved, err
}

// --- Workouts ---

func (d *DB) ListWorkouts(ctx context.Context, rider string) ([]Workout, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT id, rider, sport, name, goal_id, date, description, steps, created_at, updated_at
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
        SELECT id, rider, sport, name, goal_id, date, description, steps, created_at, updated_at
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
	)
	if err := row.Scan(&w.ID, &w.Rider, &sport, &w.Name, &w.GoalID, &w.Date, &w.Description,
		&steps, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return Workout{}, err
	}
	w.Sport = model.Sport(sport)
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
        INSERT INTO workouts (id, rider, sport, name, goal_id, date, description, steps, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, rider, string(sport), name, req.GoalID, req.Date, req.Description, steps, ts, ts)
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

	steps, err := json.Marshal(current.Steps)
	if err != nil {
		return Workout{}, fmt.Errorf("workout: encode steps: %w", err)
	}

	_, err = d.db.ExecContext(ctx, d.query(`
        UPDATE workouts SET sport=?, name=?, goal_id=?, date=?, description=?, steps=?, updated_at=?
        WHERE id=?`),
		string(current.Sport), current.Name, current.GoalID, current.Date, current.Description,
		steps, timestamp(), id)
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
	return nil
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
