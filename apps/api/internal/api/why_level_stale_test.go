package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type levelWhyOut struct {
	Zone   string  `json:"zone"`
	Reason string  `json:"reason"`
	Why    *whyOut `json:"why"`
}

func levelWhyOfThreshold(t *testing.T, h *trainingHarness) levelWhyOut {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/progression", "")
	var out struct {
		Levels []levelWhyOut `json:"levels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, l := range out.Levels {
		if l.Zone == "threshold" {
			return l
		}
	}
	t.Fatal("no threshold level")
	return levelWhyOut{}
}

// TestARecalibrationWhyIsHiddenOnceARideMovedTheLevel: the level's reason is
// whatever moved it last, and a why that describes an earlier move would
// contradict it.
func TestARecalibrationWhyIsHiddenOnceARideMovedTheLevel(t *testing.T) {
	h := newTrainingHarness(t)
	ctx := context.Background()
	save := func(level float64, reason, at string) {
		if err := h.store.SaveLevel(ctx, workout.ProgressionLevel{
			Rider: "wilant", Sport: model.SportCycling, Zone: workout.ZoneThreshold, Level: level, Reason: reason, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	save(4.6, "FTP 250 → 268 W — threshold 5.3 → 4.6", "2026-05-01T00:00:00Z")
	rec := why.NewRecord(why.LevelRecalibration, "FTP 250 → 268 W — threshold 5.3 → 4.6", why.LevelRecalibrationInputs{FTPFrom: 250, FTPTo: 268, Zone: "threshold", LevelFrom: 5.3, LevelTo: 4.6, Trigger: "auto_applied"})
	if err := h.store.RecordAdjustment(ctx, "wilant", workout.SubjectLevel, "cycling:threshold", rec, "2026-06-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.conn.Exec(`UPDATE adjustments SET created_at = ?`, "2026-06-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}

	if l := levelWhyOfThreshold(t, h); l.Why == nil || l.Why.Rule != "level_recalibration" {
		t.Fatalf("control: the recalibration's why is missing: %+v", l)
	}

	save(4.9, "Nailed Threshold 3x8 (4.9) — threshold 4.6 → 4.9", "2026-07-01T00:00:00Z")
	l := levelWhyOfThreshold(t, h)
	if l.Why != nil {
		t.Errorf("a recalibration why is shown after a ride moved the level: %+v", l.Why)
	}
	if l.Reason != "Nailed Threshold 3x8 (4.9) — threshold 4.6 → 4.9" {
		t.Errorf("reason = %q, want the ride's own", l.Reason)
	}
}
