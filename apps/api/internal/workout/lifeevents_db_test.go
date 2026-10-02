package workout

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func TestLifeEventsEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}
	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("create, read, update, delete", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				created, err := db.CreateLifeEvent(ctx, LifeEvent{
					Rider: " Wilant ", Kind: "travel", Start: "2026-10-08", End: "2026-10-11",
					Option: "no_bike", Note: "Berlin",
				})
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if created.ID == "" || created.CreatedAt == "" || created.UpdatedAt == "" {
					t.Fatalf("create did not fill id and timestamps: %+v", created)
				}
				if created.Rider != "wilant" {
					t.Errorf("rider = %q, want it normalised to wilant", created.Rider)
				}

				got, err := db.GetLifeEvent(ctx, "wilant", created.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Kind != "travel" || got.Start != "2026-10-08" || got.End != "2026-10-11" || got.Option != "no_bike" || got.Note != "Berlin" {
					t.Fatalf("read back %+v", got)
				}

				got.End, got.Note = "2026-10-12", "Berlin, then Hamburg"
				updated, err := db.UpdateLifeEvent(ctx, "wilant", created.ID, got)
				if err != nil {
					t.Fatalf("update: %v", err)
				}
				if updated.End != "2026-10-12" || updated.Note != "Berlin, then Hamburg" {
					t.Fatalf("update not stored: %+v", updated)
				}
				if updated.CreatedAt != created.CreatedAt {
					t.Error("an update must not change created_at")
				}

				if err := db.DeleteLifeEvent(ctx, "wilant", created.ID); err != nil {
					t.Fatalf("delete: %v", err)
				}
				if _, err := db.GetLifeEvent(ctx, "wilant", created.ID); !errors.Is(err, ErrLifeEventNotFound) {
					t.Fatalf("after delete: %v, want ErrLifeEventNotFound", err)
				}
				if err := db.DeleteLifeEvent(ctx, "wilant", created.ID); !errors.Is(err, ErrLifeEventNotFound) {
					t.Fatalf("deleting twice: %v, want ErrLifeEventNotFound", err)
				}
			})

			t.Run("two events starting the same day get different ids", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				a, err := db.CreateLifeEvent(ctx, LifeEvent{Rider: "wilant", Kind: "travel", Start: "2026-10-08", End: "2026-10-09"})
				if err != nil {
					t.Fatal(err)
				}
				b, err := db.CreateLifeEvent(ctx, LifeEvent{Rider: "wilant", Kind: "travel", Start: "2026-10-08", End: "2026-10-09"})
				if err != nil {
					t.Fatal(err)
				}
				if a.ID == b.ID {
					t.Fatalf("both events got id %q", a.ID)
				}
			})

			t.Run("a rider only ever sees their own", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				mine, err := db.CreateLifeEvent(ctx, LifeEvent{Rider: "wilant", Kind: "illness", Start: "2026-10-08", End: "2026-10-09", Option: "mild"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.CreateLifeEvent(ctx, LifeEvent{Rider: "sam", Kind: "busy", Start: "2026-10-08", End: "2026-10-09"}); err != nil {
					t.Fatal(err)
				}

				list, err := db.ListLifeEvents(ctx, "wilant", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(list) != 1 || list[0].ID != mine.ID {
					t.Fatalf("wilant sees %+v, want only their own", list)
				}
				if _, err := db.GetLifeEvent(ctx, "sam", mine.ID); !errors.Is(err, ErrLifeEventNotFound) {
					t.Errorf("sam reading wilant's event: %v, want not found", err)
				}
				if _, err := db.UpdateLifeEvent(ctx, "sam", mine.ID, LifeEvent{Kind: "busy", Start: "2026-10-08", End: "2026-10-09"}); !errors.Is(err, ErrLifeEventNotFound) {
					t.Errorf("sam updating wilant's event: %v, want not found", err)
				}
				if err := db.DeleteLifeEvent(ctx, "sam", mine.ID); !errors.Is(err, ErrLifeEventNotFound) {
					t.Errorf("sam deleting wilant's event: %v, want not found", err)
				}
				if still, err := db.GetLifeEvent(ctx, "wilant", mine.ID); err != nil || still.Kind != "illness" {
					t.Fatalf("the event was changed by another rider: %+v, %v", still, err)
				}
			})

			t.Run("listing from a date drops events that ended before it, oldest first", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				for _, e := range []LifeEvent{
					{Rider: "wilant", Kind: "busy", Start: "2026-10-20", End: "2026-10-21"},
					{Rider: "wilant", Kind: "travel", Start: "2026-09-01", End: "2026-09-05"},
					{Rider: "wilant", Kind: "illness", Start: "2026-09-30", End: "2026-10-01", Option: "proper"},
					{Rider: "wilant", Kind: "other", Start: "2026-10-08", End: "2026-10-09"},
				} {
					if _, err := db.CreateLifeEvent(ctx, e); err != nil {
						t.Fatal(err)
					}
				}
				all, err := db.ListLifeEvents(ctx, "wilant", "")
				if err != nil || len(all) != 4 {
					t.Fatalf("all = %d events, %v", len(all), err)
				}
				recent, err := db.ListLifeEvents(ctx, "wilant", "2026-09-30")
				if err != nil {
					t.Fatal(err)
				}
				var kinds []string
				for _, e := range recent {
					kinds = append(kinds, e.Kind)
				}
				want := []string{"illness", "other", "busy"}
				if len(kinds) != len(want) {
					t.Fatalf("recent = %v, want %v", kinds, want)
				}
				for i := range want {
					if kinds[i] != want[i] {
						t.Fatalf("recent = %v, want %v", kinds, want)
					}
				}
			})
		})
	}
}

func TestLifeEventsTableIsAddedToAnExistingDatabase(t *testing.T) {
	// A database that predates the table must gain it on start, and a second
	// start must change nothing.
	dsn := filepath.Join(t.TempDir(), "old.db")
	src, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	first, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Conn().Exec(`DROP TABLE life_events`); err != nil {
		t.Fatalf("the table was not created in the first place: %v", err)
	}
	_ = first

	again, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("starting on a database without the table: %v", err)
	}
	if _, err := again.CreateLifeEvent(t.Context(), LifeEvent{Rider: "wilant", Kind: "busy", Start: "2026-10-08", End: "2026-10-08"}); err != nil {
		t.Fatalf("the table was not added: %v", err)
	}
	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Fatalf("a second start: %v", err)
	}
	list, err := again.ListLifeEvents(t.Context(), "wilant", "")
	if err != nil || len(list) != 1 {
		t.Fatalf("a second start changed the data: %v, %v", list, err)
	}
}
