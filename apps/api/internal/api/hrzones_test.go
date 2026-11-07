package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHRZonesPutThenGetRoundTrips(t *testing.T) {
	h := newSeasonHarness(t)
	put := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile",
		`{"maxHr":190,"hrZones":{"cycling":{"method":"custom","bounds":[120,140,155,170]}}}`)
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d %s", put.StatusCode, readBody(t, put))
	}
	var saved struct {
		HRZones struct{ Cycling, Running workout.HRZoneSetting } `json:"hrZones"`
	}
	if err := json.Unmarshal([]byte(readBody(t, put)), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.HRZones.Cycling.Method != "custom" || len(saved.HRZones.Cycling.Bounds) != 4 {
		t.Errorf("PUT response = %+v", saved.HRZones)
	}

	// A later save that does not mention the zones leaves them alone.
	if r := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"maxHr":191}`); r.StatusCode != http.StatusOK {
		t.Fatalf("second PUT = %d", r.StatusCode)
	}
	get := h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "")
	var got struct {
		HRZones struct{ Cycling, Running *workout.HRZoneSetting } `json:"hrZones"`
	}
	if err := json.Unmarshal([]byte(readBody(t, get)), &got); err != nil {
		t.Fatal(err)
	}
	if got.HRZones.Running == nil || got.HRZones.Cycling == nil {
		t.Fatalf("GET must return both sports: %+v", got.HRZones)
	}
	if got.HRZones.Cycling.Method != "custom" || got.HRZones.Cycling.Bounds[1] != 140 || got.HRZones.Running.Method != "" {
		t.Errorf("GET = %+v / %+v", got.HRZones.Cycling, got.HRZones.Running)
	}
}

func TestHRZonesPutRefusesBadCustomBounds(t *testing.T) {
	h := newSeasonHarness(t)
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile",
		`{"maxHr":190,"hrZones":{"running":{"method":"custom","bounds":[140,130,155,170]}}}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PUT = %d, want 422", resp.StatusCode)
	}
	var e map[string]string
	if err := json.Unmarshal([]byte(readBody(t, resp)), &e); err != nil || e["error"] == "" {
		t.Errorf("body = %v, %v", e, err)
	}
	// Nothing of the rejected save was written.
	p, _, err := h.store.GetProfile(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxHR != 0 {
		t.Errorf("MaxHR = %d, a refused save must write nothing", p.MaxHR)
	}
}

type zoneRow struct {
	Zone    int     `json:"zone"`
	Low     float64 `json:"low"`
	High    float64 `json:"high"`
	Seconds int     `json:"seconds"`
}

func fitnessZones(t *testing.T, h *seasonHarness, id string) []zoneRow {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/fitness", "")
	var out struct {
		Sessions []struct {
			ID       string `json:"id"`
			Analysis *struct {
				HRZones []zoneRow `json:"hrZones"`
			} `json:"analysis"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	for _, s := range out.Sessions {
		if s.ID == id && s.Analysis != nil {
			return s.Analysis.HRZones
		}
	}
	t.Fatalf("session %s with analysis not in response", id)
	return nil
}

func TestRideTimeInZoneFollowsTheRidersSettingWithoutReanalysis(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{Rider: "wilant", MaxHR: 190}); err != nil {
		t.Fatal(err)
	}
	sess, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "hz1", Sport: "cycling",
		Date: "2026-10-05", DurationSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 100 s each at 100, 130, 150, 170 and 185 bpm.
	hist := &workout.HRHistogram{Min: 100, Seconds: make([]int, 86)}
	for _, bpm := range []int{100, 130, 150, 170, 185} {
		hist.Seconds[bpm-100] = 100
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: sess.ID, Rider: "wilant", Outcome: "completed", HRHistogram: hist,
	}); err != nil {
		t.Fatal(err)
	}

	// Automatic with max HR 190: 114/133/152/171, so 130 is Z2, 150 Z3, 170 Z4.
	auto := fitnessZones(t, h, sess.ID)
	if len(auto) != 5 || auto[0].Seconds != 100 || auto[1].Seconds != 100 || auto[4].Seconds != 100 || auto[0].Low != 0 || auto[4].High != 0 {
		t.Fatalf("automatic zones = %+v", auto)
	}

	put := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile",
		`{"maxHr":190,"hrZones":{"cycling":{"method":"custom","bounds":[110,125,140,160]}}}`)
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d %s", put.StatusCode, readBody(t, put))
	}
	custom := fitnessZones(t, h, sess.ID)
	// 100 is below 110 (Z1); 130 is in [125,140) (Z3); 150 in [140,160) (Z4); 170 and 185 are Z5.
	want := []int{100, 0, 100, 100, 200}
	for i, w := range want {
		if custom[i].Seconds != w {
			t.Errorf("custom zone %d = %d s, want %d (%+v)", i+1, custom[i].Seconds, w, custom)
		}
	}
	if custom[1].Low != 110 || custom[1].High != 125 {
		t.Errorf("Z2 bounds = [%v, %v]", custom[1].Low, custom[1].High)
	}
}
