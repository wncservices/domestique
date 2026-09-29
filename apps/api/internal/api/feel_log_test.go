package api_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// TestRatingARideLogsNoEffortValue is the privacy rule for the survey: how a
// ride felt is the rider's own health information, and it does not go in a log
// line beside their name. The line names the rider and the session, nothing else.
func TestRatingARideLogsNoEffortValue(t *testing.T) {
	h := newTrainingHarness(t)
	seedThresholdWorkoutAndAnalysis(t, h, "wilant", "sess-log", "nailed", 5.0)
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))

	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/sessions/sess-log/feel", `{"feel":5}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	out := logs.String()
	if !strings.Contains(out, "session feel recorded") || !strings.Contains(out, "rider=wilant") || !strings.Contains(out, "session=sess-log") {
		t.Fatalf("the feel log line is missing or lost its rider or session:\n%s", out)
	}
	for _, banned := range []string{"feel=", "effort", "=5"} {
		if strings.Contains(out, banned) {
			t.Errorf("log contains %q beside a rider:\n%s", banned, out)
		}
	}
}
