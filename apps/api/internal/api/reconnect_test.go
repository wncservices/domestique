package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/komoot"
	"github.com/wncservices/domestique/apps/api/internal/wahoo"
)

// A refused session is not an outage: the rider has to reconnect, and the UI
// can say so. 409 with a flag, not a generic 502.
func TestAnUpstreamThatRefusesTheSessionIsAReconnect(t *testing.T) {
	for _, tc := range []struct {
		upstream, provider string
		err                error
	}{
		{"Garmin", "garmin", fmt.Errorf("garmin course import: %w", garmin.ErrSessionRefused)},
		{"Wahoo", "wahoo", fmt.Errorf("wahoo: list routes: %w", wahoo.ErrSessionRefused)},
		{"Komoot", "komoot", fmt.Errorf("komoot tours: %w", komoot.ErrSessionRefused)},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			var logs bytes.Buffer
			s := &Server{Log: slog.New(slog.NewTextHandler(&logs, nil))}
			rec := httptest.NewRecorder()
			s.failUpstream(rec, tc.upstream, tc.err)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", rec.Code)
			}
			var body struct {
				Error     string `json:"error"`
				Reconnect bool   `json:"reconnect"`
				Provider  string `json:"provider"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != tc.upstream+" needs reconnecting" || !body.Reconnect || body.Provider != tc.provider {
				t.Fatalf("body = %+v", body)
			}
			if !strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("not logged at Warn: %s", logs.String())
			}
		})
	}

	// Anything else stays the generic 502.
	s := &Server{Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	rec := httptest.NewRecorder()
	s.failUpstream(rec, "Garmin", errors.New("garmin: course import returned 500"))
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "reconnect") {
		t.Fatalf("an ordinary failure: status = %d body = %s", rec.Code, rec.Body.String())
	}
}
