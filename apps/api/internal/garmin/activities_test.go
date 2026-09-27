package garmin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func activityFake(t *testing.T, status int, response string) *Client {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc(activityListPath, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("limit"); got == "" {
			t.Error("expected a limit query param")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, response)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1"})
	return c
}

func TestActivitiesDecodesTheSummaryList(t *testing.T) {
	c := activityFake(t, http.StatusOK, `[
		{"activityId":123,"activityName":"Morning Ride","startTimeLocal":"2026-03-01 07:30:00",
		 "activityType":{"typeKey":"road_biking"},"duration":3600,"distance":30000,"averageHR":142,"avgPower":215,
		 "normPower":230,"trainingStressScore":85.5,"intensityFactor":0.82,
		 "maxAvgPower_5":950,"maxAvgPower_60":410,"maxAvgPower_300":280,"maxAvgPower_1200":240,"maxAvgPower_3600":220},
		{"activityId":456,"activityName":"Easy Run","startTimeLocal":"2026-03-02 06:00:00",
		 "activityType":{"typeKey":"trail_running"},"duration":1800,"distance":5000,"averageHR":135,"avgPower":0}
	]`)

	activities, err := c.Activities(t.Context(), 0)
	if err != nil {
		t.Fatalf("Activities: %v", err)
	}
	if len(activities) != 2 {
		t.Fatalf("activities = %d, want 2", len(activities))
	}

	ride := activities[0]
	if ride.ID != "123" || ride.Name != "Morning Ride" || ride.Sport != "cycling" {
		t.Errorf("ride = %+v", ride)
	}
	if ride.DurationSeconds != 3600 || ride.DistanceM != 30000 || ride.AvgHR != 142 || ride.AvgPowerWatts != 215 {
		t.Errorf("ride metrics = %+v", ride)
	}
	if ride.StartTime.Format("2006-01-02") != "2026-03-01" {
		t.Errorf("ride start = %v", ride.StartTime)
	}
	if ride.NormalizedPower != 230 || ride.TrainingStressScore != 85.5 || ride.IntensityFactor != 0.82 {
		t.Errorf("ride summary fields = %+v", ride)
	}
	wantBest := map[int]float64{5: 950, 60: 410, 300: 280, 1200: 240, 3600: 220}
	if len(ride.BestPower) != len(wantBest) {
		t.Fatalf("BestPower = %+v, want %+v", ride.BestPower, wantBest)
	}
	for seconds, want := range wantBest {
		if got := ride.BestPower[seconds]; got != want {
			t.Errorf("BestPower[%d] = %v, want %v", seconds, got, want)
		}
	}

	run := activities[1]
	if run.Sport != "running" {
		t.Errorf("run sport = %q, want running (typeKey trail_running)", run.Sport)
	}
	// No summary fields at all in the fixture: everything stays zero, and
	// no best-power window is reported as present.
	if run.NormalizedPower != 0 || run.TrainingStressScore != 0 || run.IntensityFactor != 0 {
		t.Errorf("run summary fields should default to zero: %+v", run)
	}
	if len(run.BestPower) != 0 {
		t.Errorf("run BestPower = %+v, want none (absent fields stay out of the map)", run.BestPower)
	}
}

func TestActivitiesDefaultsTheLimit(t *testing.T) {
	c := activityFake(t, http.StatusOK, `[]`)
	if _, err := c.Activities(t.Context(), 0); err != nil {
		t.Fatalf("Activities: %v", err)
	}
}

func TestActivitiesSurfacesRejection(t *testing.T) {
	c := activityFake(t, http.StatusForbidden, `{}`)
	if _, err := c.Activities(t.Context(), 10); err == nil {
		t.Fatal("expected an error for a 403")
	}
}

func TestMapSport(t *testing.T) {
	for key, want := range map[string]string{
		"road_biking":       "cycling",
		"indoor_cycling":    "cycling",
		"running":           "running",
		"trail_running":     "running",
		"treadmill_running": "running",
		"mountain_biking":   "cycling",
		"strength_training": "cycling", // no clean fit; defaults to cycling
	} {
		if got := mapSport(key); got != want {
			t.Errorf("mapSport(%q) = %q, want %q", key, got, want)
		}
	}
}
