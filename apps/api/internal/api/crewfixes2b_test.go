package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A life event that ends and frees a day offers back what the plan would have
// made there. In a week built around a long crew ride that is never a second
// long ride.
func TestFreeingADayNeverOffersASecondLongRideInACrewRideWeek(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	ride := h.seedRide(s.crewID, s.route, cpNextSat, "wilant")
	goal := h.planFor("wilant")
	h.mustGo("wilant", ride.ID)
	h.fillWeek("wilant", goal, cpNextMonday) // built around the ride: no long ride

	event := `"kind":"busy","startDate":"` + cpNextSunday + `","endDate":"` + cpNextSunday + `"`
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/life-events", `{`+event+`}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create event: status = %d", resp.StatusCode)
	}
	var made lifeResult
	if err := json.NewDecoder(resp.Body).Decode(&made); err != nil || made.Event == nil {
		t.Fatalf("decode: %v", err)
	}

	resp = h.as("wilant", "cyclists", http.MethodDelete, "/api/training/life-events/"+made.Event.ID+"?dryRun=1", "")
	var preview lifeResult
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	for _, c := range preview.Diff.Changes {
		if c.Op == "add" && c.Date == cpNextSunday {
			t.Errorf("ending the event offers a second long ride: %+v", c)
		}
	}
}

const cpNextSunday = "2026-10-18"
