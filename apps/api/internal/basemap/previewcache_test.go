package basemap

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func newPreviewCache(t *testing.T) *PreviewCache {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	cache, err := UsePreviewCacheDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func TestPreviewCacheMiss(t *testing.T) {
	cache := newPreviewCache(t)
	if _, found, err := cache.Get("some-route", "build-1"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("expected a miss for a slug that was never cached")
	}
}

func TestPreviewCachePutThenGet(t *testing.T) {
	cache := newPreviewCache(t)
	if err := cache.Put("kemmelberg-loop", "build-1", `{"earth":[],"landuse":[],"water":[],"waterLines":[],"roads":[]}`); err != nil {
		t.Fatal(err)
	}

	got, found, err := cache.Get("kemmelberg-loop", "build-1")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected a hit right after Put")
	}
	if got != `{"earth":[],"landuse":[],"water":[],"waterLines":[],"roads":[]}` {
		t.Fatalf("got %q", got)
	}
}

// TestPreviewCacheMissesOnStaleBasemap is the whole point of keying the
// cache by basemap update ID: an admin rebuilding the basemap must
// invalidate every previously-cached preview without a separate
// cache-busting step, since a rebuild changes every byte offset in the
// archive — a preview computed against the old file is simply wrong
// against the new one.
func TestPreviewCacheMissesOnStaleBasemap(t *testing.T) {
	cache := newPreviewCache(t)
	if err := cache.Put("kemmelberg-loop", "build-1", `{}`); err != nil {
		t.Fatal(err)
	}

	if _, found, err := cache.Get("kemmelberg-loop", "build-2"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("expected a miss once the basemap has been rebuilt (a different update ID)")
	}
}

func TestPreviewCachePutOverwritesPreviousEntry(t *testing.T) {
	cache := newPreviewCache(t)
	if err := cache.Put("kemmelberg-loop", "build-1", `{"v":1}`); err != nil {
		t.Fatal(err)
	}
	if err := cache.Put("kemmelberg-loop", "build-2", `{"v":2}`); err != nil {
		t.Fatal(err)
	}

	got, found, err := cache.Get("kemmelberg-loop", "build-2")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != `{"v":2}` {
		t.Fatalf("got %q, found=%v, want the newer entry", got, found)
	}

	if _, found, err := cache.Get("kemmelberg-loop", "build-1"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("expected the stale build-1 row to have been replaced, not kept alongside the new one")
	}
}

// TestPreviewCacheDelete proves the one way besides a basemap rebuild that a
// cached preview can now go stale: api.handleUpdateRoutePoints editing a
// route's own path. Without a way to clear just that slug, an edited route
// would keep showing its pre-edit preview until some unrelated basemap
// rebuild happened to invalidate it too.
func TestPreviewCacheDelete(t *testing.T) {
	cache := newPreviewCache(t)
	if err := cache.Put("kemmelberg-loop", "build-1", `{"v":1}`); err != nil {
		t.Fatal(err)
	}

	if err := cache.Delete("kemmelberg-loop"); err != nil {
		t.Fatal(err)
	}

	if _, found, err := cache.Get("kemmelberg-loop", "build-1"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("expected a miss after Delete")
	}
}

// TestPreviewCacheDeleteOnAnUncachedSlugIsANoOp proves it is safe to call
// Delete for a route that was never cached (or whose preview was already
// cleared) — handleUpdateRoutePoints calls it unconditionally on every
// edit, not only when it knows a cached entry exists.
func TestPreviewCacheDeleteOnAnUncachedSlugIsANoOp(t *testing.T) {
	cache := newPreviewCache(t)
	if err := cache.Delete("never-cached"); err != nil {
		t.Fatal(err)
	}
}
