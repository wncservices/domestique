package wahoo

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/fitcourse"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

// syntheticFIT builds a tiny, real, valid FIT course file — synthetic
// coordinates, never real ride data (AGENTS.md: FIT fixtures must stay
// synthetic).
func syntheticFIT(t *testing.T) []byte {
	t.Helper()
	points := []gpx.Point{
		{Lat: 51.05, Lon: 3.72},
		{Lat: 51.06, Lon: 3.73},
		{Lat: 51.07, Lon: 3.74},
	}
	data, err := fitcourse.Encode(points, fitcourse.Options{Name: "Test"})
	if err != nil {
		t.Fatalf("building a synthetic FIT fixture: %v", err)
	}
	return data
}

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
						"heart_rate_avg": "142.0", "power_bike_avg": "215.0",
					"power_bike_np_last": "230.0", "power_bike_tss_last": "85.5",
					"file": {"url": "https://cdn.wahooligan.com/wahoo-cloud/production/uploads/f.fit"}
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
	if ride.NormalizedPower != 230 || ride.TSS != 85.5 {
		t.Errorf("ride NP/TSS = %+v, want 230/85.5", ride)
	}
	if ride.FileURL != "https://cdn.wahooligan.com/wahoo-cloud/production/uploads/f.fit" {
		t.Errorf("ride FileURL = %q", ride.FileURL)
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
	if noSummary.NormalizedPower != 0 || noSummary.TSS != 0 || noSummary.FileURL != "" {
		t.Errorf("no-summary NP/TSS/FileURL should be zero: %+v", noSummary)
	}
}

// power_bike_np_last/power_bike_tss_last were observed to vary between a
// JSON string and a bare number, unlike the rest of workout_summary's
// fields — flexibleNumber has to accept both.
func TestListWorkoutsAcceptsNPAndTSSAsStringOrNumber(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workouts": [
			{"id": 1, "name": "String form", "starts": "2026-01-01T00:00:00Z", "minutes": 30,
			 "workout_summary": {"power_bike_np_last": "200.5", "power_bike_tss_last": "60.0"}},
			{"id": 2, "name": "Number form", "starts": "2026-01-02T00:00:00Z", "minutes": 30,
			 "workout_summary": {"power_bike_np_last": 210.5, "power_bike_tss_last": 65}}
		]}`))
	})
	workouts, err := c.ListWorkouts(t.Context(), "at", 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(workouts) != 2 {
		t.Fatalf("got %d workouts, want 2", len(workouts))
	}
	if workouts[0].NormalizedPower != 200.5 || workouts[0].TSS != 60 {
		t.Errorf("string-form NP/TSS = %+v", workouts[0])
	}
	if workouts[1].NormalizedPower != 210.5 || workouts[1].TSS != 65 {
		t.Errorf("number-form NP/TSS = %+v", workouts[1])
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

func TestWorkoutFITDownloadsAndValidates(t *testing.T) {
	fit := syntheticFIT(t)
	var gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write(fit)
	})

	got, err := c.WorkoutFIT(t.Context(), c.APIBase+"/workout/5/file.fit")
	if err != nil {
		t.Fatalf("WorkoutFIT: %v", err)
	}
	if string(got) != string(fit) {
		t.Fatalf("got %d bytes, want the fixture's %d bytes to match exactly", len(got), len(fit))
	}
	// WorkoutFIT takes no access token — fileURL is a pre-signed link, and
	// there is nothing to attach even when it happens to land on the API
	// host. See WorkoutFIT's own doc comment.
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none: WorkoutFIT has no token to attach", gotAuth)
	}
}

func TestWorkoutFITRejectsA403(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	})
	if _, err := c.WorkoutFIT(t.Context(), c.APIBase+"/workout/5/file.fit"); err == nil {
		t.Fatal("expected an error for a 403")
	}
}

func TestWorkoutFITRefusesAnEmptyURLBeforeAnyRequest(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request should have been sent for an empty file URL")
	})
	if _, err := c.WorkoutFIT(t.Context(), ""); err == nil {
		t.Fatal("expected an error for an empty file URL")
	}
}

// Same SSRF-prevention shape as DownloadRoute's own
// TestDownloadRouteRefusesAnUnrecognizedHost: workout_summary.file.url comes
// straight out of Wahoo's response body, so a compromised or malicious
// upstream must not be able to point this pod at an arbitrary host.
func TestWorkoutFITRefusesAnUnrecognizedHost(t *testing.T) {
	var reached bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = w.Write([]byte("fit-bytes"))
	}))
	t.Cleanup(elsewhere.Close)

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should never reach the API host either — the URL is refused before any request is sent")
	})

	if _, err := c.WorkoutFIT(t.Context(), elsewhere.URL+"/workout.fit"); err == nil {
		t.Fatal("expected an error for a file URL on an unrecognized host")
	}
	if reached {
		t.Error("the unrecognized host was fetched despite not being on the allowlist")
	}
}

func TestWorkoutFITRejectsANonFITBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>not a fit file</body></html>"))
	})
	if _, err := c.WorkoutFIT(t.Context(), c.APIBase+"/workout/5/file.fit"); err == nil {
		t.Fatal("expected an error for a non-FIT body")
	}
}

func TestWorkoutFITRejectsAnOversizedBodyWithoutBuffering(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte{'A'}, 1<<20)
		for written := 0; written <= MaxFITBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	if _, err := c.WorkoutFIT(t.Context(), c.APIBase+"/workout/5/file.fit"); err == nil {
		t.Fatal("expected an error for a body over MaxFITBytes")
	}
}
