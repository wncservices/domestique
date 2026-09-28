package workout

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

// TestThresholdSuggestionsEachEngine mirrors TestEachEngine's own
// SQLite-and-PostgreSQL structure (see db_test.go) for the
// threshold_suggestions table — new in this change, so it gets its own
// top-level test rather than growing db_test.go's already-large
// TestEachEngine further, the same call progression_db_test.go already
// made for progression_levels.
func TestThresholdSuggestionsEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("create, get, list pending newest first, owner-only", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				first, err := db.CreateSuggestion(ctx, ThresholdSuggestion{
					Rider: "Wilant", Field: "ftp", Value: 260, Previous: 250,
					SourceSessionID: "sess-1", SourceDate: "2026-01-10",
					Reason: "from Saturday's 20-minute effort (282 W)",
				})
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if first.Rider != "wilant" {
					t.Errorf("rider = %q, want normalized wilant", first.Rider)
				}
				if first.Status != ThresholdPending {
					t.Errorf("status = %q, want pending", first.Status)
				}
				if first.CreatedAt == "" || first.UpdatedAt == "" {
					t.Error("timestamps not stamped")
				}

				second, err := db.CreateSuggestion(ctx, ThresholdSuggestion{
					Rider: "wilant", Field: FieldMaxHR, Value: 191, Previous: 188,
					SourceSessionID: "sess-2", SourceDate: "2026-01-12",
					Reason: "peak heart rate 191 on Tuesday's ride",
				})
				if err != nil {
					t.Fatalf("create second: %v", err)
				}

				if _, err := db.CreateSuggestion(ctx, ThresholdSuggestion{
					Rider: "other", Field: "ftp", Value: 300, Previous: 280,
				}); err != nil {
					t.Fatalf("create other rider's: %v", err)
				}

				got, err := db.GetSuggestion(ctx, first.ID)
				if err != nil {
					t.Fatalf("get: %v", err)
				}
				if got.Value != 260 || got.Field != "ftp" || got.Reason != first.Reason {
					t.Errorf("get = %+v", got)
				}

				pending, err := db.ListPendingSuggestions(ctx, "WILANT")
				if err != nil {
					t.Fatalf("list pending: %v", err)
				}
				if len(pending) != 2 {
					t.Fatalf("list = %+v, want 2 pending for wilant only", pending)
				}
				if pending[0].ID != second.ID {
					t.Errorf("first in list = %+v, want the most recently created", pending[0])
				}

				if _, err := db.GetSuggestion(ctx, "does-not-exist"); err != ErrThresholdSuggestionNotFound {
					t.Errorf("get missing id: err = %v, want ErrThresholdSuggestionNotFound", err)
				}
			})

			t.Run("a newer pending suggestion replaces the older one for the same field", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				first, err := db.CreateSuggestion(ctx, ThresholdSuggestion{
					Rider: "wilant", Field: "ftp", Value: 260, Previous: 250,
				})
				if err != nil {
					t.Fatalf("create first: %v", err)
				}
				second, err := db.CreateSuggestion(ctx, ThresholdSuggestion{
					Rider: "wilant", Field: "ftp", Value: 268, Previous: 250,
				})
				if err != nil {
					t.Fatalf("create second: %v", err)
				}

				pending, err := db.ListPendingSuggestions(ctx, "wilant")
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(pending) != 1 || pending[0].ID != second.ID || pending[0].Value != 268 {
					t.Fatalf("pending = %+v, want only the newer (268 W) suggestion", pending)
				}

				if _, err := db.GetSuggestion(ctx, first.ID); err != ErrThresholdSuggestionNotFound {
					t.Errorf("the replaced suggestion should be gone entirely, got err=%v", err)
				}
			})

			t.Run("accept marks accepted and is idempotent-refusing on a second resolve", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				sug, err := db.CreateSuggestion(ctx, ThresholdSuggestion{Rider: "wilant", Field: "ftp", Value: 260})
				if err != nil {
					t.Fatalf("create: %v", err)
				}

				if err := db.MarkSuggestionAccepted(ctx, sug.ID); err != nil {
					t.Fatalf("accept: %v", err)
				}
				got, err := db.GetSuggestion(ctx, sug.ID)
				if err != nil || got.Status != ThresholdAccepted {
					t.Fatalf("after accept: %+v, err=%v", got, err)
				}

				if err := db.MarkSuggestionAccepted(ctx, sug.ID); err != ErrThresholdSuggestionNotFound {
					t.Errorf("accepting an already-resolved suggestion: err = %v, want ErrThresholdSuggestionNotFound", err)
				}

				pending, err := db.ListPendingSuggestions(ctx, "wilant")
				if err != nil || len(pending) != 0 {
					t.Errorf("pending after accept = %+v, err=%v, want none", pending, err)
				}
			})

			t.Run("dismiss marks dismissed and LatestDismissedSuggestion finds it", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				sug, err := db.CreateSuggestion(ctx, ThresholdSuggestion{
					Rider: "wilant", Field: FieldMaxHR, Value: 191, Previous: 188,
				})
				if err != nil {
					t.Fatalf("create: %v", err)
				}

				if _, ok, err := db.LatestDismissedSuggestion(ctx, "wilant", FieldMaxHR, "up"); err != nil || ok {
					t.Fatalf("before dismiss: ok=%v err=%v, want none yet", ok, err)
				}

				if err := db.MarkSuggestionDismissed(ctx, sug.ID); err != nil {
					t.Fatalf("dismiss: %v", err)
				}

				dismissed, ok, err := db.LatestDismissedSuggestion(ctx, "wilant", FieldMaxHR, "up")
				if err != nil || !ok {
					t.Fatalf("latest dismissed: ok=%v err=%v", ok, err)
				}
				if dismissed.ID != sug.ID || dismissed.Status != ThresholdDismissed {
					t.Errorf("dismissed = %+v", dismissed)
				}

				if err := db.MarkSuggestionDismissed(ctx, sug.ID); err != ErrThresholdSuggestionNotFound {
					t.Errorf("dismissing again: err = %v, want ErrThresholdSuggestionNotFound", err)
				}
			})

			t.Run("dismissing does not block a fresh suggestion for the field afterwards", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				sug, err := db.CreateSuggestion(ctx, ThresholdSuggestion{Rider: "wilant", Field: "ftp", Value: 260})
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if err := db.MarkSuggestionDismissed(ctx, sug.ID); err != nil {
					t.Fatalf("dismiss: %v", err)
				}

				fresh, err := db.CreateSuggestion(ctx, ThresholdSuggestion{Rider: "wilant", Field: "ftp", Value: 275})
				if err != nil {
					t.Fatalf("create after dismiss: %v", err)
				}
				pending, err := db.ListPendingSuggestions(ctx, "wilant")
				if err != nil || len(pending) != 1 || pending[0].ID != fresh.ID {
					t.Fatalf("pending after dismiss+create = %+v, err=%v", pending, err)
				}
				// The dismissed row itself is untouched — it is history the
				// sync's re-suggest gate reads, not something CreateSuggestion
				// ever overwrites (only a *pending* row for the same field is
				// replaced).
				stillDismissed, ok, err := db.LatestDismissedSuggestion(ctx, "wilant", "ftp", "up")
				if err != nil || !ok || stillDismissed.ID != sug.ID {
					t.Fatalf("latest dismissed after fresh create = %+v ok=%v err=%v", stillDismissed, ok, err)
				}
			})

			t.Run("direction round-trips and defaults to up when not set", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				withDirection, err := db.CreateSuggestion(ctx, ThresholdSuggestion{
					Rider: "wilant", Field: "ftp", Value: 240, Previous: 260, Direction: "down",
				})
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if withDirection.Direction != "down" {
					t.Errorf("direction = %q, want down", withDirection.Direction)
				}
				got, err := db.GetSuggestion(ctx, withDirection.ID)
				if err != nil || got.Direction != "down" {
					t.Fatalf("get = %+v, err=%v, want direction=down", got, err)
				}

				defaulted, err := db.CreateSuggestion(ctx, ThresholdSuggestion{Rider: "wilant", Field: FieldMaxHR, Value: 191})
				if err != nil {
					t.Fatalf("create without direction: %v", err)
				}
				if defaulted.Direction != "up" {
					t.Errorf("direction = %q, want the default up", defaulted.Direction)
				}
			})

			t.Run("LatestDismissedSuggestion is scoped to direction — an opposite-direction dismissal is invisible", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				down, err := db.CreateSuggestion(ctx, ThresholdSuggestion{
					Rider: "wilant", Field: "ftp", Value: 240, Previous: 260, Direction: "down",
				})
				if err != nil {
					t.Fatalf("create down: %v", err)
				}
				if err := db.MarkSuggestionDismissed(ctx, down.ID); err != nil {
					t.Fatalf("dismiss down: %v", err)
				}

				// An up lookup must not find the dismissed down suggestion —
				// review round 1's fix: a dismissal only gates a finding in
				// the SAME direction it was itself dismissed at.
				if _, ok, err := db.LatestDismissedSuggestion(ctx, "wilant", "ftp", "up"); err != nil || ok {
					t.Fatalf("up lookup found a down dismissal: ok=%v err=%v", ok, err)
				}
				// The matching direction still finds it.
				found, ok, err := db.LatestDismissedSuggestion(ctx, "wilant", "ftp", "down")
				if err != nil || !ok || found.ID != down.ID {
					t.Fatalf("down lookup = %+v ok=%v err=%v, want the dismissed down suggestion", found, ok, err)
				}
			})

			t.Run("DeletePendingSuggestion removes a pending row and no-ops when there is none", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				sug, err := db.CreateSuggestion(ctx, ThresholdSuggestion{Rider: "wilant", Field: "ftp", Value: 260})
				if err != nil {
					t.Fatalf("create: %v", err)
				}

				if err := db.DeletePendingSuggestion(ctx, "wilant", "ftp"); err != nil {
					t.Fatalf("delete: %v", err)
				}
				if _, err := db.GetSuggestion(ctx, sug.ID); err != ErrThresholdSuggestionNotFound {
					t.Errorf("get after delete: err = %v, want ErrThresholdSuggestionNotFound", err)
				}

				// A second delete, or one for a field with nothing pending,
				// is a no-op rather than an error.
				if err := db.DeletePendingSuggestion(ctx, "wilant", "ftp"); err != nil {
					t.Errorf("delete again: %v, want no error", err)
				}
				if err := db.DeletePendingSuggestion(ctx, "wilant", FieldMaxHR); err != nil {
					t.Errorf("delete for a field with nothing pending: %v, want no error", err)
				}
			})

			t.Run("creating without a rider or field fails", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				if _, err := db.CreateSuggestion(ctx, ThresholdSuggestion{Field: "ftp", Value: 260}); err == nil {
					t.Error("expected an error with no rider")
				}
				if _, err := db.CreateSuggestion(ctx, ThresholdSuggestion{Rider: "wilant", Value: 260}); err == nil {
					t.Error("expected an error with no field")
				}
			})
		})
	}
}

// TestThresholdSuggestionsTableGainsDirectionColumn mirrors
// TestSessionAnalysesTableGainsFeelAndLevelDeltaColumns's own migration
// shape (see progression_db_test.go): a pre-existing threshold_suggestions
// table without direction must migrate cleanly, defaulting existing rows to
// 'up', and UseDB must stay idempotent against an already-migrated database.
func TestThresholdSuggestionsTableGainsDirectionColumn(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	// The pre-migration shape of threshold_suggestions, minus direction.
	if _, err := src.Conn().Exec(`
CREATE TABLE threshold_suggestions (
    id                 TEXT PRIMARY KEY,
    rider              TEXT NOT NULL,
    field              TEXT NOT NULL,
    value              DOUBLE PRECISION NOT NULL DEFAULT 0,
    previous           DOUBLE PRECISION NOT NULL DEFAULT 0,
    source_session_id  TEXT NOT NULL DEFAULT '',
    source_date        TEXT NOT NULL DEFAULT '',
    reason             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'pending',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := src.Conn().Exec(`
INSERT INTO threshold_suggestions (id, rider, field, value, previous, status, created_at, updated_at)
VALUES ('old-sug', 'wilant', 'ftp', 260, 250, 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	db, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("UseDB (migrate): %v", err)
	}

	old, err := db.GetSuggestion(t.Context(), "old-sug")
	if err != nil {
		t.Fatalf("get pre-existing row after migration: %v", err)
	}
	if old.Direction != "up" {
		t.Errorf("pre-existing row direction = %q, want the default up", old.Direction)
	}

	// The migrated column is fully usable — a direction-scoped lookup finds
	// the backfilled row under "up".
	pending, err := db.ListPendingSuggestions(t.Context(), "wilant")
	if err != nil || len(pending) != 1 || pending[0].Direction != "up" {
		t.Fatalf("pending after migration = %+v, err=%v", pending, err)
	}

	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Errorf("second UseDB call: %v", err)
	}
}
