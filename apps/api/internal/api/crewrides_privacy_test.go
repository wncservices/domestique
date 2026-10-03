package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Privacy: a crew mate sees who is going and the route's own estimate, and
// nothing about the rider's plan, fitness, health, goals or life events.
func TestAPeerNeverSeesAnythingPrivateOnTheCrewRideEndpoints(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.seedPrivateData("wilant")
	h.mustGo("wilant", s.ride)

	// sam reads the list and joins himself: both are peer-facing responses.
	resp := h.as("sam", "cyclists", http.MethodGet, "/api/training/crew-rides?from="+cpToday, "")
	list := bodyOf(t, resp)
	assertNothingPrivate(t, "GET crew-rides", list)
	assertOnlyKeys(t, "GET crew-rides", list,
		"id", "crewId", "crewName", "slug", "routeName", "date", "time", "going", "mine", "km", "ascentM", "minutes", "tss", "kind")
	if !strings.Contains(list, `"wilant"`) {
		t.Error("the list should name who is going: that is what crosses")
	}

	put := h.as("sam", "cyclists", http.MethodPut, "/api/training/crew-rides/"+s.ride+"/going", `{"going":true}`)
	assertNothingPrivate(t, "PUT going", bodyOf(t, put))
}

// No write of this endpoint reaches another rider's workouts, whoever asks.
func TestGoingNeverWritesAnotherRidersWorkouts(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("wilant", s.ride)
	before := h.stored("wilant")

	h.mustGo("sam", s.ride)
	h.mustLeave("sam", s.ride)
	h.setGoing("sam", s.ride, `{"going":true,"dryRun":true}`)
	h.setGoing("outsider", s.ride, `{"going":false}`)

	after := h.stored("wilant")
	if len(after) != len(before) {
		t.Fatalf("wilant has %d workouts, had %d", len(after), len(before))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].UpdatedAt != after[i].UpdatedAt || before[i].Description != after[i].Description {
			t.Errorf("wilant's workout %s was written by somebody else's request", before[i].ID)
		}
	}
}

// A dry run writes nothing at all.
func TestADryRunOfGoingWritesNothing(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	resp, out := h.setGoing("wilant", s.ride, `{"going":true,"dryRun":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if out.Workout != nil {
		t.Error("a dry run returned a session")
	}
	if _, ok := h.fixedRow("wilant", s.ride); ok {
		t.Error("a dry run created the fixed session")
	}
	if going, _ := h.sched.Going(context.Background(), s.ride); len(going) != 0 {
		t.Errorf("a dry run recorded %v as going", going)
	}
}
