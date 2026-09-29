package api_test

import (
	"context"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A test ridden and read a few days ago silences the banner even when it gave
// nothing usable (so FTP was not re-verified): the rider just tested.
func TestARecentlyReadTestSilencesTheBannerEvenIfUnreadable(t *testing.T) {
	h := newTrainingHarness(t)
	seedRiderWithStaleFTP(t, h, "wilant") // clock 2031-03-18, FTP verified long ago
	if getFTPTests(t, h, "wilant").Suggestion == nil {
		t.Fatal("precondition: a stale FTP should be suggested")
	}
	ctx := context.Background()
	w, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Name: "FTP Test (ramp)", Date: "2031-03-16", TestProtocol: "ramp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SetTestResult(ctx, w.ID, -1); err != nil {
		t.Fatal(err)
	}
	if got := getFTPTests(t, h, "wilant"); got.Suggestion != nil {
		t.Errorf("suggestion = %+v two days after a test", got.Suggestion)
	}
}
