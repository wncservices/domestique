package api_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/geocoding"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

// A failed place search must not write what the rider typed to the log, at
// any level, nor log the failure twice (audit N3).
func TestGeocodeFailureLogsNoAddress(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	srv := &api.Server{
		Source: db, Store: st, Config: &config.Config{},
		Geocoder: geocoding.New(dead.URL),
		Log:      slog.New(slog.NewTextHandler(&logs, nil)),
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/api/geocode?q=12+Rue+Privee%2C+Home+Town")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(logs.String(), "Privee") || strings.Contains(logs.String(), "Rue") {
		t.Errorf("logs carry the typed address:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("an upstream being down is a Warn, got:\n%s", logs.String())
	}
}
