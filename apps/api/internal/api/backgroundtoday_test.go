package api_test

import (
	"context"
	"testing"
	"time"
)

// "Today" for the background passes is the deployment's training zone, the same
// one weather and the morning summary use, not the pod's UTC. At 22:30 UTC on
// Sunday 11 October it is already Monday 12th in Brussels: the tick must build
// the week that starts on the 12th, and nothing dated before it.
func TestTheTickBuildsFromTheTrainingZonesToday(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	for _, w := range h.all(t) {
		_ = h.store.DeleteWorkout(ctx, w.ID)
	}
	if _, err := h.conn.Exec(`DELETE FROM scheduled_weeks`); err != nil {
		t.Fatal(err)
	}
	h.now = time.Date(2026, time.October, 11, 22, 30, 0, 0, time.UTC)

	h.srv.AutoScheduleTick(ctx)

	all := h.all(t)
	if len(all) == 0 {
		t.Fatal("the tick built nothing")
	}
	for _, w := range all {
		if w.Date < "2026-10-12" {
			t.Errorf("%q is dated %s, a day that is already over in the training zone", w.Name, w.Date)
		}
	}
}
