package basemap

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func newPreviewImageCache(t *testing.T) *PreviewImageCache {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	cache, err := UsePreviewImageCacheDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func TestPreviewImageCachePutThenGet(t *testing.T) {
	cache := newPreviewImageCache(t)
	if err := cache.Put("kemmelberg-loop", "light", "build-1", []byte("png-bytes")); err != nil {
		t.Fatal(err)
	}

	got, found, err := cache.Get("kemmelberg-loop", "light", "build-1")
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(got) != "png-bytes" {
		t.Fatalf("got %q, found=%v", got, found)
	}
}

// TestPreviewImageCacheDelete proves the one way besides a basemap rebuild
// that a cached card image can now go stale: api.handleUpdateRoutePoints
// editing a route's own path — same reasoning as PreviewCache's own Delete.
// Both themes must clear together, since a rider toggling light/dark should
// never see one already-fresh and the other still showing the pre-edit
// shape.
func TestPreviewImageCacheDelete(t *testing.T) {
	cache := newPreviewImageCache(t)
	if err := cache.Put("kemmelberg-loop", "light", "build-1", []byte("light-png")); err != nil {
		t.Fatal(err)
	}
	if err := cache.Put("kemmelberg-loop", "dark", "build-1", []byte("dark-png")); err != nil {
		t.Fatal(err)
	}

	if err := cache.Delete("kemmelberg-loop"); err != nil {
		t.Fatal(err)
	}

	if _, found, err := cache.Get("kemmelberg-loop", "light", "build-1"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("expected the light theme to miss after Delete")
	}
	if _, found, err := cache.Get("kemmelberg-loop", "dark", "build-1"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("expected the dark theme to miss after Delete")
	}
}

// TestPreviewImageCacheDeleteOnAnUncachedSlugIsANoOp mirrors
// TestPreviewCacheDeleteOnAnUncachedSlugIsANoOp: handleUpdateRoutePoints
// calls Delete unconditionally on every edit, not only when it knows a
// cached image exists.
func TestPreviewImageCacheDeleteOnAnUncachedSlugIsANoOp(t *testing.T) {
	cache := newPreviewImageCache(t)
	if err := cache.Delete("never-cached"); err != nil {
		t.Fatal(err)
	}
}
