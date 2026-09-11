package wahoo

import (
	"net/http"
	"testing"
)

func TestListWorkoutsParsesTheSummary(t *testing.T) {
	var gotAuth, gotQuery string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/workouts" {
			t.Errorf("%s %s, want GET /v1/workouts", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{
			"workouts": [
				{
					"id": 42, "name": "Morning Ride", "starts": "2026-03-01T07:30:00Z", "minutes": 58,
					"workout_summary": {
						"duration_total_accum": "3600.5", "distance_accum": "30000.0",
						"heart_rate_avg": "142.0", "power_bike_avg": "215.0"
					}
				},
				{
					"id": 43, "name": "No Summary Yet", "starts": "2026-03-02T07:00:00Z", "minutes": 20
				}
			],
			"total": 2, "page": 1, "per_page": 30
		}`))
	})

	workouts, err := c.ListWorkouts(t.Context(), "at", 1, 30)
	if err != nil {
		t.Fatalf("ListWorkouts: %v", err)
	}
	if gotAuth != "Bearer at" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotQuery != "page=1&per_page=30" {
		t.Errorf("query = %q", gotQuery)
	}
	if len(workouts) != 2 {
		t.Fatalf("got %d workouts, want 2", len(workouts))
	}

	ride := workouts[0]
	if ride.ID != "42" || ride.Name != "Morning Ride" {
		t.Errorf("ride = %+v", ride)
	}
	if ride.DurationSeconds != 3600.5 || ride.DistanceM != 30000 || ride.AvgHR != 142 || ride.AvgPowerWatts != 215 {
		t.Errorf("ride metrics = %+v", ride)
	}
	if ride.Starts.Format("2006-01-02") != "2026-03-01" {
		t.Errorf("ride starts = %v", ride.Starts)
	}

	// No workout_summary yet: falls back to the list item's own `minutes`
	// for duration, and zero for everything the summary alone carries.
	noSummary := workouts[1]
	if noSummary.DurationSeconds != 20*60 {
		t.Errorf("no-summary duration = %v, want 1200 (20 minutes)", noSummary.DurationSeconds)
	}
	if noSummary.DistanceM != 0 || noSummary.AvgHR != 0 {
		t.Errorf("no-summary metrics should be zero: %+v", noSummary)
	}
}

// The power field's name is the one confirmed discrepancy between the two
// secondary sources this was built from — power_bike_avg vs. power_avg.
// Both must work.
func TestListWorkoutsAcceptsEitherPowerFieldSpelling(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workouts": [
			{"id": 1, "name": "A", "starts": "2026-01-01T00:00:00Z", "minutes": 30,
			 "workout_summary": {"power_avg": "190.0"}}
		]}`))
	})
	workouts, err := c.ListWorkouts(t.Context(), "at", 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(workouts) != 1 || workouts[0].AvgPowerWatts != 190 {
		t.Errorf("workouts = %+v, want avg power 190 from the power_avg spelling", workouts)
	}
}

func TestListWorkoutsDefaultsPageAndPerPage(t *testing.T) {
	var gotQuery string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"workouts": []}`))
	})
	if _, err := c.ListWorkouts(t.Context(), "at", 0, 0); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "page=1&per_page=30" {
		t.Errorf("query = %q, want the defaults", gotQuery)
	}
}

func TestListWorkoutsSurfacesAnUpstreamFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, err := c.ListWorkouts(t.Context(), "at", 1, 30); err == nil {
		t.Fatal("expected an error for a 401")
	}
}
