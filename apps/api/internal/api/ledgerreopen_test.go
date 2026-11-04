package api_test

import (
	"context"
	"testing"
)

// A week whose target is legitimately 0 (nothing built in it) is opened once.
// "Not opened yet" used to be read off target == 0, so it was reopened, and its
// carried-in debt recomputed, every morning of the week.
func TestAZeroTargetWeekIsOpenedOnce(t *testing.T) {
	h := newLedgerHarness(t, 250)
	ctx := context.Background()
	for _, w := range h.thisWeek(t) {
		if err := h.store.DeleteWorkout(ctx, w.ID); err != nil {
			t.Fatal(err)
		}
	}

	h.at(6, 6, 45)
	h.tick()
	row, ok, err := h.store.GetLedgerWeek(ctx, "wilant", "2026-10-05")
	if err != nil || !ok || row.TargetTSS != 0 {
		t.Fatalf("setup: want an opened week with a zero target, got %+v ok=%v err=%v", row, ok, err)
	}
	// Anything a reopening would overwrite.
	if _, err := h.conn.Exec(`UPDATE load_ledger SET carried_in_tss = 77 WHERE rider = 'wilant' AND week_start = '2026-10-05'`); err != nil {
		t.Fatal(err)
	}

	h.at(7, 6, 45)
	h.tick()

	row, _, _ = h.store.GetLedgerWeek(ctx, "wilant", "2026-10-05")
	if row.CarriedInTSS != 77 {
		t.Fatalf("the week was reopened: carried in = %v, want 77", row.CarriedInTSS)
	}
}
