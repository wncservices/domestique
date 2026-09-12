package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/wahoo"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// metricsSyncHarness is its own small harness (rather than reusing
// newWahooHarness or newConnectHarness) — this is the one test in the
// suite that needs Garmin *and* Wahoo *and* Training connected together,
// and building that combination once here is simpler than stretching an
// existing single-provider harness to cover a case it was not shaped for.
type metricsSyncHarness struct {
	t          *testing.T
	client     *http.Client
	base       string
	links      *providerlink.Store
	wahooFake  *httptest.Server
	wahooCalls []string
}

func newMetricsSyncHarness(t *testing.T, garminConnector *fakeGarmin) *metricsSyncHarness {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	trainingStore, err := workout.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := auth.New(auth.Config{
		Mode:  auth.ModeProxy,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	h := &metricsSyncHarness{t: t, links: links}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/workouts", func(w http.ResponseWriter, r *http.Request) {
		h.wahooCalls = append(h.wahooCalls, r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"workouts": [
			{"id": 9001, "name": "Evening Spin", "starts": "2026-03-05T18:00:00Z", "minutes": 45,
			 "workout_summary": {"duration_total_accum": "2700", "distance_accum": "20000",
			                      "heart_rate_avg": "138", "power_bike_avg": "190"}}
		]}`)
	})
	h.wahooFake = httptest.NewServer(mux)
	t.Cleanup(h.wahooFake.Close)

	wahooClient := wahoo.New(wahoo.Config{ClientID: "id", ClientSecret: "secret", RedirectURL: "https://app.test/cb"})
	wahooClient.APIBase = h.wahooFake.URL

	srv := &api.Server{
		Source:   db,
		Auth:     authenticator,
		Links:    links,
		Training: trainingStore,
		Garmin:   garminConnector,
		Wahoo:    wahooClient,
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	h.client, h.base = server.Client(), server.URL
	return h
}

func (h *metricsSyncHarness) as(user, groups, method, path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Remote-User", user)
	req.Header.Set("Remote-Groups", groups)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (h *metricsSyncHarness) seedWahooSession(rider string) {
	h.t.Helper()
	sealed, err := json.Marshal(wahoo.Session{AccessToken: "at-1", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.links.Save("wahoo", rider, providerlink.Connection{Secret: string(sealed)}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *metricsSyncHarness) seedGarminSession(rider string) {
	h.t.Helper()
	sealed, err := json.Marshal(garmin.Session{OAuth1Token: "tok", OAuth1Secret: "sec"})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.links.Save("garmin", rider, providerlink.Connection{Secret: string(sealed)}); err != nil {
		h.t.Fatal(err)
	}
}

func TestSyncTrainingMetricsFromBothProviders(t *testing.T) {
	fake := &fakeGarmin{activities: []garmin.Activity{
		{ID: "5001", Name: "Threshold Ride", Sport: "cycling", StartTime: time.Date(2026, 3, 4, 7, 0, 0, 0, time.UTC),
			DurationSeconds: 5400, DistanceM: 45000, AvgHR: 150, AvgPowerWatts: 230},
	}}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.seedWahooSession("wilant")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Synced   int      `json:"synced"`
		Warnings []string `json:"warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Synced != 2 {
		t.Fatalf("synced = %d, want 2 (one from each provider), warnings = %v", out.Synced, out.Warnings)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", out.Warnings)
	}
	if len(h.wahooCalls) != 1 || h.wahooCalls[0] != "Bearer at-1" {
		t.Errorf("wahoo calls = %v", h.wahooCalls)
	}

	// The fitness endpoint now reflects both sessions.
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/fitness", "")
	var fit struct {
		Snapshots []struct {
			Date string  `json:"date"`
			CTL  float64 `json:"ctl"`
		} `json:"snapshots"`
		Sessions []struct {
			Provider     string  `json:"provider"`
			TrainingLoad float64 `json:"trainingLoad"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fit); err != nil {
		t.Fatal(err)
	}
	if len(fit.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(fit.Sessions))
	}
	if len(fit.Snapshots) == 0 {
		t.Fatal("expected fitness snapshots after a sync with sessions")
	}
	for _, s := range fit.Sessions {
		if s.TrainingLoad <= 0 {
			t.Errorf("session %+v has no training load", s)
		}
	}

	// A different rider's fitness is untouched.
	resp = h.as("other", "cyclists", http.MethodGet, "/api/training/fitness", "")
	var otherFit struct {
		Sessions []any `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&otherFit); err != nil {
		t.Fatal(err)
	}
	if len(otherFit.Sessions) != 0 {
		t.Errorf("other rider's sessions = %v, want none", otherFit.Sessions)
	}
}

func TestSyncTrainingMetricsOneProviderFailingDoesNotBlockTheOther(t *testing.T) {
	fake := &fakeGarmin{activitiesErr: fmt.Errorf("garmin: the session was refused")}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.seedWahooSession("wilant")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	var out struct {
		Synced   int      `json:"synced"`
		Warnings []string `json:"warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Synced != 1 {
		t.Errorf("synced = %d, want 1 (wahoo alone)", out.Synced)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "garmin") {
		t.Errorf("warnings = %v, want exactly one naming garmin", out.Warnings)
	}
}

func TestSyncTrainingMetricsWithNoConnectionsSyncsNothing(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with nothing connected", resp.StatusCode)
	}
	var out struct {
		Synced int `json:"synced"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Synced != 0 {
		t.Errorf("synced = %d, want 0", out.Synced)
	}
}
