package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// ride records a completed session of sport with 100 s at each given bpm.
func (h *seasonHarness) rideAt(t *testing.T, ext, sport string, bpms ...int) string {
	t.Helper()
	ctx := context.Background()
	sess, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: ext, Sport: sport,
		Date: "2026-10-05", DurationSeconds: float64(100 * len(bpms)),
	})
	if err != nil {
		t.Fatal(err)
	}
	hist := &workout.HRHistogram{Min: 90, Seconds: make([]int, 120)}
	for _, b := range bpms {
		hist.Seconds[b-90] = 100
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{
		SessionID: sess.ID, Rider: "wilant", Outcome: "completed", HRHistogram: hist,
	}); err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func (h *seasonHarness) saveProfileBody(t *testing.T, body string) {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT profile %s = %d %s", body, resp.StatusCode, readBody(t, resp))
	}
}

func secondsOf(z []zoneRow) []int {
	out := make([]int, len(z))
	for i, r := range z {
		out[i] = r.Seconds
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestARiderWhoPicksTheirOwnHeartRateZonesSeesRidesAndTargetsFollow(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", ThresholdHR: 160, MaxHR: 190, AvailableDays: []string{"tue", "thu", "sat", "sun"}, HoursPerAvailableDay: 2,
	}); err != nil {
		t.Fatal(err)
	}
	// Cycling Friel (LTHR 160): 129.6 / 144 / 150.4 / 160. 100, 135, 146, 155, 175 bpm
	// land one second-block in each zone. Running Friel: 136 / 144 / 152 / 160.
	cyc := h.rideAt(t, "j-cyc", "cycling", 100, 135, 146, 155, 175)
	run := h.rideAt(t, "j-run", "running", 100, 135, 146, 155, 175)
	friel := []int{100, 100, 100, 100, 100}

	// 1. Nothing saved: both sports automatic, zones from the Friel table.
	get := h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "")
	var prof struct {
		HRZones struct{ Cycling, Running *workout.HRZoneSetting } `json:"hrZones"`
	}
	if err := json.Unmarshal([]byte(readBody(t, get)), &prof); err != nil {
		t.Fatal(err)
	}
	if prof.HRZones.Cycling == nil || prof.HRZones.Cycling.Method != "" || prof.HRZones.Running == nil || prof.HRZones.Running.Method != "" {
		t.Fatalf("fresh rider's zones = %+v, want automatic for both", prof.HRZones)
	}
	if got := secondsOf(fitnessZones(t, h, cyc)); !sameInts(got, friel) {
		t.Fatalf("automatic cycling zones = %v, want %v", got, friel)
	}
	runAuto := secondsOf(fitnessZones(t, h, run))

	// 2. Custom for cycling: the cycling ride is redrawn without re-analysis, running is not.
	h.saveProfileBody(t, `{"maxHr":190,"thresholdHr":160,"availableDays":["tue","thu","sat","sun"],"hoursPerAvailableDay":2,"hrZones":{"cycling":{"method":"custom","bounds":[110,125,140,160]}}}`)
	if got, want := secondsOf(fitnessZones(t, h, cyc)), []int{100, 0, 100, 200, 100}; !sameInts(got, want) {
		t.Errorf("custom cycling zones = %v, want %v", got, want)
	}
	if got := secondsOf(fitnessZones(t, h, run)); !sameInts(got, runAuto) {
		t.Errorf("running zones moved to %v when only cycling changed (was %v)", got, runAuto)
	}

	// 5. A generated endurance ride for a rider with no FTP targets the custom bounds.
	g := h.createGoal(t, `{"name":"Race","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()
	found := false
	var walk func(steps []workout.WorkoutStep)
	walk = func(steps []workout.WorkoutStep) {
		for _, s := range steps {
			if s.Target == workout.TargetHeartRate && s.TargetLow == 110 && s.TargetHigh == 125 {
				found = true
			}
			walk(s.Steps)
		}
	}
	for _, w := range h.goalSessions(t, g.ID) {
		if w.Zone == workout.ZoneEndurance {
			walk(w.Steps)
		}
	}
	if !found {
		t.Error("no generated endurance step targets the custom 110-125 bpm band")
	}

	// 3. Reserve for running with no resting HR: no zones; with one, zones.
	h.saveProfileBody(t, `{"maxHr":190,"thresholdHr":160,"availableDays":["tue","thu","sat","sun"],"hoursPerAvailableDay":2,"hrZones":{"cycling":{"method":"custom","bounds":[110,125,140,160]},"running":{"method":"reserve"}}}`)
	if z := fitnessZones(t, h, run); len(z) != 0 {
		t.Errorf("reserve without a resting heart rate gave zones %+v, want none", z)
	}
	// Reserve, rest 50, max 190: 134 / 148 / 162 / 176.
	h.saveProfileBody(t, `{"maxHr":190,"thresholdHr":160,"availableDays":["tue","thu","sat","sun"],"hoursPerAvailableDay":2,"restingHr":50,"hrZones":{"running":{"method":"reserve"}}}`)
	if got, want := secondsOf(fitnessZones(t, h, run)), []int{100, 200, 100, 100, 0}; !sameInts(got, want) {
		t.Errorf("reserve running zones = %v, want %v", got, want)
	}

	// 4. Cycling back to automatic: the Friel zones return.
	h.saveProfileBody(t, `{"maxHr":190,"thresholdHr":160,"availableDays":["tue","thu","sat","sun"],"hoursPerAvailableDay":2,"restingHr":50,"hrZones":{"cycling":{"method":""}}}`)
	if got := secondsOf(fitnessZones(t, h, cyc)); !sameInts(got, friel) {
		t.Errorf("after switching back to automatic, cycling zones = %v, want %v", got, friel)
	}
}
