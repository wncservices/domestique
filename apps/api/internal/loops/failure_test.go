package loops_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/loops"
	"github.com/wncservices/domestique/apps/api/internal/routing"
)

func TestFailureClassNamesTheCauseWithoutTheEnginesWords(t *testing.T) {
	body := "Unable to find a route for point (47.377, 8.542)"
	cases := map[string]error{
		"quota":      fmt.Errorf("routing service returned 429 Too Many Requests: %s", body),
		"auth":       fmt.Errorf("routing service returned 403 Forbidden: %s", body),
		"unroutable": fmt.Errorf("routing service returned 404 Not Found: %s", body),
		"outage":     fmt.Errorf("routing service returned 503 Service Unavailable: %s", body),
		"canceled":   fmt.Errorf("wrapped: %w", context.Canceled),
		"timeout":    fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
		"engine":     errors.New("dial tcp: connection refused"),
	}
	cases["unroutable-2"] = errors.New("routing service returned no usable route")
	for want, err := range cases {
		got := loops.FailureClass(err)
		if want == "unroutable-2" {
			want = "unroutable"
		}
		if got != want {
			t.Errorf("FailureClass(%v) = %q, want %q", err, got, want)
		}
		if strings.ContainsAny(got, "0123456789(") {
			t.Errorf("class %q carries engine text", got)
		}
	}
	if loops.FailureClass(nil) != "" {
		t.Error("a nil error has a class")
	}
}

func TestGenerateSkipsTheSecondRoundWhenEveryCalibrationCallFailed(t *testing.T) {
	eng := &fakeEngine{fail: func(int) error { return errors.New("routing service returned 429 Too Many Requests") }}
	r := request(distanceObjective{target: 20000})
	r.StopWhenCalibrationFails = true
	got, stats := loops.Generate(context.Background(), eng, r)
	if len(got) != 0 || len(eng.calls) != 5 {
		t.Errorf("%d calls, %d loops: a dead engine should cost only the calibration round (5 calls)", len(eng.calls), len(got))
	}
	if stats.Attempts != 5 || len(stats.Failures) != 5 || stats.LastErr == nil {
		t.Errorf("stats = %+v, want the 5 failures reported", stats)
	}
}

// The route builder's suggest keeps spending its whole budget (a test in api
// pins it), so the stop is opt-in.
func TestGenerateWithoutTheFlagStillRunsTheSecondRound(t *testing.T) {
	eng := &fakeEngine{fail: func(seed int) error {
		if seed <= 105 {
			return errors.New("engine down")
		}
		return nil
	}}
	_, _ = loops.Generate(context.Background(), eng, request(distanceObjective{target: 20000}))
	if len(eng.calls) != 15 {
		t.Errorf("%d calls, want all 15 without StopWhenCalibrationFails", len(eng.calls))
	}
}

func TestGenerateStillRunsTheSecondRoundWhenSomeCalibrationCallsWork(t *testing.T) {
	eng := &fakeEngine{overrun: 1.0, fail: func(seed int) error {
		if seed <= 104 { // four of five fail
			return errors.New("engine down")
		}
		return nil
	}}
	_, _ = loops.Generate(context.Background(), eng, request(distanceObjective{target: 20000}))
	if len(eng.calls) != 15 {
		t.Errorf("%d calls, want both rounds when one calibration seed worked", len(eng.calls))
	}
}

var _ = routing.LatLng{}
