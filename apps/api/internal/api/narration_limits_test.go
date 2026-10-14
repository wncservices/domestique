package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/narration"
)

// Audit N7: both proposal endpoints took a note of up to 256 KiB straight into
// a paid model prompt. Plan edits already cap their sentence at 300 runes.
func TestProposalNotesAreCappedBeforeTheModelSeesThem(t *testing.T) {
	for _, path := range []string{"/api/training/profile/propose", "/api/training/goals/propose"} {
		t.Run(path, func(t *testing.T) {
			h := newTrainingHarness(t)
			var calls atomic.Int32
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, "unused", http.StatusInternalServerError)
			}))
			defer fake.Close()
			client := narration.New("test-key")
			client.APIBase = fake.URL
			h.srv.Narration = client

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

// An Anthropic error body can quote the request, which holds the rider's note
// ("I am ill"). complete() put up to 4 KiB of it in the error and the handlers
// log the error. Status and type only now.
func TestNarrationFailureLogsNeverQuoteTheNote(t *testing.T) {
	for _, path := range []string{"/api/training/profile/propose", "/api/training/goals/propose"} {
		t.Run(path, func(t *testing.T) {
			h := newTrainingHarness(t)
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad prompt: ` + strings.ReplaceAll(string(body), `"`, `'`) + `"}}`))
			}))
			defer fake.Close()
			client := narration.New("test-key")
			client.APIBase = fake.URL
			h.srv.Narration = client
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
