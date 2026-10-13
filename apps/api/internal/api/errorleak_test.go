package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const leakyDetail = `pq: duplicate key value violates unique constraint "routes_pkey" (host db-0.internal:5432)`

// fail is the catch-all for an error nobody planned for. Its text can name a
// table, a driver or an internal host, so the client gets a fixed message and
// the detail goes to the log, at Error: something that should work did not.
func TestFailReturnsAGenericMessageAndLogsTheDetail(t *testing.T) {
	var logs bytes.Buffer
	s := &Server{Log: slog.New(slog.NewTextHandler(&logs, nil))}

	rec := httptest.NewRecorder()
	s.fail(rec, errors.New(leakyDetail))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"internal error"}` {
		t.Fatalf("body = %s", got)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "routes_pkey") {
		t.Fatalf("the detail is not in the log at Error: %s", logs.String())
	}
}

// A lookup that finds nothing is a deliberate 404 the rider can act on; any
// other failure falls through to the generic one.
func TestFailLookupKeepsTheNotFoundMessageButNotOtherErrors(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	rec := httptest.NewRecorder()
	s.failLookup(rec, errors.New(leakyDetail))
	if strings.Contains(rec.Body.String(), "routes_pkey") {
		t.Fatalf("body leaks the detail: %s", rec.Body.String())
	}
}

// failUpstream is for a third party that refused or failed: the rider is told
// who, never what they said (a response body can carry tokens, hosts, ids).
func TestFailUpstreamNamesTheServiceNotTheError(t *testing.T) {
	var logs bytes.Buffer
	s := &Server{Log: slog.New(slog.NewTextHandler(&logs, nil))}

	rec := httptest.NewRecorder()
	s.failUpstream(rec, "Komoot", errors.New(`komoot: 403 {"token":"abc123","host":"internal"}`))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "abc123") || strings.Contains(body, "internal") {
		t.Fatalf("body leaks the upstream's answer: %s", body)
	}
	if !strings.Contains(body, "Komoot") {
		t.Fatalf("body does not say which service failed: %s", body)
	}
	if !strings.Contains(logs.String(), "abc123") {
		t.Fatalf("the detail is not in the log: %s", logs.String())
	}
}
