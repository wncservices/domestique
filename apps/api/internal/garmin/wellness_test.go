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
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1", DisplayName: "wilant-n"})
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

func TestRestingHeartRateRequiresADisplayName(t *testing.T) {
	c := New()
	c.APIBase = "https://example.invalid"
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1"}) // no DisplayName

	if _, err := c.RestingHeartRate(t.Context(), time.Now()); err == nil {
		t.Fatal("expected an error with no display name on the session")
	}
}
