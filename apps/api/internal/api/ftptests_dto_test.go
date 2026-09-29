package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The workout DTO says a test could not be read (so the day card can), without
// leaking the -1 marker as a wattage.
func TestWorkoutDTOFlagsAnUnreadableTestWithoutLeakingTheMarker(t *testing.T) {
	h := newFTPResultHarness(t, "ramp", 0, workout.RiderProfile{FTPWatts: 250})
	h.sync() // a decoded ride with no power: unreadable

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+h.testID, "")
	var out struct {
		TestResultWatts float64 `json:"testResultWatts"`
		TestUnreadable  bool    `json:"testUnreadable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.TestUnreadable || out.TestResultWatts != 0 {
		t.Errorf("dto = %+v, want testUnreadable and no wattage", out)
	}
}
