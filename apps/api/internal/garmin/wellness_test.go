package garmin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func wellnessFake(t *testing.T, status int, response string) *Client {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc(dailySummaryPath+"/wilant-n", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("calendarDate"); got == "" {
			t.Error("expected a calendarDate query param")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, response)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1", ProfileID: "wilant-n"})
	return c
}

func TestRestingHeartRateDecodesTheDailySummary(t *testing.T) {
	c := wellnessFake(t, http.StatusOK, `{"restingHeartRate":48}`)

	bpm, err := c.RestingHeartRate(t.Context(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RestingHeartRate: %v", err)
	}
	if bpm != 48 {
		t.Errorf("bpm = %d, want 48", bpm)
	}
}

func TestRestingHeartRateZeroMeansNoReading(t *testing.T) {
	c := wellnessFake(t, http.StatusOK, `{}`)

	bpm, err := c.RestingHeartRate(t.Context(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RestingHeartRate: %v", err)
	}
	if bpm != 0 {
		t.Errorf("bpm = %d, want 0 for a day with no wellness data", bpm)
	}
}

func TestRestingHeartRateSurfacesRejection(t *testing.T) {
	c := wellnessFake(t, http.StatusForbidden, `{}`)

	if _, err := c.RestingHeartRate(t.Context(), time.Now()); err == nil {
		t.Fatal("expected an error for a 403")
	}
}

func TestRestingHeartRateFailsWhenNoProfileHandleCanBeFound(t *testing.T) {
	c := New()
	c.APIBase = "https://example.invalid"
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1"}) // no ProfileID, and no Connect to ask

	if _, err := c.RestingHeartRate(t.Context(), time.Now()); err == nil {
		t.Fatal("expected an error with no profile handle to ask for")
	}
}

// wellnessResponses configures how each of the four Wellness endpoints
// answers, so tests can make one fail while the others still succeed.
type wellnessResponses struct {
	hrvStatus, sleepStatus, readinessStatus, restingStatus int
	hrvBody, sleepBody, readinessBody, restingBody         string
}

func wellnessFullFake(t *testing.T, r wellnessResponses) *Client {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc(dailySummaryPath+"/wilant-n", func(w http.ResponseWriter, r2 *http.Request) {
		if got := r2.URL.Query().Get("calendarDate"); got == "" {
			t.Error("expected a calendarDate query param")
		}
		w.WriteHeader(r.restingStatus)
		fmt.Fprint(w, r.restingBody)
	})
	mux.HandleFunc(hrvPath+"/2026-03-01", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(r.hrvStatus)
		fmt.Fprint(w, r.hrvBody)
	})
	mux.HandleFunc(sleepPath+"/wilant-n", func(w http.ResponseWriter, r2 *http.Request) {
		if got := r2.URL.Query().Get("date"); got != "2026-03-01" {
			t.Errorf("date query = %q, want 2026-03-01", got)
		}
		if got := r2.URL.Query().Get("nonSleepBufferMinutes"); got != "60" {
			t.Errorf("nonSleepBufferMinutes query = %q, want 60", got)
		}
		w.WriteHeader(r.sleepStatus)
		fmt.Fprint(w, r.sleepBody)
	})
	mux.HandleFunc(trainingReadinessPath+"/2026-03-01", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(r.readinessStatus)
		fmt.Fprint(w, r.readinessBody)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1", ProfileID: "wilant-n"})
	return c
}

func TestWellnessDecodesAllFourSignals(t *testing.T) {
	c := wellnessFullFake(t, wellnessResponses{
		hrvStatus:       http.StatusOK,
		hrvBody:         `{"hrvSummary":{"lastNightAvg":48,"weeklyAvg":50,"status":"balanced"}}`,
		sleepStatus:     http.StatusOK,
		sleepBody:       `{"dailySleepDTO":{"sleepTimeSeconds":25920,"sleepScores":{"overall":{"value":82}}}}`,
		readinessStatus: http.StatusOK,
		readinessBody:   `[{"score":71,"level":"moderate"}]`,
		restingStatus:   http.StatusOK,
		restingBody:     `{"restingHeartRate":48}`,
	})

	w, err := c.Wellness(t.Context(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Wellness: %v", err)
	}
	if len(w.Partial) != 0 {
		t.Errorf("Partial = %v, want none", w.Partial)
	}
	if w.Date != "2026-03-01" {
		t.Errorf("Date = %q, want 2026-03-01", w.Date)
	}
	if w.HRVLastNight != 48 || w.HRVWeeklyAvg != 50 {
		t.Errorf("HRV = %v/%v, want 48/50", w.HRVLastNight, w.HRVWeeklyAvg)
	}
	// Status strings are upper-cased as received, whatever case Garmin sent.
	if w.HRVStatus != "BALANCED" {
		t.Errorf("HRVStatus = %q, want BALANCED", w.HRVStatus)
	}
	if w.SleepSeconds != 25920 || w.SleepScore != 82 {
		t.Errorf("sleep = %d/%d, want 25920/82", w.SleepSeconds, w.SleepScore)
	}
	if w.ReadinessScore != 71 || w.ReadinessLevel != "MODERATE" {
		t.Errorf("readiness = %d/%q, want 71/MODERATE", w.ReadinessScore, w.ReadinessLevel)
	}
	if w.RestingHR != 48 {
		t.Errorf("RestingHR = %d, want 48", w.RestingHR)
	}
}

func TestWellnessOneEndpointFailingLeavesTheOthersFilled(t *testing.T) {
	c := wellnessFullFake(t, wellnessResponses{
		hrvStatus:       http.StatusInternalServerError,
		hrvBody:         `{}`,
		sleepStatus:     http.StatusOK,
		sleepBody:       `{"dailySleepDTO":{"sleepTimeSeconds":25920,"sleepScores":{"overall":{"value":82}}}}`,
		readinessStatus: http.StatusOK,
		readinessBody:   `[{"score":71,"level":"MODERATE"}]`,
		restingStatus:   http.StatusOK,
		restingBody:     `{"restingHeartRate":48}`,
	})

	w, err := c.Wellness(t.Context(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Wellness: %v", err)
	}
	if len(w.Partial) != 1 || w.Partial[0] != "hrv:500" {
		t.Errorf("Partial = %v, want [hrv:500]", w.Partial)
	}
	if w.HRVLastNight != 0 || w.HRVWeeklyAvg != 0 || w.HRVStatus != "" {
		t.Errorf("HRV fields = %v/%v/%q, want zero values after the failure", w.HRVLastNight, w.HRVWeeklyAvg, w.HRVStatus)
	}
	if w.SleepSeconds != 25920 || w.SleepScore != 82 {
		t.Errorf("sleep should still be filled: %d/%d", w.SleepSeconds, w.SleepScore)
	}
	if w.ReadinessScore != 71 {
		t.Errorf("readiness should still be filled: %d", w.ReadinessScore)
	}
	if w.RestingHR != 48 {
		t.Errorf("resting HR should still be filled: %d", w.RestingHR)
	}
}

func TestWellnessEmptyReadinessArrayMeansNoReading(t *testing.T) {
	c := wellnessFullFake(t, wellnessResponses{
		hrvStatus:       http.StatusOK,
		hrvBody:         `{}`,
		sleepStatus:     http.StatusOK,
		sleepBody:       `{}`,
		readinessStatus: http.StatusOK,
		readinessBody:   `[]`,
		restingStatus:   http.StatusOK,
		restingBody:     `{}`,
	})

	w, err := c.Wellness(t.Context(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Wellness: %v", err)
	}
	if len(w.Partial) != 0 {
		t.Errorf("Partial = %v, want none — an empty readiness array is a normal 'no reading', not a failure", w.Partial)
	}
	if w.ReadinessScore != 0 || w.ReadinessLevel != "" {
		t.Errorf("readiness = %d/%q, want zero values", w.ReadinessScore, w.ReadinessLevel)
	}
}

// The bug this guards: a session stored with the rider's full name as its
// DisplayName put "Wilant Nackaerts" in the sleep and daily-summary paths.
// Connect answered 404, read as "no reading", and sleep and resting HR were
// empty every night. With no ProfileID stored, the handle comes from the
// profile, once per client, and HRV and readiness never depend on it.
func TestWellnessLooksUpTheProfileHandleNotTheFullName(t *testing.T) {
	profileCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc("/userprofile-service/socialProfile", func(w http.ResponseWriter, _ *http.Request) {
		profileCalls++
		fmt.Fprint(w, `{"displayName":"a1b2-c3","fullName":"Wilant Nackaerts"}`)
	})
	mux.HandleFunc(sleepPath+"/a1b2-c3", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"dailySleepDTO":{"sleepTimeSeconds":25920,"sleepScores":{"overall":{"value":82}}}}`)
	})
	mux.HandleFunc(dailySummaryPath+"/a1b2-c3", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"restingHeartRate":48}`)
	})
	// Anything else, including the full name in a path, is Connect's 404.
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1", DisplayName: "Wilant Nackaerts"})

	for _, day := range []int{1, 2} {
		w, err := c.Wellness(t.Context(), time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if w.SleepScore != 82 || w.RestingHR != 48 {
			t.Errorf("day %d: sleep score %d, resting HR %d, want 82 and 48", day, w.SleepScore, w.RestingHR)
		}
	}
	if profileCalls != 1 {
		t.Errorf("profile asked %d times, want once per client", profileCalls)
	}
	if got := c.Session().ProfileID; got != "a1b2-c3" {
		t.Errorf("ProfileID = %q, want the handle kept on the session", got)
	}
}

// A profile lookup that fails costs sleep and resting HR only.
func TestWellnessWithoutAProfileStillReadsHRVAndReadiness(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc(hrvPath+"/2026-03-01", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"hrvSummary":{"lastNightAvg":48,"weeklyAvg":50,"status":"balanced"}}`)
	})
	mux.HandleFunc(trainingReadinessPath+"/2026-03-01", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"score":71,"level":"moderate"}]`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1"})

	w, err := c.Wellness(t.Context(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if w.HRVStatus != "BALANCED" || w.ReadinessScore != 71 {
		t.Errorf("hrv %q readiness %d, want both read without a profile", w.HRVStatus, w.ReadinessScore)
	}
	if got := fmt.Sprint(w.Partial); got != "[sleep:profile resting_hr:profile]" {
		t.Errorf("Partial = %s", got)
	}
}
