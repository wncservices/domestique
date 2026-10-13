package api

import (
	"context"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/basemap"
	"github.com/wncservices/domestique/apps/api/internal/routeshare"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// A purge deletes the rider's routes, so every share link to those routes goes
// with them, whoever made it (an admin can share a rider's route), and the
// rendered card previews keyed by the same slugs go too. Slugs are reusable:
// anything left behind would attach itself to the next route to take the name.
func TestPurgeRemovesSharesAndPreviewsOfTheRidersRoutes(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		ctx := context.Background()
		src := env.srv.Source.(*source.DB)
		previews, err := basemap.UsePreviewCacheDB(env.db, src.DSN())
		if err != nil {
			t.Fatal(err)
		}
		images, err := basemap.UsePreviewImageCacheDB(env.db, src.DSN())
		if err != nil {
			t.Fatal(err)
		}
		env.srv.PreviewCache, env.srv.PreviewImageCache = previews, images

		const gpxBody = `<?xml version="1.0"?><gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>` +
			`<trkpt lat="50.79" lon="2.81"><ele>42</ele></trkpt><trkpt lat="50.80" lon="2.84"><ele>128</ele></trkpt></trkseg></trk></gpx>`
		gone, err := src.Create(ctx, source.CreateRequest{Name: "Leaver Loop", GPX: []byte(gpxBody), UploadedBy: "leaver"})
		if err != nil {
			t.Fatal(err)
		}
		kept, err := src.Create(ctx, source.CreateRequest{Name: "Stayer Loop", GPX: []byte(gpxBody), UploadedBy: "stayer"})
		if err != nil {
			t.Fatal(err)
		}
		// An admin's link to the leaver's route: created_by is not the leaver.
		if _, _, err := env.srv.Shares.Create(ctx, routeshare.Route{Slug: gone.Slug, Owner: "leaver", CreatedAt: gone.CreatedAt}, "admin", 7*24*time.Hour); err != nil {
			t.Fatal(err)
		}
		if _, _, err := env.srv.Shares.Create(ctx, routeshare.Route{Slug: kept.Slug, Owner: "stayer", CreatedAt: kept.CreatedAt}, "stayer", 7*24*time.Hour); err != nil {
			t.Fatal(err)
		}
		for _, slug := range []string{gone.Slug, kept.Slug} {
			if err := previews.Put(slug, "u1", "{}"); err != nil {
				t.Fatal(err)
			}
			if err := images.Put(slug, "light", "u1", []byte("png")); err != nil {
				t.Fatal(err)
			}
		}

		if _, err := env.srv.purgeRiderData(ctx, "leaver"); err != nil {
			t.Fatalf("purge: %v", err)
		}

		if n := count(t, env, "route_shares", "route_slug", gone.Slug); n != 0 {
			t.Errorf("%d share links to the purged rider's route remain", n)
		}
		if n := count(t, env, "route_shares", "route_slug", kept.Slug); n != 1 {
			t.Errorf("the other rider's share was touched: %d rows, want 1", n)
		}
		if n := count(t, env, "track_preview_cache", "slug", gone.Slug); n != 0 {
			t.Errorf("track_preview_cache: %d rows left for the purged rider's route", n)
		}
		if n := count(t, env, "track_preview_images", "slug", gone.Slug); n != 0 {
			t.Errorf("track_preview_images: %d rows left for the purged rider's route", n)
		}
		if n := count(t, env, "track_preview_cache", "slug", kept.Slug); n != 1 {
			t.Errorf("the other rider's preview was touched: %d rows, want 1", n)
		}
	})
}
