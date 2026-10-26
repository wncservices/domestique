package workout

import "testing"

func TestClaimLLMNoteOnce(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		ok, err := db.ClaimLLMNote(ctx, "Wilant", "changes", "2026-10-20")
		if err != nil || !ok {
			t.Fatalf("first claim = %v, %v; want inserted", ok, err)
		}
		ok, err = db.ClaimLLMNote(ctx, "wilant", "changes", "2026-10-20")
		if err != nil || ok {
			t.Fatalf("second claim = %v, %v; want not inserted", ok, err)
		}
		// Another kind or day is a different note.
		if ok, err := db.ClaimLLMNote(ctx, "wilant", "weekly", "2026-10-20"); err != nil || !ok {
			t.Fatalf("other kind = %v, %v; want inserted", ok, err)
		}
		if ok, err := db.ClaimLLMNote(ctx, "wilant", "changes", "2026-10-21"); err != nil || !ok {
			t.Fatalf("other day = %v, %v; want inserted", ok, err)
		}
	})
}

func TestAClaimedNoteReadsAsAbsent(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		if _, err := db.ClaimLLMNote(ctx, "wilant", "changes", "2026-10-20"); err != nil {
			t.Fatal(err)
		}
		text, ok, err := db.GetLLMNote(ctx, "wilant", "changes", "2026-10-20")
		if err != nil || ok || text != "" {
			t.Fatalf("claimed get = %q, %v, %v; want empty and absent", text, ok, err)
		}
		if _, ok, err := db.GetLLMNote(ctx, "wilant", "changes", "2026-10-22"); err != nil || ok {
			t.Fatalf("unknown get = %v, %v; want absent", ok, err)
		}
	})
}

func TestSaveLLMNoteUpserts(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		if err := db.SaveLLMNote(ctx, "Wilant", "changes", "2026-10-20", "First."); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveLLMNote(ctx, "wilant", "changes", "2026-10-20", "Second."); err != nil {
			t.Fatal(err)
		}
		text, ok, err := db.GetLLMNote(ctx, "wilant", "changes", "2026-10-20")
		if err != nil || !ok || text != "Second." {
			t.Fatalf("get after upsert = %q, %v, %v; want the second text", text, ok, err)
		}
		// A save over a claimed row makes the note readable.
		if _, err := db.ClaimLLMNote(ctx, "wilant", "weekly", "2026-10-19"); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveLLMNote(ctx, "wilant", "weekly", "2026-10-19", "Summary."); err != nil {
			t.Fatal(err)
		}
		if text, ok, _ := db.GetLLMNote(ctx, "wilant", "weekly", "2026-10-19"); !ok || text != "Summary." {
			t.Fatalf("get after saving over a claim = %q, %v", text, ok)
		}
	})
}

func TestPruneLLMNotes(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		for _, day := range []string{"2026-08-01", "2026-08-31", "2026-09-01", "2026-10-01"} {
			if err := db.SaveLLMNote(ctx, "wilant", "changes", day, "note"); err != nil {
				t.Fatal(err)
			}
		}
		for _, day := range []string{"2026-08-01", "2026-10-01"} {
			if err := db.SaveLLMNote(ctx, "other", "changes", day, "note"); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.PruneLLMNotes(ctx, "2026-09-01"); err != nil {
			t.Fatal(err)
		}
		for day, want := range map[string]bool{"2026-08-01": false, "2026-08-31": false, "2026-09-01": true, "2026-10-01": true} {
			if _, ok, _ := db.GetLLMNote(ctx, "wilant", "changes", day); ok != want {
				t.Errorf("wilant %s present = %v, want %v", day, ok, want)
			}
		}
		// The cut-off applies to every rider: old notes go, recent ones stay.
		if _, ok, _ := db.GetLLMNote(ctx, "other", "changes", "2026-08-01"); ok {
			t.Error("an old note of another rider survived the prune")
		}
		if _, ok, _ := db.GetLLMNote(ctx, "other", "changes", "2026-10-01"); !ok {
			t.Error("pruning deleted another rider's recent note")
		}
	})
}
