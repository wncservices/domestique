package wahoo

import (
	"net/http"
	"testing"
)

// ascent_accum is the workout's climbing in metres, string or number like the
// summary's other figures; a workout without one reads 0 (not reported).
func TestListWorkoutsReadsTheAccumulatedAscent(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workouts": [
			{"id": 1, "name": "String", "starts": "2026-01-01T00:00:00Z", "minutes": 30,
			 "workout_summary": {"ascent_accum": "312.5"}},
			{"id": 2, "name": "Number", "starts": "2026-01-02T00:00:00Z", "minutes": 30,
			 "workout_summary": {"ascent_accum": 120}},
			{"id": 3, "name": "None", "starts": "2026-01-03T00:00:00Z", "minutes": 30,
			 "workout_summary": {"power_avg": "190.0"}}
		]}`))
	})
	got, err := c.ListWorkouts(t.Context(), "at", 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].AscentM != 312.5 || got[1].AscentM != 120 || got[2].AscentM != 0 {
		t.Errorf("ascents = %v, %v, %v; want 312.5, 120, 0", got[0].AscentM, got[1].AscentM, got[2].AscentM)
	}
}
