package api_test

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// Between local midnight and UTC midnight the rider's today is already the next
// calendar date. The advisory must look at the day after the rider's today, the
// same one the rest of the pass is keyed by, not the day after the pod's.
func TestTomorrowAdvisoryUsesTheTrainingZonesToday(t *testing.T) {
	h := newTomorrowHarness(t)
	// 23:30 UTC on the 19th is 00:30 on the 20th in Brussels.
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 23, 30, 0, 0, time.UTC) }
	h.hard("2026-03-21")
	if _, err := h.conn.Exec(`INSERT INTO fitness_snapshots (rider, date, ctl, atl, tsb) VALUES ('wilant', ?, ?, ?, ?)`,
		"2026-03-20", 40.0, 100.0, -60.0); err != nil {
		t.Fatal(err)
	}
	var records []spyRecord
	h.srv.Log = slog.New(spyHandler{&records})

	h.srv.AdaptWorkouts(context.Background())

	for _, r := range records {
		if r.msg == "tomorrow's session may need easing" {
			return
		}
	}
	t.Fatalf("no advisory for the local tomorrow: %+v", records)
}
