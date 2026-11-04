package api_test

import (
	"strings"
	"testing"
)

// The plan starts at the current Monday, so last week is no longer in it when
// the Monday summary is written; its phase must still reach the model.
func TestWeeklySummaryNamesLastWeeksPhase(t *testing.T) {
	h := newLedgerHarness(t, 250)
	f := withLLM(t, h.srv, fixed(weeklyReply))

	h.at(12, 6, 45)
	morningPass(h)

	if body := string(f.last); !strings.Contains(body, " phase") {
		t.Fatalf("the weekly request names no phase:\n%s", body)
	}
}
