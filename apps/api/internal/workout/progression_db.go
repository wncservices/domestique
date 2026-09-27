package workout

import (
	"context"
	"errors"

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

	_, err := d.db.ExecContext(ctx, d.query(`
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
