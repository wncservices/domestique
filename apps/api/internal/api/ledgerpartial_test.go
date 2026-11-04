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

func debtOf(t *testing.T, h *ledgerHarness) float64 {
	t.Helper()
	row, _, err := h.store.GetLedgerWeek(context.Background(), "wilant", "2026-10-05")
	if err != nil {
		t.Fatal(err)
	}
	d, err := h.srv.WeekDebtForTest(context.Background(), "wilant", "2026-10-05", row)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func rideToday(t *testing.T, h *ledgerHarness, id, sport string, secs, load float64) {
	t.Helper()
	if _, err := h.store.UpsertSession(context.Background(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: id, Sport: sport,
		Date: reoptToday, DurationSeconds: secs, TrainingLoad: load,
	}); err != nil {
		t.Fatal(err)
	}
}

// openWithBigDebt opens the week and carries enough debt in that the floor at
// zero cannot hide a miscount.
func openWithBigDebt(t *testing.T, h *reoptHarness) {
	t.Helper()
	h.at(7, 7, 0)
	h.tick()
	if _, err := h.conn.Exec(`UPDATE load_ledger SET carried_in_tss = 900 WHERE rider = 'wilant' AND week_start = '2026-10-05'`); err != nil {
		t.Fatal(err)
	}
}

// One of today's two sessions is done and the other is not: the finished ride
// is the finished session's own and must not shave the unfinished one.
func TestAFinishedSessionsRideDoesNotShaveTheUnfinishedOne(t *testing.T) {
	h := newReoptHarness(t)
	long := h.plantEndurance(t, reoptToday, 3)
	openWithBigDebt(t, h)
	tempo := h.get(t, h.hard.ID)
	ftp := 250.0
	tempoTSS := workout.PlannedTSSOrHours(tempo, ftp)
	before := debtOf(t, h.ledgerHarness)

	rideToday(t, h.ledgerHarness, "done-tempo", "cycling", workout.PlannedSeconds(tempo.Steps), tempoTSS)

	// The tempo moved from still-ahead to ridden at its planned load; the long
	// ride is untouched and still counts in full.
	if after := debtOf(t, h.ledgerHarness); math.Abs(after-before) > 1e-6 {
		t.Fatalf("debt moved from %.2f to %.2f; the %.0f-TSS long ride was netted against the finished ride", before, after, workout.PlannedTSSOrHours(long, ftp))
	}
}

// A run is not the start of a cycling session.
func TestARideInAnotherSportDoesNotShaveTodaysSession(t *testing.T) {
	h := newReoptHarness(t)
	openWithBigDebt(t, h)
	before := debtOf(t, h.ledgerHarness)

	rideToday(t, h.ledgerHarness, "easy-run", "running", 600, 15)

	// Only the ridden load joins what is completed; nothing planned is removed.
	if after := debtOf(t, h.ledgerHarness); math.Abs((before-after)-15) > 1e-6 {
		t.Fatalf("debt moved from %.2f to %.2f, want a fall of exactly the 15 TSS ridden", before, after)
	}
}
