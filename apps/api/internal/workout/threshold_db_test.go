package workout

import "testing"

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

				if _, ok, err := db.LatestDismissedSuggestion(ctx, "wilant", FieldMaxHR); err != nil || ok {
					t.Fatalf("before dismiss: ok=%v err=%v, want none yet", ok, err)
				}

				if err := db.MarkSuggestionDismissed(ctx, sug.ID); err != nil {
					t.Fatalf("dismiss: %v", err)
				}

				dismissed, ok, err := db.LatestDismissedSuggestion(ctx, "wilant", FieldMaxHR)
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
				stillDismissed, ok, err := db.LatestDismissedSuggestion(ctx, "wilant", "ftp")
				if err != nil || !ok || stillDismissed.ID != sug.ID {
					t.Fatalf("latest dismissed after fresh create = %+v ok=%v err=%v", stillDismissed, ok, err)
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
