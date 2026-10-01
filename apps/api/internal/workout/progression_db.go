package workout

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

// ProgressionLevel is one rider's current level in one sport/zone, as
// internal/progression computes it. This package deliberately does not
// import progression — the store stays dependency-free, the same reasoning
// SessionAnalysis's own doc comment gives for mirroring rideanalysis.Result
// by hand rather than importing it — so this is a plain value the API layer
// fills in from progression.Level and progression.Reason.
type ProgressionLevel struct {
	Rider     string
	Sport     model.Sport
	Zone      Zone
	Level     float64
	Reason    string
	UpdatedAt string
}

// ListLevels returns every level a rider has — across both sports, every
// structured zone that has ever been saved for them. Callers that need a
// rider's levels initialised (nothing saved yet) are expected to call
// progression.Initial and SaveLevel for each one first; ListLevels itself
// never invents rows.
func (d *DB) ListLevels(ctx context.Context, rider string) ([]ProgressionLevel, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT rider, sport, zone, level, reason, updated_at
        FROM progression_levels WHERE rider = ? ORDER BY sport, zone`), normalizeRider(rider))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var levels []ProgressionLevel
	for rows.Next() {
		l, err := scanLevel(rows)
		if err != nil {
			return nil, err
		}
		levels = append(levels, l)
	}
	return levels, rows.Err()
}

// SaveLevel upserts on (rider, sport, zone) — a level move replaces the
// previous value and reason for that zone rather than accumulating a
// history row, the same upsert shape SaveAnalysis already uses for
// session_analyses.
func (d *DB) SaveLevel(ctx context.Context, l ProgressionLevel) error {
	rider := normalizeRider(l.Rider)
	if rider == "" {
		return errors.New("workout: no rider — whose level is this?")
	}
	if l.Sport == "" {
		return errors.New("workout: a level needs a sport")
	}
	if l.Zone == "" {
		return errors.New("workout: a level needs a zone")
	}

	updatedAt := l.UpdatedAt
	if updatedAt == "" {
		updatedAt = timestamp()
	}

	// History first records a move, so a value saved again unchanged (a
	// re-rate that lands where it was) adds no point to the chart.
	var prev float64
	err := d.db.QueryRowContext(ctx, d.query(
		`SELECT level FROM progression_levels WHERE rider = ? AND sport = ? AND zone = ?`),
		rider, string(l.Sport), string(l.Zone)).Scan(&prev)
	switch {
	case errors.Is(err, sql.ErrNoRows) || (err == nil && prev != l.Level):
		if _, err := d.db.ExecContext(ctx, d.query(
			`INSERT INTO progression_history (rider, sport, zone, level, reason, at) VALUES (?, ?, ?, ?, ?, ?)`),
			rider, string(l.Sport), string(l.Zone), l.Level, l.Reason, updatedAt); err != nil {
			return err
		}
	case err != nil:
		return err
	}

	_, err = d.db.ExecContext(ctx, d.query(`
        INSERT INTO progression_levels (rider, sport, zone, level, reason, updated_at)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT (rider, sport, zone) DO UPDATE SET
            level = excluded.level, reason = excluded.reason, updated_at = excluded.updated_at`),
		rider, string(l.Sport), string(l.Zone), l.Level, l.Reason, updatedAt)
	return err
}

func scanLevel(row rowScanner) (ProgressionLevel, error) {
	var (
		l           ProgressionLevel
		sport, zone string
	)
	if err := row.Scan(&l.Rider, &sport, &zone, &l.Level, &l.Reason, &l.UpdatedAt); err != nil {
		return ProgressionLevel{}, err
	}
	l.Sport = model.Sport(sport)
	l.Zone = Zone(zone)
	return l, nil
}

// LevelPoint is one value a level held from At on.
type LevelPoint struct {
	Sport  model.Sport
	Zone   Zone
	Level  float64
	Reason string
	At     string
}

// LevelHistory returns the rider's level moves at or after since (RFC 3339 or
// a plain date), oldest first, plus for each sport/zone the last value held
// before since, so a chart over a window starts at the level the rider was
// on rather than at their first move inside it.
func (d *DB) LevelHistory(ctx context.Context, rider, since string) ([]LevelPoint, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT sport, zone, level, reason, at FROM progression_history
        WHERE rider = ? ORDER BY at, sport, zone`), normalizeRider(rider))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	type key struct{ sport, zone string }
	before := map[key]LevelPoint{}
	var out []LevelPoint
	for rows.Next() {
		var p LevelPoint
		var sport, zone string
		if err := rows.Scan(&sport, &zone, &p.Level, &p.Reason, &p.At); err != nil {
			return nil, err
		}
		p.Sport, p.Zone = model.Sport(sport), Zone(zone)
		if p.At < since {
			before[key{sport, zone}] = p
			continue
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, p := range before {
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out, nil
}

// backfillProgressionHistory gives levels saved before progression_history
// existed a history, once: for a zone with no history rows it walks that
// zone's analysed rides back from today's level, undoing each ride's own
// level_delta (the move the ride made, see SessionAnalysis.LevelDelta), so
// each ride's date gets the level it left the rider on. A starting point the
// day before the first such ride carries the level before it. Moves nothing
// recorded (an FTP recalibration) are not in the walk, so the reconstructed
// line is approximate before today; every move from now on is exact.
func (d *DB) backfillProgressionHistory() error {
	ctx := context.Background()
	levels, err := d.allLevels(ctx)
	if err != nil {
		return err
	}
	for _, l := range levels {
		var n int
		if err := d.db.QueryRow(d.query(
			`SELECT COUNT(*) FROM progression_history WHERE rider = ? AND sport = ? AND zone = ?`),
			l.Rider, string(l.Sport), string(l.Zone)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		rides, err := d.db.Query(d.query(`
            SELECT s.date, a.level_delta FROM session_analyses a
            JOIN completed_sessions s ON s.id = a.session_id
            JOIN workouts w ON w.id = a.workout_id
            WHERE a.rider = ? AND w.sport = ? AND w.zone = ? AND a.level_delta <> 0
            ORDER BY s.date DESC`), l.Rider, string(l.Sport), string(l.Zone))
		if err != nil {
			return err
		}
		type ride struct {
			date  string
			delta float64
		}
		var walk []ride
		for rides.Next() {
			var r ride
			if err := rides.Scan(&r.date, &r.delta); err != nil {
				_ = rides.Close()
				return err
			}
			walk = append(walk, r)
		}
		_ = rides.Close()

		// With no ride to walk, the one point is today's level from when it
		// was set. With rides, the newest ride's point carries today's level.
		var points []LevelPoint
		if len(walk) == 0 {
			points = append(points, LevelPoint{Level: l.Level, Reason: l.Reason, At: l.UpdatedAt})
		}
		level := l.Level
		for _, r := range walk {
			points = append(points, LevelPoint{Level: level, At: r.date + "T12:00:00Z"})
			level = clampLevel(level - r.delta)
		}
		if len(walk) > 0 {
			first, _ := time.Parse("2006-01-02", walk[len(walk)-1].date)
			points = append(points, LevelPoint{Level: level, At: first.AddDate(0, 0, -1).Format("2006-01-02") + "T12:00:00Z"})
		}
		for _, p := range points {
			if _, err := d.db.Exec(d.query(
				`INSERT INTO progression_history (rider, sport, zone, level, reason, at) VALUES (?, ?, ?, ?, ?, ?)`),
				l.Rider, string(l.Sport), string(l.Zone), p.Level, p.Reason, p.At); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *DB) allLevels(ctx context.Context) ([]ProgressionLevel, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT rider, sport, zone, level, reason, updated_at FROM progression_levels`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ProgressionLevel
	for rows.Next() {
		l, err := scanLevel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// clampLevel keeps a reconstructed value on the 1-10 ladder, one decimal,
// the same bounds progression.Apply holds a live move to.
func clampLevel(v float64) float64 {
	v = math.Round(v*10) / 10
	return math.Min(10, math.Max(1, v))
}
