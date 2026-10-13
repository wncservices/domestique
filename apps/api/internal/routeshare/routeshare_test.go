package routeshare

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

const postgresEnv = "DOMESTIQUE_TEST_POSTGRES"

func openStore(t *testing.T, dsn string) *Store {
	t.Helper()

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	// Postgres tests share a database; start clean.
	if _, err := db.Conn().Exec(`DELETE FROM route_share_redemptions`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`DELETE FROM route_shares`); err != nil {
		t.Fatal(err)
	}
	return store
}

func sqliteStore(t *testing.T) *Store {
	return openStore(t, filepath.Join(t.TempDir(), "routeshare.db"))
}

func postgresStore(t *testing.T) *Store {
	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return openStore(t, dsn)
}

const sevenDays = 7 * 24 * time.Hour

func TestRouteShareEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *Store{
		"sqlite":   sqliteStore,
		"postgres": postgresStore,
	} {
		t.Run(engine, func(t *testing.T) {
			t.Run("create then look up by the raw token", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				token, share, err := store.Create(ctx, Route{Slug: "hill-loop"}, "wilant", sevenDays)
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if token == "" {
					t.Fatal("token is empty")
				}
				if share.ID == token {
					t.Fatal("share.ID must not equal the raw token — only its hash may be stored")
				}
				if share.RouteSlug != "hill-loop" || share.CreatedBy != "wilant" {
					t.Fatalf("share = %+v", share)
				}

				got, err := store.Lookup(ctx, token)
				if err != nil {
					t.Fatalf("lookup: %v", err)
				}
				if got.ID != share.ID || got.RouteSlug != "hill-loop" {
					t.Fatalf("got = %+v", got)
				}
			})

			t.Run("a token that never existed is ErrNotFound", func(t *testing.T) {
				store := open(t)
				if _, err := store.Lookup(t.Context(), "never-issued"); !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
			})

			t.Run("revoke makes Lookup ErrNotFound but Get still shows it", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				token, share, err := store.Create(ctx, Route{Slug: "hill-loop"}, "wilant", sevenDays)
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if err := store.Revoke(ctx, share.ID); err != nil {
					t.Fatalf("revoke: %v", err)
				}

				if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrNotFound) {
					t.Fatalf("lookup after revoke: err = %v, want ErrNotFound — a revoked share must not be usable", err)
				}

				got, err := store.Get(ctx, share.ID)
				if err != nil {
					t.Fatalf("get after revoke: %v", err)
				}
				if !got.Revoked() {
					t.Error("got.Revoked() = false, want true — the owner's own view should still show it as revoked")
				}
			})

			t.Run("revoking an already-revoked or unknown id is not an error", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()
				_, share, err := store.Create(ctx, Route{Slug: "hill-loop"}, "wilant", sevenDays)
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if err := store.Revoke(ctx, share.ID); err != nil {
					t.Fatalf("first revoke: %v", err)
				}
				if err := store.Revoke(ctx, share.ID); err != nil {
					t.Fatalf("second revoke: %v", err)
				}
				if err := store.Revoke(ctx, "no-such-id"); err != nil {
					t.Fatalf("revoke unknown id: %v", err)
				}
			})

			t.Run("an expired share is still returned by Lookup, reporting Expired true", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()
				token, share, err := store.Create(ctx, Route{Slug: "hill-loop"}, "wilant", sevenDays)
				if err != nil {
					t.Fatalf("create: %v", err)
				}

				future := share.ExpiresAt.Add(time.Hour)
				got, err := store.Lookup(ctx, token)
				if err != nil {
					t.Fatalf("lookup: %v — an expired share must still resolve, not ErrNotFound", err)
				}
				if !got.Expired(future) {
					t.Error("Expired(after expiry) = false, want true")
				}
				if got.Expired(share.CreatedAt) {
					t.Error("Expired(right after creation) = true, want false")
				}
			})

			t.Run("Create rejects an unsupported lifetime", func(t *testing.T) {
				store := open(t)
				if _, _, err := store.Create(t.Context(), Route{Slug: "hill-loop"}, "wilant", 3*24*time.Hour); err == nil {
					t.Fatal("expected an error for an unsupported ttl")
				}
			})

			t.Run("Create requires a route and a rider", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()
				if _, _, err := store.Create(ctx, Route{Slug: ""}, "wilant", sevenDays); err == nil {
					t.Fatal("expected an error for a missing route")
				}
				if _, _, err := store.Create(ctx, Route{Slug: "hill-loop"}, "", sevenDays); err == nil {
					t.Fatal("expected an error for a missing rider")
				}
			})

			t.Run("Touch records a redemption, and a repeat visit updates it rather than duplicating", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()
				_, share, err := store.Create(ctx, Route{Slug: "hill-loop"}, "wilant", sevenDays)
				if err != nil {
					t.Fatalf("create: %v", err)
				}

				if err := store.Touch(ctx, share.ID, "friend"); err != nil {
					t.Fatalf("touch: %v", err)
				}
				first, err := store.Redemptions(ctx, share.ID)
				if err != nil {
					t.Fatalf("redemptions: %v", err)
				}
				if len(first) != 1 || first[0].Rider != "friend" {
					t.Fatalf("redemptions = %+v, want one row for friend", first)
				}

				if err := store.Touch(ctx, share.ID, "friend"); err != nil {
					t.Fatalf("second touch: %v", err)
				}
				second, err := store.Redemptions(ctx, share.ID)
				if err != nil {
					t.Fatalf("redemptions: %v", err)
				}
				if len(second) != 1 {
					t.Fatalf("redemptions = %+v, want still exactly one row — a repeat visit updates, not duplicates", second)
				}
				if !second[0].RedeemedAt.After(first[0].RedeemedAt) && second[0].RedeemedAt != first[0].RedeemedAt {
					t.Errorf("redeemedAt did not move forward on the second visit: first=%v second=%v",
						first[0].RedeemedAt, second[0].RedeemedAt)
				}
			})

			t.Run("DeleteRoute removes that route's shares and redemptions only", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				_, gone, err := store.Create(ctx, Route{Slug: "hill-loop"}, "wilant", sevenDays)
				if err != nil {
					t.Fatal(err)
				}
				_, kept, err := store.Create(ctx, Route{Slug: "flat-loop"}, "wilant", sevenDays)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{gone.ID, kept.ID} {
					if err := store.Touch(ctx, id, "bob"); err != nil {
						t.Fatal(err)
					}
				}

				n, err := store.DeleteRoute(ctx, "hill-loop")
				if err != nil || n != 1 {
					t.Fatalf("DeleteRoute = %d, %v; want 1, nil", n, err)
				}
				if _, err := store.Get(ctx, gone.ID); !errors.Is(err, ErrNotFound) {
					t.Errorf("deleted route's share: err = %v, want ErrNotFound", err)
				}
				if r, _ := store.Redemptions(ctx, gone.ID); len(r) != 0 {
					t.Errorf("redemptions of the deleted route's share remain: %v", r)
				}
				if _, err := store.Get(ctx, kept.ID); err != nil {
					t.Errorf("another route's share was touched: %v", err)
				}
				if r, _ := store.Redemptions(ctx, kept.ID); len(r) != 1 {
					t.Errorf("another route's redemptions were touched: %v", r)
				}
			})

			t.Run("the route binding is stored and read back", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				_, share, err := store.Create(ctx, Route{Slug: "hill-loop", Owner: "Alice", CreatedAt: "2026-01-01T00:00:00Z"}, "root", sevenDays)
				if err != nil {
					t.Fatal(err)
				}
				got, err := store.Get(ctx, share.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.RouteOwner != "alice" || got.RouteCreatedAt != "2026-01-01T00:00:00Z" {
					t.Fatalf("binding = %q, %q", got.RouteOwner, got.RouteCreatedAt)
				}
				// An admin's share of alice's route matches alice's route, not root's.
				if !got.Matches("alice", "2026-01-01T00:00:00Z") {
					t.Error("same route must match")
				}
				if got.Matches("root", "2026-01-01T00:00:00Z") || got.Matches("bob", "2026-01-01T00:00:00Z") {
					t.Error("another owner under the same slug must not match")
				}
				if got.Matches("alice", "2026-02-02T00:00:00Z") {
					t.Error("another generation of the slug must not match")
				}
			})

			t.Run("a share from before the binding falls back to the creator being the owner", func(t *testing.T) {
				legacy := Share{CreatedBy: "alice"}
				if !legacy.Matches("alice", "anything") {
					t.Error("the creator's own route must still match")
				}
				if legacy.Matches("bob", "anything") {
					t.Error("someone else's route under the slug must not match")
				}
			})

			t.Run("opening the store again is idempotent and keeps old rows", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()
				_, share, err := store.Create(ctx, Route{Slug: "hill-loop", Owner: "alice"}, "alice", sevenDays)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.addBindingColumns(); err != nil {
					t.Fatalf("second migration: %v", err)
				}
				if _, err := store.Get(ctx, share.ID); err != nil {
					t.Fatalf("share lost across migration: %v", err)
				}
			})

			t.Run("ListForRoute is scoped to one route and includes revoked shares", func(t *testing.T) {
				store := open(t)
				ctx := t.Context()

				_, shareA, err := store.Create(ctx, Route{Slug: "hill-loop"}, "wilant", sevenDays)
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if _, _, err := store.Create(ctx, Route{Slug: "flat-loop"}, "wilant", sevenDays); err != nil {
					t.Fatalf("create: %v", err)
				}
				if err := store.Revoke(ctx, shareA.ID); err != nil {
					t.Fatalf("revoke: %v", err)
				}

				list, err := store.ListForRoute(ctx, "hill-loop")
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(list) != 1 || list[0].ID != shareA.ID {
					t.Fatalf("list = %+v, want just hill-loop's one (revoked) share", list)
				}
				if !list[0].Revoked() {
					t.Error("the listed share should still show as revoked")
				}
			})
		})
	}
}
