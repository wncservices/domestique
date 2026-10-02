package api

import (
	"context"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A refreshed session with neither a zone nor a name used to slice an empty
// string while building its sentence.
func TestASeasonRefreshWithNoZoneOrNameStillGetsASentence(t *testing.T) {
	h := newRecalHarness(t)
	h.s.recordSeasonRefresh(context.Background(), "wilant",
		workout.Workout{ID: "w1", Date: "2026-03-02", Level: 1},
		workout.CreateWorkoutRequest{Level: 2}, 250)
	got, err := h.db.LatestAdjustments(context.Background(), "wilant", workout.SubjectWorkout, []string{"w1"})
	if err != nil || got["w1"].Text == "" {
		t.Fatalf("adjustment = %+v, %v; want a non-empty sentence", got["w1"], err)
	}
}

// An auto-applied threshold whose finding had no reason of its own still gets
// a sentence, since the popover shows the text first.
func TestAnAutoAppliedThresholdWithNoReasonStillGetsASentence(t *testing.T) {
	h := newRecalHarness(t)
	h.s.recordDetectedThresholds(context.Background(), "wilant", []detectedThresholdDTO{
		{Field: "ftp", Value: 262, previous: 250},
		{Field: "max_hr", Value: 188, fromTest: false},
	})
	got, err := h.db.LatestAdjustments(context.Background(), "wilant", workout.SubjectLevel, []string{"profile:ftp", "profile:max_hr"})
	if err != nil || len(got) != 2 {
		t.Fatalf("adjustments = %+v, %v", got, err)
	}
	if got["profile:ftp"].Text != "FTP updated from 250 to 262 (from your rides)." {
		t.Errorf("ftp text = %q", got["profile:ftp"].Text)
	}
	if got["profile:max_hr"].Text != "Max heart rate set to 188 (from your rides)." {
		t.Errorf("max hr text = %q", got["profile:max_hr"].Text)
	}
}
