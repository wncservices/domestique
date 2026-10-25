package api_test

import (
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Audit N7: both proposal endpoints took a note of up to 256 KiB straight into
// a paid model prompt. Plan edits already cap their sentence at 300 runes.
func TestProposalNotesAreCappedBeforeTheModelSeesThem(t *testing.T) {
	for _, path := range []string{"/api/training/profile/propose", "/api/training/goals/propose"} {
		t.Run(path, func(t *testing.T) {
			h := newTrainingHarness(t)
			var calls atomic.Int32
			withLLM(t, h.srv, func(string, []byte) string {
				calls.Add(1)
				return "unused"
			})

			long := strings.Repeat("é", 501) // 501 runes, 1002 bytes: the cap counts characters
			resp := h.as("wilant", "cyclists", http.MethodPost, path, `{"note":"`+long+`"}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			if calls.Load() != 0 {
				t.Error("the model was called with an over-long note")
			}
			// 500 runes is fine: it gets as far as the model.
			resp = h.as("wilant", "cyclists", http.MethodPost, path, `{"note":"`+strings.Repeat("é", 500)+`"}`)
			if resp.StatusCode == http.StatusBadRequest {
				t.Errorf("a 500-character note was refused: %s", readAll(t, resp))
			}
			if calls.Load() == 0 {
				t.Error("a note at the cap never reached the model")
			}
		})
	}
}

// A gateway's error body can quote the request, which holds the rider's note
// ("I am ill"). The client once put up to 4 KiB of it in the error and the
// handlers log the error. Status only now.
func TestNarrationFailureLogsNeverQuoteTheNote(t *testing.T) {
	for _, path := range []string{"/api/training/profile/propose", "/api/training/goals/propose"} {
		t.Run(path, func(t *testing.T) {
			h := newTrainingHarness(t)
			f := withLLM(t, h.srv, fixed(""))
			f.code = http.StatusBadRequest
			var logs strings.Builder
			h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))

			resp := h.as("wilant", "cyclists", http.MethodPost, path, `{"note":"I am recovering from surgery"}`)
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", resp.StatusCode)
			}
			if strings.Contains(logs.String(), "surgery") {
				t.Errorf("logs quote the rider's note:\n%s", logs.String())
			}
			if !strings.Contains(logs.String(), "400") {
				t.Errorf("logs = %s, want the upstream status", logs.String())
			}
		})
	}
}
