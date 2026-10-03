package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A route un-shared after a goal linked it must not make the goal uneditable:
// the edit form sends the current link back on every save.
func TestEditingAGoalWhoseRouteWasUnsharedKeepsTheLink(t *testing.T) {
	h := newRouteHarness(t)
	// Straight through the store: the route is not visible to the rider, as if
	// it had been shared with them and then withdrawn.
	slug := h.addRoute("someone-else", "Withdrawn Hills", true)
	g, err := h.store.CreateGoal(t.Context(), workout.CreateGoalRequest{Rider: "wilant", Name: "Fondo", EventDate: "2027-05-01", RouteSlug: slug})
	if err != nil {
		t.Fatal(err)
	}

	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"eventDate":"2027-06-01","routeSlug":"`+slug+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("editing the date with the current link sent back: status %d", resp.StatusCode)
	}
	got, _ := h.store.GetGoal(t.Context(), g.ID)
	if got.EventDate != "2027-06-01" || got.RouteSlug != slug {
		t.Errorf("goal = %+v, want the new date and the link kept", got)
	}
	// Changing to a different invisible route is still refused.
	other := h.addRoute("someone-else", "Other Hills", true)
	if got := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"routeSlug":"`+other+`"}`).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("linking a different invisible route: %d, want 422", got)
	}
	// And a routed goal still cannot become a running goal, link unchanged or not.
	if got := h.as("wilant", "cyclists", http.MethodPatch, "/api/training/goals/"+g.ID, `{"sport":"running","routeSlug":"`+slug+`"}`).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("running with a route: %d, want 422", got)
	}
}

func TestGoalRoutesListsOnlyTheCyclingRoutesTheRiderMaySee(t *testing.T) {
	h := newRouteHarness(t)
	own := h.addRoute("wilant", "Own Hills", true)
	h.addRoute("someone-else", "Private Hills", true)
	run, err := h.lib.Create(t.Context(), source.CreateRequest{
		Name: "Trail", UploadedBy: "wilant", Sport: model.SportRunning,
		GPX: routefixture.GPX("Trail", true, 25, 100, routefixture.Hilly()...),
	})
	if err != nil {
		t.Fatal(err)
	}
	// An admin sees every route in the library, but the picker offers what the
	// server will accept.
	for _, groups := range []string{"cyclists", "admins"} {
		resp := h.as("wilant", groups, http.MethodGet, "/api/training/goal-routes", "")
		var out []struct{ Slug, Name string }
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK || len(out) != 1 || out[0].Slug != own {
			t.Errorf("%s: status %d, routes %+v; want only %q (not the private route or %q)", groups, resp.StatusCode, out, own, run.Slug)
		}
	}
	if got := h.as("wilant", "guests", http.MethodGet, "/api/training/goal-routes", "").StatusCode; got != http.StatusForbidden {
		t.Errorf("a viewer: %d, want 403", got)
	}
}
