package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// privateNeedles are strings that must never reach another crew member: the
// text of a rider's goal and life event note, and the keys under which FTP,
// heart rate, wellness, readiness, goals and life events travel. A response
// that holds any of them is leaking something the spec's "what crosses" table
// does not allow.
var privateNeedles = []string{
	"Secret Goal Zeta", "quokka-private-note",
	`"ftp`, `"maxHr`, `"restingHr`, `"thresholdHr`, `"hrv`, `"sleep`, `"readiness`, `"verdict`,
	`"goal`, `"lifeEvent`, `"availableDays`, `"wellness`, `"weightKg`,
}

// seedPrivateData gives rider the data a peer must never see.
func (h *crewPlanHarness) seedPrivateData(rider string) {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: rider, Name: "Secret Goal Zeta", Priority: workout.PriorityA, EventDate: "2027-05-01"}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: rider, FTPWatts: 317, MaxHR: 193, RestingHR: 41, ThresholdHR: 171,
		HoursPerAvailableDay: 2, AvailableDays: []string{"tue", "thu", "sat"},
	}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.training.CreateLifeEvent(ctx, workout.LifeEvent{Rider: rider, Kind: "busy", Start: cpNextSat, End: cpNextSat, Note: "quokka-private-note"}); err != nil {
		h.t.Fatal(err)
	}
	if err := h.training.SaveWellness(ctx, workout.DailyWellness{Rider: rider, Date: cpToday, HRVLastNight: 77, SleepSeconds: 20000, RestingHR: 41, ReadinessScore: 12, ReadinessLevel: "low"}); err != nil {
		h.t.Fatal(err)
	}
}

// assertNothingPrivate fails if a peer's response body holds anything a rider
// keeps to themselves.
func assertNothingPrivate(t *testing.T, label, body string) {
	t.Helper()
	for _, needle := range privateNeedles {
		if strings.Contains(body, needle) {
			t.Errorf("%s leaks %q to another crew member:\n%s", label, needle, body)
		}
	}
}

// assertOnlyKeys fails if any JSON object in body (an array of objects or one
// object) has a key outside allowed.
func assertOnlyKeys(t *testing.T, label, body string, allowed ...string) {
	t.Helper()
	ok := map[string]bool{}
	for _, k := range allowed {
		ok[k] = true
	}
	var rows []map[string]json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(body), "[") {
		if err := json.Unmarshal([]byte(body), &rows); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	} else {
		var one map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &one); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		rows = []map[string]json.RawMessage{one}
	}
	for _, row := range rows {
		for k := range row {
			if !ok[k] {
				t.Errorf("%s carries the undocumented key %q", label, k)
			}
		}
	}
}
