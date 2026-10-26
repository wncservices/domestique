package api_test

import (
	"bytes"
	"strings"
	"testing"
)

// verdictLines are the log messages that, beside a rider's name, would say how
// that rider's morning went (a readiness verdict, an easing, owed load, a green
// push). AGENTS.md: once a verdict is known no line names the rider.
var verdictLines = []string{
	"workout adapted automatically",
	"tomorrow's session may need easing",
	"load repaid",
	"load ledger settled last week",
	"green push",
	"could not record why a change was made",
	"load ledger: could not update a workout",
	"adapt: could not step a workout down",
	"adapt: could not update a workout",
}

// assertNoRiderBesideAVerdict fails when a verdict-bearing line names the
// rider, and returns how many such lines there were so a caller can refuse a
// vacuous pass.
func assertNoRiderBesideAVerdict(t *testing.T, logs string, rider string) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		for _, msg := range verdictLines {
			if !strings.Contains(line, msg) {
				continue
			}
			n++
			if strings.Contains(line, rider) {
				t.Errorf("a log line names the rider beside a verdict:\n%s", line)
			}
		}
	}
	return n
}

func TestACautionMorningLogsNoRiderBesideTheVerdict(t *testing.T) {
	h := newReoptHarness(t)
	var logs bytes.Buffer
	h.srv.Log = scanLogger(&logs)
	h.cautionMorning(t)

	h.at(7, 7, 0)
	h.tick()

	if n := assertNoRiderBesideAVerdict(t, logs.String(), "wilant"); n == 0 {
		t.Fatalf("setup: no verdict line was logged at all:\n%s", logs.String())
	}
}

func TestAGreenMorningLogsNoRiderBesideTheRepayment(t *testing.T) {
	h := newLedgerPushHarness(t)
	var logs bytes.Buffer
	h.srv.Log = scanLogger(&logs)
	plantTodaysTempo(t, h.ledgerHarness, "2026-10-07", 3, 4)
	goodMorning(t, h.ledgerHarness, 7, h.push.garmin)

	h.at(7, 7, 0)
	h.tick()

	if n := assertNoRiderBesideAVerdict(t, logs.String(), "wilant"); n == 0 {
		t.Fatalf("setup: no repayment line was logged at all:\n%s", logs.String())
	}
}
