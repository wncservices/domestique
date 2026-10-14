package workout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// bestEffortSchema is the best_efforts table. It is derived: one row per ride
// per metric the ride has a value for, rebuildable from session_analyses (see
// RebuildBestEfforts), so nothing here is the only copy of anything except the
// award, which records what was new when a ride was analysed and cannot be
// re-derived. Raw rows rather than a "current record" table: a season, the last
// 42 days and last year's curve all fall out of the same rows, and a merge of
// two riders keeps the better value per metric just by moving the rows.
//
// announced is false only for an award a sync has just made and the rider has
// not been told about yet; a rebuilt or imported row carries no award and is
// never announced, which is what keeps three years of imported history from
// toasting.
//
// The primary key is (session_id, metric), no rider in it: a session belongs to
// one rider, and a merge moves the rider column without any key colliding.
func bestEffortSchema(boolean string) string {
	return `
CREATE TABLE IF NOT EXISTS best_efforts (
    rider      TEXT NOT NULL,
    session_id TEXT NOT NULL,
    metric     TEXT NOT NULL,
    sport      TEXT NOT NULL,
    date       TEXT NOT NULL,
    value      DOUBLE PRECISION NOT NULL,
    award      TEXT NOT NULL DEFAULT '',
    previous   DOUBLE PRECISION NOT NULL DEFAULT 0,
    announced  ` + boolean + ` NOT NULL DEFAULT TRUE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (session_id, metric)
);
CREATE INDEX IF NOT EXISTS best_efforts_rider_metric_idx ON best_efforts (rider, metric, value);
CREATE INDEX IF NOT EXISTS best_efforts_rider_date_idx ON best_efforts (rider, date);`
}

// Award scopes, as stored. Mirrors internal/bestefforts, which this package
// cannot import.
const (
	AwardSeason  = "season"
	AwardAllTime = "all_time"
)

// BestEffort is one ride's value for one metric.
type BestEffort struct {
	Rider     string
	SessionID string
	Metric    string
	Sport     string
	// Date is the ride's local date, copied here for the index.
	Date  string
	Value float64
	// Award is "", AwardSeason or AwardAllTime: what the ride was when it was
	// first analysed. It is written once and never moved or removed.
	Award string
	// Previous is the best the award beat, for the toast ("268 W, was 262 W").
	Previous  float64
	Announced bool
	CreatedAt string
}

// EffortFunc turns one analysed ride into its efforts: metric to value. It is
// internal/bestefforts.Efforts; passed in because that package imports this one.
type EffortFunc func(a SessionAnalysis, sport string) map[string]float64

// UpsertEfforts records a ride's efforts. A metric the ride already has a row
// for has its value, date and sport updated and its award left alone (a
// re-analysis after an FTP change must not forget what the ride once was); a
// metric it no longer has is removed, unless it holds an award. It returns the
// metrics that were new, which is the only time an award may be decided.
func (d *DB) UpsertEfforts(ctx context.Context, rider, sessionID, sport, date string, efforts map[string]float64) (fresh []string, err error) {
	rider = normalizeRider(rider)
	if rider == "" || sessionID == "" {
		return nil, errors.New("workout: an effort needs a rider and a session")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	existing := map[string]bool{}
	rows, err := tx.QueryContext(ctx, d.query(`SELECT metric FROM best_efforts WHERE session_id = ?`), sessionID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			_ = rows.Close()
			return nil, err
		}
		existing[m] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	now := d.timestamp()
	for metric, value := range efforts {
		if existing[metric] {
			if _, err := tx.ExecContext(ctx, d.query(
				`UPDATE best_efforts SET rider = ?, sport = ?, date = ?, value = ? WHERE session_id = ? AND metric = ?`),
				rider, sport, date, value, sessionID, metric); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, d.query(`
            INSERT INTO best_efforts (rider, session_id, metric, sport, date, value, award, announced, created_at)
            VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`),
			rider, sessionID, metric, sport, date, value, true, now); err != nil {
			return nil, err
		}
		fresh = append(fresh, metric)
	}
	for metric := range existing {
		if _, still := efforts[metric]; still {
			continue
		}
		if _, err := tx.ExecContext(ctx, d.query(
			`DELETE FROM best_efforts WHERE session_id = ? AND metric = ? AND award = ''`), sessionID, metric); err != nil {
			return nil, err
		}
	}
	return fresh, tx.Commit()
}

// SetEffortAward writes a ride's award for a metric, with the best it beat, and
// marks it not yet announced. The caller only does this for a row UpsertEfforts
// just reported as new, so an award is written once.
func (d *DB) SetEffortAward(ctx context.Context, sessionID, metric, award string, previous float64) error {
	_, err := d.db.ExecContext(ctx, d.query(
		`UPDATE best_efforts SET award = ?, previous = ?, announced = ? WHERE session_id = ? AND metric = ?`),
		award, previous, false, sessionID, metric)
	return err
}

const effortColumns = `rider, session_id, metric, sport, date, value, award, previous, announced, created_at`

func scanEffort(row rowScanner) (BestEffort, error) {
	var e BestEffort
	err := row.Scan(&e.Rider, &e.SessionID, &e.Metric, &e.Sport, &e.Date, &e.Value, &e.Award, &e.Previous, &e.Announced, &e.CreatedAt)
	return e, err
}

// scopeClause is the date bounds shared by BestIn and ScopeValues; an empty
// bound is open. Both ends are inclusive local-date strings.
func scopeClause(from, to string, args []any) (string, []any) {
	clause := ""
	if from != "" {
		clause += ` AND date >= ?`
		args = append(args, from)
	}
	if to != "" {
		clause += ` AND date <= ?`
		args = append(args, to)
	}
	return clause, args
}

// BestIn is a rider's best ride for a metric between two dates, both inclusive
// (an empty bound is open). A tie goes to the earliest ride, so the record
// stays with whoever set it first.
func (d *DB) BestIn(ctx context.Context, rider, metric, from, to string) (BestEffort, bool, error) {
	args := []any{normalizeRider(rider), metric}
	clause, args := scopeClause(from, to, args)
	row := d.db.QueryRowContext(ctx, d.query(`SELECT `+effortColumns+` FROM best_efforts
        WHERE rider = ? AND metric = ?`+clause+` ORDER BY value DESC, date ASC, session_id ASC LIMIT 1`), args...)
	e, err := scanEffort(row)
	if errors.Is(err, sql.ErrNoRows) {
		return BestEffort{}, false, nil
	}
	return e, err == nil, err
}

// ScopeValues is every stored value of a metric in a scope, except the ride
// excludeSession (the one being judged): what bestefforts.Award compares with.
func (d *DB) ScopeValues(ctx context.Context, rider, metric, from, to, excludeSession string) ([]float64, error) {
	args := []any{normalizeRider(rider), metric}
	clause, args := scopeClause(from, to, args)
	if excludeSession != "" {
		clause += ` AND session_id <> ?`
		args = append(args, excludeSession)
	}
	rows, err := d.db.QueryContext(ctx, d.query(`SELECT value FROM best_efforts WHERE rider = ? AND metric = ?`+clause), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteEffortsForSession removes a session's rows, awards included: the ride
// they describe is gone.
func (d *DB) DeleteEffortsForSession(ctx context.Context, sessionID string) error {
	_, err := d.db.ExecContext(ctx, d.query(`DELETE FROM best_efforts WHERE session_id = ?`), sessionID)
	return err
}

// UnannouncedAwards lists the awards a rider has not been told about yet,
// oldest ride first.
func (d *DB) UnannouncedAwards(ctx context.Context, rider string) ([]BestEffort, error) {
	rows, err := d.db.QueryContext(ctx, d.query(`SELECT `+effortColumns+` FROM best_efforts
        WHERE rider = ? AND award <> '' AND announced = ? ORDER BY date, session_id, metric`), normalizeRider(rider), false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []BestEffort
	for rows.Next() {
		e, err := scanEffort(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkAwardsAnnounced records that the rider has been told about these awards.
// The rider is part of the match, so naming another rider's rows does nothing.
func (d *DB) MarkAwardsAnnounced(ctx context.Context, rider string, awards []BestEffort) error {
	rider = normalizeRider(rider)
	for _, a := range awards {
		if _, err := d.db.ExecContext(ctx, d.query(
			`UPDATE best_efforts SET announced = ? WHERE rider = ? AND session_id = ? AND metric = ?`),
			true, rider, a.SessionID, a.Metric); err != nil {
			return err
		}
	}
	return nil
}

// RebuildBestEfforts regenerates every row from session_analyses joined to
// completed_sessions, with no award (it cannot know what was new at the time).
// It runs only when the table is empty, so a start after the first does
// nothing, and returns the rows it wrote.
func (d *DB) RebuildBestEfforts(ctx context.Context, efforts EffortFunc) (int, error) {
	var one int
	err := d.db.QueryRowContext(ctx, `SELECT 1 FROM best_efforts LIMIT 1`).Scan(&one)
	if err == nil {
		return 0, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return d.BackfillEfforts(ctx, "", efforts)
}

// BackfillEfforts writes the rows of every analysed ride that has none, for one
// rider ("" for all of them), with no award and already announced. It is how an
// import's rides reach the record book: the import analyses a ride without
// going through the sync, and an imported ride never earns an award. Rows that
// exist are left exactly as they are.
func (d *DB) BackfillEfforts(ctx context.Context, rider string, efforts EffortFunc) (int, error) {
	rider = normalizeRider(rider)
	rows, err := d.db.QueryContext(ctx, d.query(`
        SELECT a.session_id, a.rider, s.sport, s.date, a.max_hr, a.best_speed_1200, a.best_speed_1800, a.power_curve
        FROM session_analyses a
        JOIN completed_sessions s ON s.id = a.session_id
        WHERE (? = '' OR a.rider = ?)
          AND NOT EXISTS (SELECT 1 FROM best_efforts e WHERE e.session_id = a.session_id)`), rider, rider)
	if err != nil {
		return 0, err
	}
	var pending []BestEffort
	for rows.Next() {
		var (
			a     SessionAnalysis
			sport string
			date  string
			curve []byte
		)
		if err := rows.Scan(&a.SessionID, &a.Rider, &sport, &date, &a.MaxHR, &a.BestSpeed1200, &a.BestSpeed1800, &curve); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if len(curve) > 0 {
			if err := json.Unmarshal(curve, &a.PowerCurve); err != nil {
				_ = rows.Close()
				return 0, fmt.Errorf("workout: decode power curve for %s: %w", a.SessionID, err)
			}
		}
		for metric, value := range efforts(a, sport) {
			pending = append(pending, BestEffort{Rider: a.Rider, SessionID: a.SessionID, Metric: metric, Sport: sport, Date: date, Value: value})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := d.timestamp()
	written := 0
	for _, e := range pending {
		res, err := tx.ExecContext(ctx, d.query(`
            INSERT INTO best_efforts (rider, session_id, metric, sport, date, value, award, announced, created_at)
            VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)
            ON CONFLICT (session_id, metric) DO NOTHING`),
			e.Rider, e.SessionID, e.Metric, e.Sport, e.Date, e.Value, true, now)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		written += int(n)
	}
	return written, tx.Commit()
}
