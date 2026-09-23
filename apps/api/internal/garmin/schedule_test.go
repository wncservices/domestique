package garmin

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func scheduleFake(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc("/workout-service/", handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1"})
	return c
}

func TestScheduleWorkoutPostsTheDateAndReadsTheEntryID(t *testing.T) {
	var gotPath, gotBody string
	c := scheduleFake(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.Method+" "+r.URL.Path, string(b)
		fmt.Fprint(w, `{"workoutScheduleId": 777001, "date": "2026-03-20"}`)
	})

	id, err := c.ScheduleWorkout(t.Context(), "42", "2026-03-20")
	if err != nil {
		t.Fatalf("ScheduleWorkout: %v", err)
	}
	if id != "777001" {
		t.Errorf("schedule id = %q, want 777001", id)
	}
	if gotPath != "POST /workout-service/schedule/42" || !strings.Contains(gotBody, `"date":"2026-03-20"`) {
		t.Errorf("request = %s %s", gotPath, gotBody)
	}
}

func TestScheduleWorkoutWithNoIDInTheResponseStillSucceeds(t *testing.T) {
	c := scheduleFake(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) })
	id, err := c.ScheduleWorkout(t.Context(), "42", "2026-03-20")
	if err != nil || id != "" {
		t.Errorf("id = %q err = %v, want an empty id and no error: it was scheduled, we just cannot move it later", id, err)
	}
}

func TestScheduleWorkoutReportsAWorkoutTheRiderDeleted(t *testing.T) {
	c := scheduleFake(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	if _, err := c.ScheduleWorkout(t.Context(), "42", "2026-03-20"); !errors.Is(err, ErrWorkoutGone) {
		t.Errorf("err = %v, want ErrWorkoutGone", err)
	}
}

func TestUnscheduleWorkoutTreatsAnAlreadyGoneEntryAsDone(t *testing.T) {
	c := scheduleFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/workout-service/schedule/777001" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	if err := c.UnscheduleWorkout(t.Context(), "777001"); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestUpdateAndDeleteWorkoutReportAWorkoutTheRiderDeleted(t *testing.T) {
	c := scheduleFake(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	if err := c.DeleteWorkout(t.Context(), "42"); !errors.Is(err, ErrWorkoutGone) {
		t.Errorf("delete: err = %v, want ErrWorkoutGone", err)
	}
	if err := c.UpdateWorkout(t.Context(), "42", "x", "cycling", exampleSteps()); !errors.Is(err, ErrWorkoutGone) {
		t.Errorf("update: err = %v, want ErrWorkoutGone", err)
	}
}
