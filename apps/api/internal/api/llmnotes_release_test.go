package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func claimRows(t *testing.T, h *ledgerHarness, kind string) int {
	t.Helper()
	var n int
	if err := h.conn.QueryRow(`SELECT COUNT(1) FROM llm_notes WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A pass cut short by shutdown must not leave the day's note claimed forever.
func TestACancelledChangesNoteReleasesItsClaim(t *testing.T) {
	h := newLedgerHarness(t, 250)
	recordCaution(t, h, firstWorkoutIDs(t, h, 1)[0], "2026-10-06")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.at(6, 6, 45)
	h.tick() // no model yet, so the tick's own notes pass does nothing
	h.srv.WaitForBackground()
	f := withLLM(t, h.srv, func(string, []byte) string {
		cancel()
		time.Sleep(200 * time.Millisecond)
		return explainReply
	})
	h.srv.RunLLMNotesForTest(ctx)

	if n := claimRows(t, h, workout.LLMNoteChanges); n != 0 {
		t.Fatalf("claim rows after a cancelled call = %d, want 0", n)
	}

	f.reply = fixed(explainReply)
	h.srv.RunLLMNotesForTest(context.Background())
	if got, ok := storedNote(t, h, workout.LLMNoteChanges, "2026-10-06"); !ok || got != explainText {
		t.Fatalf("the retry did not write the note: %q (ok %v)", got, ok)
	}
}

// A save that fails leaves nothing claimed, so the next tick retries.
func TestAFailedSaveReleasesTheClaim(t *testing.T) {
	h := newLedgerHarness(t, 250)
	recordCaution(t, h, firstWorkoutIDs(t, h, 1)[0], "2026-10-06")
	h.at(6, 6, 45)
	h.tick()
	h.srv.WaitForBackground()
	withLLM(t, h.srv, fixed(explainReply))
	if _, err := h.conn.Exec(`CREATE TRIGGER no_note_save BEFORE UPDATE ON llm_notes BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}

	h.srv.RunLLMNotesForTest(context.Background())
	if n := claimRows(t, h, workout.LLMNoteChanges); n != 0 {
		t.Fatalf("claim rows after a failed save = %d, want 0", n)
	}

	if _, err := h.conn.Exec(`DROP TRIGGER no_note_save`); err != nil {
		t.Fatal(err)
	}
	h.srv.RunLLMNotesForTest(context.Background())
	if got, ok := storedNote(t, h, workout.LLMNoteChanges, "2026-10-06"); !ok || got != explainText {
		t.Fatalf("the retry did not write the note: %q (ok %v)", got, ok)
	}
}

// A model that answers badly is not asked again the same day.
func TestAModelFailureStillKeepsItsClaim(t *testing.T) {
	h := newLedgerHarness(t, 250)
	recordCaution(t, h, firstWorkoutIDs(t, h, 1)[0], "2026-10-06")
	f := withLLM(t, h.srv, fixed(explainReply))
	f.code = 500

	h.at(6, 6, 45)
	morningPass(h)
	calls := f.calls.Load()
	morningPass(h)

	if f.calls.Load() != calls {
		t.Fatalf("the model was called again after failing: %d -> %d", calls, f.calls.Load())
	}
	if _, ok := storedNote(t, h, workout.LLMNoteChanges, "2026-10-06"); !ok {
		t.Fatal("the rule-title fallback was not saved")
	}
}
