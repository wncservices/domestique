package api

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A rider whose cycling levels were seeded before endurance existed has rows
// for the sport, so the old "seed only when there are none" rule would never
// give them an endurance level.
func TestExistingRiderGetsEnduranceSeeded(t *testing.T) {
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Training: store}
	ctx := t.Context()
	if err := store.SaveLevel(ctx, workout.ProgressionLevel{Rider: "wilant", Sport: model.SportCycling, Zone: workout.ZoneThreshold, Level: 5.5, Reason: "ride"}); err != nil {
		t.Fatal(err)
	}

	profile := workout.RiderProfile{ExperienceLevel: "intermediate"}
	levels, err := s.levelsFor(ctx, "wilant", profile, model.SportCycling)
	if err != nil {
		t.Fatal(err)
	}
	if levels["endurance"] != 4.0 {
		t.Errorf("endurance = %v, want 4.0 from experience", levels["endurance"])
	}
	if levels["threshold"] != 5.5 {
		t.Errorf("threshold = %v, want the saved 5.5 untouched", levels["threshold"])
	}
	if _, ok := levels["tempo"]; !ok {
		t.Error("the other missing Initial zones are seeded too")
	}
	saved, err := store.ListLevels(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range saved {
		if l.Zone == workout.ZoneThreshold && l.Level != 5.5 {
			t.Errorf("threshold row = %v, want 5.5", l.Level)
		}
		if l.Zone == workout.ZoneEndurance && l.Reason != "Starting level from your experience" {
			t.Errorf("endurance reason = %q", l.Reason)
		}
	}
}
