package api

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// An imported ride has no provider to fetch a file from. If the sync's
// analysis pass treated one that lost its analysis as a candidate it would try
// to download it, fail with "unknown provider" on every sync, and write an
// analysis from the summary numbers alone over the proper one's place.
func TestSyncAnalysisNeverTriesToFetchAnImportedRide(t *testing.T) {
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s := &Server{Training: store, Log: slog.New(slog.NewTextHandler(&logs, nil))}

	today := time.Now().Format("2006-01-02")
	if _, err := store.UpsertSession(t.Context(), workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "import", ExternalID: "1-abc", Sport: "cycling", Date: today, DurationSeconds: 3600, AvgPowerWatts: 200,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.analyseNewSessions(t.Context(), "wilant", workout.RiderProfile{Rider: "wilant", FTPWatts: 250},
		nil, GarminConsumer{}, garmin.Session{}, false, "", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "fit download failed") {
		t.Errorf("the pass tried to fetch an imported ride:\n%s", logs.String())
	}
	if analyses, _ := store.ListAnalyses(t.Context(), "wilant", "2000-01-01"); len(analyses) != 0 {
		t.Errorf("the pass wrote %d analyses for an imported ride from nothing but its summary", len(analyses))
	}
}
