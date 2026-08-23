package basemap

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// PreviewCache persists precomputed route-preview geometry (see preview.go)
// keyed by route slug — a route's own points never change after import (a
// re-import creates a new route, it doesn't edit one in place, per
// RouteMap.vue's own comment on the same fact), so the only thing that can
// ever make a cached entry stale is the basemap archive itself changing
// underneath it. BasemapUpdateID pins each row to whichever
// basemap.Store.LatestSucceeded() ID it was computed against; Get compares
// that against the current one and reports a miss on mismatch, so an admin
// rebuilding the basemap invalidates every cached preview automatically —
// no separate cache-busting step to remember.
type PreviewCache struct {
	db      *sql.DB
	dialect dbx.Dialect
}

func previewCacheSchema(dialect dbx.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS track_preview_cache (
    slug              TEXT PRIMARY KEY,
    basemap_update_id TEXT NOT NULL,
    layers_json       TEXT NOT NULL,
    computed_at       TEXT NOT NULL
);`
}

// UsePreviewCacheDB puts the table in an already-open database.
func UsePreviewCacheDB(db *sql.DB, dsn string) (*PreviewCache, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	cache := &PreviewCache{db: db, dialect: d}
	if _, err := db.Exec(previewCacheSchema(d)); err != nil {
		return nil, fmt.Errorf("create track_preview_cache table: %w", err)
	}
	return cache, nil
}

// Get returns the cached layers JSON for slug, only if it was computed
// against basemapUpdateID — anything else (no row, or a stale one computed
// against a since-superseded basemap) is reported as a miss, not an error,
// same idiom as ErrNoRecord elsewhere in this package: the caller's job is
// just to recompute on a miss.
func (c *PreviewCache) Get(slug, basemapUpdateID string) (layersJSON string, found bool, err error) {
	row := c.db.QueryRow(c.dialect.Rebind(
		`SELECT layers_json FROM track_preview_cache WHERE slug = ? AND basemap_update_id = ?`),
		slug, basemapUpdateID)
	if err := row.Scan(&layersJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return layersJSON, true, nil
}

// Put upserts the cached layers for slug. A stale row (computed against an
// older basemap) is overwritten in place rather than kept alongside a new
// one — there is never a reason to keep more than the current basemap's
// version of a route's preview.
func (c *PreviewCache) Put(slug, basemapUpdateID, layersJSON string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := c.db.Exec(c.dialect.Rebind(`
INSERT INTO track_preview_cache (slug, basemap_update_id, layers_json, computed_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (slug) DO UPDATE SET
    basemap_update_id = excluded.basemap_update_id,
    layers_json        = excluded.layers_json,
    computed_at         = excluded.computed_at`),
		slug, basemapUpdateID, layersJSON, now)
	return err
}
