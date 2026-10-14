package wahoo

import (
	"net/http"
	"testing"
)

// The ascent field's form is unconfirmed, and a figure that cannot be read is a
// missing figure: it must never abort the whole workout list (every ride in it
// would then fail to sync).
func TestAnUnreadableAscentIsZeroAndNeverAbortsTheList(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workouts": [
			{"id": 1, "name": "Text", "starts": "2026-01-01T00:00:00Z", "minutes": 30,
			 "workout_summary": {"ascent_accum": "n/a", "power_avg": "190.0"}},
			{"id": 2, "name": "Object", "starts": "2026-01-02T00:00:00Z", "minutes": 30,
			 "workout_summary": {"ascent_accum": {"value": 5}}},
			{"id": 3, "name": "Good", "starts": "2026-01-03T00:00:00Z", "minutes": 30,
			 "workout_summary": {"ascent_accum": "88"}}
		]}`))
	})
	got, err := c.ListWorkouts(t.Context(), "at", 1, 30)
	if err != nil {
		t.Fatalf("an unreadable ascent aborted the list: %v", err)
	}
	if len(got) != 3 || got[0].AscentM != 0 || got[1].AscentM != 0 || got[2].AscentM != 88 || got[0].AvgPowerWatts != 190 {
		t.Errorf("workouts = %+v, want zero for the two unreadable and 88 for the good one", got)
	}
}
