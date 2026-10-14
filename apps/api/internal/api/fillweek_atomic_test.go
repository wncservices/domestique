package api_test

import (
	"context"
	"testing"
)

func (h *onceHarness) recordedWeeks(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.conn.QueryRow(`SELECT COUNT(*) FROM scheduled_weeks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A store that fails on a week's third session must not leave a half-built week
// recorded as filled: the next pass would adopt it as a legacy week and the
// missing sessions would never be made.
func TestAWeekWhoseThirdSessionFailsIsNotRecordedAndIsCompletedLater(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	for _, w := range h.all(t) {
		_ = h.store.DeleteWorkout(ctx, w.ID)
	}
	if _, err := h.conn.Exec(`DELETE FROM scheduled_weeks`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.conn.Exec(`CREATE TRIGGER third_insert_fails BEFORE INSERT ON workouts
		WHEN (SELECT COUNT(*) FROM workouts) = 2 BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(ctx)

	if got := len(h.all(t)); got != 0 {
		t.Errorf("%d sessions survived the failed fills, want none", got)
	}
	if got := h.recordedWeeks(t); got != 0 {
		t.Errorf("%d weeks recorded as filled after the failure, want none", got)
	}

	if _, err := h.conn.Exec(`DROP TRIGGER third_insert_fails`); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)

	monday := h.now.AddDate(0, 0, -2) // Wednesday 7th: its week starts on the 5th
	if got := len(h.week(t, monday)); got < 3 {
		t.Errorf("the current week holds %d sessions after the retry, want it completed (at least 3)", got)
	}
	if h.recordedWeeks(t) == 0 {
		t.Error("the retry recorded no weeks")
	}
}
