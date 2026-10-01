package garmin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// rhrFake answers every wellness endpoint from a per-path status/body table
// and counts requests per path, so a test can assert both what was read and
// which calls were (not) made.
type rhrFake struct {
	mu     sync.Mutex
	counts map[string]int
	client *Client
}

type rhrReply struct {
	status int
	body   string
}

func newRHRFake(t *testing.T, replies map[string]rhrReply) *rhrFake {
	t.Helper()
	f := &rhrFake{counts: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := ""
		for prefix := range replies {
			if strings.HasPrefix(r.URL.Path, prefix) && len(prefix) > len(key) {
				key = prefix
			}
		}
		f.mu.Lock()
		f.counts[key]++
		f.mu.Unlock()
		rep, ok := replies[key]
		if !ok {
			rep = rhrReply{http.StatusNotFound, ``}
		}
		w.WriteHeader(rep.status)
		fmt.Fprint(w, rep.body)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1", ProfileID: "wilant-n"})
	f.client = c
	return f
}

func (f *rhrFake) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[prefix]
}

var (
	rhrSummary = dailySummaryPath + "/wilant-n"
	rhrStats   = rhrStatsPath + "/wilant-n"
	rhrSleep   = sleepPath + "/wilant-n"
	testDay    = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
)

func TestRestingHeartRateSummaryValueMakesNoFallbackCall(t *testing.T) {
	f := newRHRFake(t, map[string]rhrReply{rhrSummary: {200, `{"restingHeartRate":48}`}})
	bpm, err := f.client.RestingHeartRate(t.Context(), testDay)
	if err != nil || bpm != 48 {
		t.Fatalf("bpm, err = %d, %v; want 48, nil", bpm, err)
	}
	if f.count(rhrStats) != 0 || f.count(rhrSleep) != 0 {
		t.Errorf("fallbacks called: stats=%d sleep=%d, want none", f.count(rhrStats), f.count(rhrSleep))
	}
}

func TestRestingHeartRateNullSummaryUsesTheRHREndpoint(t *testing.T) {
	f := newRHRFake(t, map[string]rhrReply{
		rhrSummary: {200, `{"restingHeartRate":null,"totalSteps":100}`},
		rhrStats:   {200, `{"allMetrics":{"metricsMap":{"WELLNESS_RESTING_HEART_RATE":[{"value":51.0,"calendarDate":"2026-03-01"}]}}}`},
	})
	bpm, err := f.client.RestingHeartRate(t.Context(), testDay)
	if err != nil || bpm != 51 {
		t.Fatalf("bpm, err = %d, %v; want 51, nil", bpm, err)
	}
	if f.count(rhrSleep) != 0 {
		t.Errorf("sleep called %d times, want 0 once the RHR endpoint answered", f.count(rhrSleep))
	}
}

func TestRestingHeartRateFallsBackToTheSleepTopLevelValue(t *testing.T) {
	f := newRHRFake(t, map[string]rhrReply{
		rhrSummary: {200, `{"restingHeartRate":0}`},
		rhrStats:   {200, `{"allMetrics":{"metricsMap":{}}}`},
		rhrSleep:   {200, `{"dailySleepDTO":{},"restingHeartRate":46}`},
	})
	bpm, err := f.client.RestingHeartRate(t.Context(), testDay)
	if err != nil || bpm != 46 {
		t.Fatalf("bpm, err = %d, %v; want 46, nil", bpm, err)
	}
}

func TestRestingHeartRateAllEmptyIsZeroWithDiagnostics(t *testing.T) {
	f := newRHRFake(t, map[string]rhrReply{
		rhrSummary: {200, `{"totalSteps":900,"restingHeartRate":null}`},
		rhrStats:   {204, ``},
		rhrSleep:   {200, `{"dailySleepDTO":{}}`},
	})
	ctx, diag := WithRHRDiagnostics(t.Context())
	bpm, err := f.client.RestingHeartRate(ctx, testDay)
	if err != nil || bpm != 0 {
		t.Fatalf("bpm, err = %d, %v; want 0, nil", bpm, err)
	}
	got := strings.Join(diag.Sources(), ",")
	for _, want := range []string{"daily_summary", "rhr_endpoint", "sleep"} {
		if !strings.Contains(got, want) {
			t.Errorf("sources = %q, missing %q", got, want)
		}
	}
	keys := strings.Join(diag.SummaryKeys(), ",")
	if !strings.Contains(keys, "totalSteps") || !strings.Contains(keys, "restingHeartRate") {
		t.Errorf("summary keys = %q, want both top-level key names", keys)
	}
	if strings.Contains(keys+got, "900") {
		t.Errorf("diagnostics leaked a value: %q %q", keys, got)
	}
}

func TestRestingHeartRateRejectsImplausibleValues(t *testing.T) {
	for _, body := range []string{`{"restingHeartRate":24}`, `{"restingHeartRate":121}`, `{"restingHeartRate":300}`} {
		f := newRHRFake(t, map[string]rhrReply{rhrSummary: {200, body}})
		bpm, err := f.client.RestingHeartRate(t.Context(), testDay)
		if err != nil || bpm != 0 {
			t.Errorf("%s: bpm, err = %d, %v; want 0, nil", body, bpm, err)
		}
	}
}

func TestWellnessMakesNoExtraSleepRequestForTheRHRFallback(t *testing.T) {
	f := newRHRFake(t, map[string]rhrReply{
		hrvPath:               {204, ``},
		rhrSummary:            {200, `{}`},
		rhrStats:              {200, `{}`},
		rhrSleep:              {200, `{"dailySleepDTO":{"sleepTimeSeconds":100},"restingHeartRate":47}`},
		trainingReadinessPath: {200, `[]`},
	})
	w, err := f.client.Wellness(t.Context(), testDay)
	if err != nil {
		t.Fatal(err)
	}
	if w.RestingHR != 47 {
		t.Errorf("RestingHR = %d, want 47 from the sleep response", w.RestingHR)
	}
	if n := f.count(rhrSleep); n != 1 {
		t.Errorf("sleep requests = %d, want exactly 1", n)
	}
	if len(w.Partial) != 0 {
		t.Errorf("Partial = %v, want none", w.Partial)
	}
}

func TestWellnessHRVNoReadingIsNotPartial(t *testing.T) {
	cases := map[string]rhrReply{
		"204":          {204, ``},
		"404":          {404, `{"message":"not found"}`},
		"empty body":   {200, ``},
		"no summary":   {200, `{"hrvReadings":[]}`},
		"null summary": {200, `{"hrvSummary":null}`},
	}
	for name, rep := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRHRFake(t, map[string]rhrReply{hrvPath: rep, rhrSummary: {200, `{"restingHeartRate":48}`}, rhrSleep: {200, `{}`}, trainingReadinessPath: {200, `[]`}})
			w, err := f.client.Wellness(t.Context(), testDay)
			if err != nil {
				t.Fatal(err)
			}
			if len(w.Partial) != 0 {
				t.Errorf("Partial = %v, want none", w.Partial)
			}
		})
	}
}

func TestWellnessRealFailuresRecordTheStatus(t *testing.T) {
	f := newRHRFake(t, map[string]rhrReply{
		hrvPath:               {500, `{}`},
		rhrSummary:            {200, `{"restingHeartRate":48}`},
		rhrSleep:              {403, `{}`},
		trainingReadinessPath: {200, `not json`},
	})
	w, err := f.client.Wellness(t.Context(), testDay)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(w.Partial, ",")
	if got != "hrv:500,sleep:403,readiness:200" {
		t.Errorf("Partial = %q, want hrv:500,sleep:403,readiness:200", got)
	}
}

func TestWellnessSleepAndReadinessNoReadingAreNotPartial(t *testing.T) {
	f := newRHRFake(t, map[string]rhrReply{
		hrvPath:               {204, ``},
		rhrSummary:            {200, `{"restingHeartRate":48}`},
		rhrSleep:              {404, ``},
		trainingReadinessPath: {204, ``},
	})
	w, err := f.client.Wellness(t.Context(), testDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Partial) != 0 {
		t.Errorf("Partial = %v, want none", w.Partial)
	}
}
