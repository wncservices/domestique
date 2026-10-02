package api_test

import (
	"math"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// A pacing plan for route B with a goal that is linked to route A would carry
// A's intensity and event onto B: refused, for the JSON plan too.
func TestPacingRefusesAGoalLinkedToAnotherRoute(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	a := h.addRoute("wilant", "Route A", true)
	b := h.addRoute("wilant", "Route B", true)
	goalA := h.goalFor("wilant", a, model.SportCycling)
	noRoute := h.goalFor("wilant", "", model.SportCycling)

	for name, goal := range map[string]string{"a goal linked to another route": goalA.ID, "a goal with no route": noRoute.ID} {
		resp, body := h.pacing("wilant", b, "goal="+goal)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422: %s", name, resp.StatusCode, body)
		}
	}
	// The matching goal still works.
	if out := h.mustPacing("wilant", a, "goal="+goalA.ID); !out.Available {
		t.Errorf("the goal's own route is refused: %+v", out)
	}
}

// A 700 m ramp comes before a 2 km climb. The training bar (1 km) keeps only the
// big climb, the device bar (500 m) keeps both, and every surface must call the
// big one C2: the demands card, the pacing plan and the head unit's cue.
func TestARampBeforeTheBigClimbDoesNotRenumberIt(t *testing.T) {
	h := newDemandsHarness(t)
	h.rider("wilant", 250)
	rt, err := h.lib.Create(t.Context(), source.CreateRequest{
		Name: "Ramp Then Climb", UploadedBy: "wilant",
		GPX: routefixture.GPX("Ramp Then Climb", true, 25, 100,
			routefixture.Piece{LengthM: 1000, Grade: 0}, routefixture.Piece{LengthM: 700, Grade: 7},
			routefixture.Piece{LengthM: 800, Grade: -5}, routefixture.Piece{LengthM: 600, Grade: 0},
			routefixture.Piece{LengthM: 2000, Grade: 6}, routefixture.Piece{LengthM: 1000, Grade: 0}),
	})
	if err != nil {
		t.Fatal(err)
	}
	g := h.goalFor("wilant", rt.Slug, model.SportCycling)

	demands := h.mustDemands("wilant", g.ID)
	plan := h.mustPacing("wilant", rt.Slug, "goal="+g.ID)
	if len(plan.Climbs) != 2 {
		t.Fatalf("the pacing plan has %d climbs, want the ramp and the big climb: %+v", len(plan.Climbs), plan.Climbs)
	}
	if len(demands.Climbs) != 1 {
		t.Fatalf("the demands card has %d climbs, want only the big one (the ramp is under 1 km): %+v", len(demands.Climbs), demands.Climbs)
	}
	big := demands.Climbs[0]
	if big.Index != 0 || big.DeviceIndex != 1 {
		t.Errorf("big climb index %d, device index %d; want 0 and 1 (C2)", big.Index, big.DeviceIndex)
	}
	// The plan's second climb is the same stretch of road.
	if p := plan.Climbs[big.DeviceIndex]; math.Abs(p.StartM-big.StartM) > 1 || math.Abs(p.Watts-big.Watts) > 0.5 {
		t.Errorf("device index %d in the plan is %+v, not the demands card's climb %+v", big.DeviceIndex, p, big)
	}
	// The plan's table calls its segment C2 as well.
	var seg *int
	for _, s := range plan.Segments {
		if s.Kind == "climb" && math.Abs(s.StartM-big.StartM) < 150 {
			seg = s.ClimbIndex
		}
	}
	if seg == nil || *seg != 1 {
		t.Errorf("the plan's climb segment index = %v, want 1", seg)
	}
}
