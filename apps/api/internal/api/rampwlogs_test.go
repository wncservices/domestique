package api_test

import (
	"bytes"
	"context"
	"testing"
)

// A return ramp follows an illness; the rule id beside a rider's name would say
// the rider was ill.
func TestTheReturnRampLogsNoRider(t *testing.T) {
	h := newOnceHarness(t)
	var logs bytes.Buffer
	h.srv.Log = scanLogger(&logs)
	h.event(t, "illness", "2026-10-04", "2026-10-06", "proper")
	h.hardSession(t, "2026-10-08", 90)

	h.srv.AdaptWorkouts(context.Background())

	if n := assertNoRiderBesideAVerdict(t, logs.String(), "wilant"); n == 0 {
		t.Fatalf("setup: no ramp line was logged:\n%s", logs.String())
	}
}
