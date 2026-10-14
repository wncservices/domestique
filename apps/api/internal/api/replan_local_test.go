package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// At 22:30 UTC on Sunday 11 October it is already Monday 12th in the training
// zone. "Re-plan this week" must act on the week the rider is living in (12th to
// 18th): delete its plan-made sessions and build them again, not look at the
// week that ended in Brussels half an hour ago.
func TestReplanAfterLocalMidnightRebuildsTheSameLocalWeek(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	h.now = time.Date(2026, time.October, 11, 22, 30, 0, 0, time.UTC)
	h.srv.AutoScheduleTick(ctx)

	dates := func() []string {
		var out []string
		for _, w := range h.all(t) {
			if w.Date >= "2026-10-12" && w.Date <= "2026-10-18" {
				out = append(out, w.Date)
			}
		}
		sort.Strings(out)
		return out
	}
	before := dates()
	if len(before) == 0 {
		t.Fatal("setup: the tick built nothing for the local week")
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replan status %d", resp.StatusCode)
	}
	var out struct{ Removed, Created int }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	after := dates()
	if out.Removed != len(before) {
		t.Errorf("replan removed %d sessions, want the %d of the local week", out.Removed, len(before))
	}
	if strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("week after replan = %v, want the same days as before (%v)", after, before)
	}
	for _, w := range h.all(t) {
		if w.Date < "2026-10-12" {
			t.Errorf("%q is dated %s, before the local today", w.Name, w.Date)
		}
	}
}
