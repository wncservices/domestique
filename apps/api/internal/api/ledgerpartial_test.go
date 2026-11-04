package api_test

import (
	"context"
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A ride that covers too little of today's session for it to count as done is
// neither a finished session nor an untouched one. Its load is ridden and the
// rest of the session is still ahead; counting the full planned load as well as
// the ride understated what the week owes until the day ended.
func TestAPartlyRiddenSessionTodayIsNotCountedTwice(t *testing.T) {
	h := newLedgerHarness(t, 250)
	ctx := context.Background()
	h.at(7, 6, 45)
	h.tick() // opens the week

	debt := func() float64 {
		row, _, err := h.store.GetLedgerWeek(ctx, "wilant", "2026-10-05")
		if err != nil {
			t.Fatal(err)
		}
		d, err := h.srv.WeekDebtForTest(ctx, "wilant", "2026-10-05", row)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	before := debt()
	// Ten minutes on a day whose session is far longer: not "done".
	if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "partial", Sport: "cycling",
		Date: "2026-10-07", DurationSeconds: 600, TrainingLoad: 15,
	}); err != nil {
		t.Fatal(err)
	}
	after := debt()
	// The ride is part of the session still planned, not on top of it.
	if math.Abs(after-before) > 1e-6 {
		t.Fatalf("debt moved from %.2f to %.2f when part of today's session was ridden", before, after)
	}
}
