package garmin

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// biometricsFake serves each biometric path with its own status and body, so
// a test can break one endpoint and leave the other two healthy.
func biometricsFake(t *testing.T, responses map[string]struct {
	status int
	body   string
}) *Client {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	for path, resp := range responses {
		mux.HandleFunc(path+"/", func(w http.ResponseWriter, r *http.Request) { serveBiometric(w, resp.status, resp.body) })
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { serveBiometric(w, resp.status, resp.body) })
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1", DisplayName: "wilant-n"})
	return c
}

func serveBiometric(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	fmt.Fprint(w, body)
}

type biometricResponse = struct {
	status int
	body   string
}

func TestBiometricsDecodesAllThree(t *testing.T) {
	c := biometricsFake(t, map[string]biometricResponse{
		heartRateZonesPath: {http.StatusOK, `[
			{"sport":"RUNNING","maxHeartRateUsed":188},
			{"sport":"DEFAULT","maxHeartRateUsed":192}]`},
		// Two near-identical dicts, one carrying the speed — the shape the
		// reference client documents for this endpoint.
		lactateThresholdPath: {http.StatusOK, `[
			{"speed":0.3611,"heartRate":171},
			{"speed":0.3611,"heartRateCycling":160}]`},
		ftpRangePath: {http.StatusOK, `[
			{"calendarDate":"2026-01-10","value":240},
			{"calendarDate":"2026-03-02","value":255.0}]`},
	})

	got, err := c.Biometrics(t.Context(), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Biometrics: %v", err)
	}
	if got.MaxHR != 192 {
		t.Errorf("MaxHR = %d, want 192 (the DEFAULT zone set, not the running one)", got.MaxHR)
	}
	if math.Abs(got.ThresholdPaceSecPerKM-1000/3.611) > 0.01 {
		t.Errorf("pace = %v, want %v", got.ThresholdPaceSecPerKM, 1000/3.611)
	}
	if got.CyclingFTPWatts != 255 {
		t.Errorf("FTP = %v, want the most recent reading, 255", got.CyclingFTPWatts)
	}
	if got.ThresholdHR != 171 {
		t.Errorf("ThresholdHR = %d, want 171 from the object that carried the speed", got.ThresholdHR)
	}
}

func TestBiometricsThresholdHRAcceptsBothKeySpellings(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"heartRate", `{"speed":0.3611,"heartRate":171}`, 171},
		{"the reference's hearRate typo", `{"speed":0.3611,"hearRate":168}`, 168},
		{"only from the object that supplied the speed", `[{"speed":0.3611,"hearRate":171},{"heartRate":150}]`, 171},
		{"an object with no speed is not used", `[{"heartRate":150}]`, 0},
		{"below range fails closed", `{"speed":0.3611,"heartRate":60}`, 0},
		{"above range fails closed", `{"speed":0.3611,"heartRate":250}`, 0},
		{"absent", `{"speed":0.3611}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := biometricsFake(t, map[string]biometricResponse{
				lactateThresholdPath: {http.StatusOK, tc.body},
			})
			got, _ := c.Biometrics(t.Context(), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
			if got.ThresholdHR != tc.want {
				t.Errorf("ThresholdHR = %d, want %d", got.ThresholdHR, tc.want)
			}
		})
	}
}

func TestBiometricsThresholdHRMakesNoExtraRequest(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc(lactateThresholdPath, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		fmt.Fprint(w, `{"speed":0.3611,"heartRate":171}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1", DisplayName: "wilant-n"})

	got, _ := c.Biometrics(t.Context(), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	if got.ThresholdHR != 171 || got.ThresholdPaceSecPerKM == 0 {
		t.Errorf("got %+v, want both the pace and the threshold HR", got)
	}
	if calls != 1 {
		t.Errorf("latestLactateThreshold calls = %d, want 1 (pace and HR share one request)", calls)
	}
}

func TestBiometricsOneEndpointFailingKeepsTheOthers(t *testing.T) {
	c := biometricsFake(t, map[string]biometricResponse{
		heartRateZonesPath:   {http.StatusNotFound, `{"message":"gone"}`},
		lactateThresholdPath: {http.StatusOK, `{"speed":3.5}`},
		ftpRangePath:         {http.StatusOK, `[{"calendarDate":"2026-03-02","value":250}]`},
	})

	got, err := c.Biometrics(t.Context(), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Error("want the failed endpoint reported, got nil")
	}
	if got.MaxHR != 0 || got.CyclingFTPWatts != 250 || got.ThresholdPaceSecPerKM == 0 {
		t.Errorf("got %+v, want MaxHR 0 and the other two intact", got)
	}
}

func TestBiometricsRejectsImplausibleValues(t *testing.T) {
	c := biometricsFake(t, map[string]biometricResponse{
		heartRateZonesPath:   {http.StatusOK, `[{"sport":"DEFAULT","maxHeartRateUsed":30}]`},
		lactateThresholdPath: {http.StatusOK, `[{"speed":42}]`},
		ftpRangePath:         {http.StatusOK, `[{"calendarDate":"2026-03-02","value":5}]`},
	})

	got, err := c.Biometrics(t.Context(), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Biometrics: %v", err)
	}
	if got != (Biometrics{}) {
		t.Errorf("got %+v, want nothing: every value was outside what a person could have", got)
	}
}

func TestPaceFromLactateSpeedHandlesBothUnits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		speed float64
		want  float64
	}{
		{"decametres per second", 0.3333, 300},
		{"metres per second", 3.3333, 300},
		{"zero", 0, 0},
		{"too slow to be a runner", 0.05, 0},
		{"too fast to be a runner", 12, 0},
	} {
		if got := paceFromLactateSpeed(tc.speed); math.Abs(got-tc.want) > 0.1 {
			t.Errorf("%s: pace = %v, want %v", tc.name, got, tc.want)
		}
	}
}
