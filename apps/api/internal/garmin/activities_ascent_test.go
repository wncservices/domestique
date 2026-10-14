package garmin

import (
	"net/http"
	"testing"
)

// The activity list carries the ride's climbing as elevationGain (metres); a
// ride without one reads 0, which the season review treats as "not reported",
// never "flat".
func TestActivitiesReadTheReportedElevationGain(t *testing.T) {
	c := activityFake(t, http.StatusOK, `[
		{"activityId":1,"startTimeLocal":"2026-03-01 07:30:00","activityType":{"typeKey":"road_biking"},"duration":3600,"elevationGain":412.4},
		{"activityId":2,"startTimeLocal":"2026-03-02 07:30:00","activityType":{"typeKey":"indoor_cycling"},"duration":3600}
	]`)
	got, err := c.Activities(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].AscentM != 412.4 {
		t.Errorf("reported ascent = %v, want 412.4", got[0].AscentM)
	}
	if got[1].AscentM != 0 {
		t.Errorf("an indoor ride with no elevation field = %v, want 0", got[1].AscentM)
	}
}
