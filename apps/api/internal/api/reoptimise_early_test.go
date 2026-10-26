package api_test

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
)

// Wellness reaches the server when the watch syncs on waking, which can be
// before the first slot. A tick then must not ease today's hard session in
// place: that would make it un-swappable (it is no longer untouched) and the
// 06:30 pass would find nothing to trade.
func TestAnEarlyTickDoesNotPreemptTheMorningSwap(t *testing.T) {
	h := newReoptHarness(t)
	h.cautionMorning(t)

	h.at(7, 5, 30)
	h.tick()
	if hard := h.get(t, h.hard.ID); hard.Date != reoptToday || len(hard.Flags.EasedBy()) != 0 || !scheduler.IsGenerated(hard) {
		t.Fatalf("a tick before the first slot eased today's session: %+v", hard.Flags)
	}

	h.at(7, 6, 30)
	h.tick()
	hard, easy := h.get(t, h.hard.ID), h.get(t, h.easy.ID)
	if hard.Date != reoptFri || easy.Date != reoptToday {
		t.Fatalf("dates after the first slot: hard %s, easy %s; want %s and %s", hard.Date, easy.Date, reoptFri, reoptToday)
	}
}
