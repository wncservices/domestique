package basemap

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/dbx"
)

// PreviewImageCache persists rendered card PNGs (see renderimage.go), keyed
// by (slug, theme) — mirrors PreviewCache's own invalidation contract
// exactly (BasemapUpdateID pins each row to whichever
// basemap.Store.LatestSucceeded() ID it was rendered against; a mismatch is
// a miss, not an error, so an admin rebuilding the basemap invalidates every
// cached image the same automatic way it already invalidates the JSON
// cache), kept as its own table rather than a column added to
// track_preview_cache because a route's rendered image comes in two
// variants (light/dark) where its geometry JSON comes in exactly one.
type PreviewImageCache struct {
	db      *sql.DB
	dialect dbx.Dialect
}

func previewImageCacheSchema(dialect dbx.Dialect) string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS track_preview_images (
    slug              TEXT NOT NULL,
    theme             TEXT NOT NULL,
    basemap_update_id TEXT NOT NULL,
    image_data        %s NOT NULL,
    computed_at       TEXT NOT NULL,
    PRIMARY KEY (slug, theme)
);`, dialect.Blob)
}

// UsePreviewImageCacheDB puts the table in an already-open database.
func UsePreviewImageCacheDB(db *sql.DB, dsn string) (*PreviewImageCache, error) {
	d, err := dbx.For(dsn)
	if err != nil {
		return nil, err
	}
	cache := &PreviewImageCache{db: db, dialect: d}
	if _, err := db.Exec(previewImageCacheSchema(d)); err != nil {
		return nil, fmt.Errorf("create track_preview_images table: %w", err)
	}
	return cache, nil
}

// Get returns the cached PNG bytes for slug+theme, only if rendered against
// basemapUpdateID — same "anything else is a miss, not an error" idiom as
// PreviewCache.Get.
func (c *PreviewImageCache) Get(slug, theme, basemapUpdateID string) (imageData []byte, found bool, err error) {
	row := c.db.QueryRow(c.dialect.Rebind(
		`SELECT image_data FROM track_preview_images WHERE slug = ? AND theme = ? AND basemap_update_id = ?`),
		slug, theme, basemapUpdateID)
	if err := row.Scan(&imageData); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return imageData, true, nil
}

// Put upserts the cached image for slug+theme. A stale row (rendered against
// an older basemap) is overwritten in place — same reasoning as
// PreviewCache.Put, there is never a reason to keep more than the current
// basemap's version.
func (c *PreviewImageCache) Put(slug, theme, basemapUpdateID string, imageData []byte) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := c.db.Exec(c.dialect.Rebind(`
INSERT INTO track_preview_images (slug, theme, basemap_update_id, image_data, computed_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (slug, theme) DO UPDATE SET
    basemap_update_id = excluded.basemap_update_id,
    image_data         = excluded.image_data,
    computed_at        = excluded.computed_at`),
		slug, theme, basemapUpdateID, imageData, now)
	return err
}
