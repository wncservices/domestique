package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
)

func (h *crewPlanHarness) code(user, method, path, body string) int {
	h.t.Helper()
	return h.as(user, "cyclists", method, path, body).StatusCode
}

// crew.Crew.Owner is "who created the crew", informational only. Ride
// authority used to read it, so a creator who was demoted or removed kept
// listing, syncing and deleting the crew's rides (audit N1).
func TestRemovedCreatorLosesAuthorityOverCrewRides(t *testing.T) {
	h := newCrewPlanHarness(t)
	crewID := h.seedCrew("Club", "wilant", "sam", "alex")
	route := h.seedRoute("Loop", "sam", 50, 300, crewID)
	ride := h.seedRide(crewID, route.Slug, cpSaturday, "sam")
	ctx := context.Background()
	if err := h.crews.SetOwner(ctx, crewID, "sam", true); err != nil {
		t.Fatal(err)
	}
	if err := h.crews.Remove(ctx, crewID, "wilant"); err != nil {
		t.Fatal(err)
	}

	ridePath := "/api/crews/" + crewID + "/rides/" + ride.ID
	for name, c := range map[string]struct{ method, path, body string }{
		"list":     {"GET", "/api/crews/" + crewID + "/rides", ""},
		"sync":     {"POST", ridePath + "/sync", ""},
		"delete":   {"DELETE", ridePath, ""},
		"schedule": {"POST", "/api/crews/" + crewID + "/rides", `{"slug":"` + route.Slug + `","date":"2026-10-12"}`},
	} {
		if got := h.code("wilant", c.method, c.path, c.body); got != http.StatusForbidden && got != http.StatusNotFound {
			t.Errorf("removed creator %s = %d, want 403/404", name, got)
		}
	}
	if _, err := h.sched.Get(ctx, ride.ID); err != nil {
		t.Errorf("the ride was deleted by a removed creator: %v", err)
	}
}

func TestDemotedCreatorCannotDeleteAnotherMembersRide(t *testing.T) {
	h := newCrewPlanHarness(t)
	crewID := h.seedCrew("Club", "wilant", "sam")
	route := h.seedRoute("Loop", "sam", 50, 300, crewID)
	ride := h.seedRide(crewID, route.Slug, cpSaturday, "sam")
	ctx := context.Background()
	if err := h.crews.SetOwner(ctx, crewID, "sam", true); err != nil {
		t.Fatal(err)
	}
	if err := h.crews.SetOwner(ctx, crewID, "wilant", false); err != nil {
		t.Fatal(err)
	}

	if got := h.code("wilant", "DELETE", "/api/crews/"+crewID+"/rides/"+ride.ID, ""); got != http.StatusForbidden {
		t.Errorf("demoted creator deleting a member's ride = %d, want 403", got)
	}
	if got := h.code("wilant", "GET", "/api/crews/"+crewID+"/rides", ""); got != http.StatusOK {
		t.Errorf("a demoted creator is still a member and may read the rides: got %d", got)
	}
}

// A deleted crew's id is free to be handed out again. A route still naming it
// would then be shared with whoever creates a crew of the same name (N11).
func TestDeletingACrewClearsItsRouteTargets(t *testing.T) {
	h := newCrewPlanHarness(t)
	crewID := h.seedCrew("Club", "wilant", "sam")
	other := h.seedCrew("Other", "wilant", "sam")
	route := h.seedRoute("Loop", "wilant", 50, 300, crewID, other)

	if got := h.code("wilant", "DELETE", "/api/crews/"+crewID, ""); got != http.StatusOK {
		t.Fatalf("delete crew = %d", got)
	}
	routes, _, err := h.db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Slug != route.Slug {
			continue
		}
		if r.Targets == nil {
			t.Fatal("targets were wiped entirely, the other crew's share must survive")
		}
		if slices.Contains(*r.Targets, crewID) {
			t.Errorf("targets %v still name the deleted crew", *r.Targets)
		}
		if !slices.Contains(*r.Targets, other) {
			t.Errorf("targets %v lost the surviving crew", *r.Targets)
		}
	}

}

func TestLastOwnerCannotLeaveACrew(t *testing.T) {
	h := newCrewPlanHarness(t)
	crewID := h.seedCrew("Club", "wilant", "sam")

	resp := h.as("wilant", "cyclists", "DELETE", "/api/crews/"+crewID+"/members/wilant", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("last owner leaving = %d, want 409", resp.StatusCode)
	}
	if ok, _ := h.crews.IsOwner(context.Background(), crewID, "wilant"); !ok {
		t.Error("the last owner was removed anyway")
	}

	// With a second owner, leaving is fine.
	if err := h.crews.SetOwner(context.Background(), crewID, "sam", true); err != nil {
		t.Fatal(err)
	}
	if got := h.code("wilant", "DELETE", "/api/crews/"+crewID+"/members/wilant", ""); got != http.StatusOK {
		t.Errorf("an owner leaving a crew with another owner = %d, want 200", got)
	}
	// Now sam is the last owner and the same rule applies to them.
	if got := h.code("sam", "DELETE", "/api/crews/"+crewID+"/members/sam", ""); got != http.StatusConflict {
		t.Errorf("the sole remaining owner leaving = %d, want 409", got)
	}
}

// Any approved member can press "sync now", and each press pushes through
// every member's own stored provider session (N13).
func TestCrewSyncNowIsRateLimitedPerRide(t *testing.T) {
	h := newCrewPlanHarness(t)
	crewID := h.seedCrew("Club", "wilant", "sam")
	route := h.seedRoute("Loop", "wilant", 50, 300, crewID)
	ride := h.seedRide(crewID, route.Slug, cpSaturday, "wilant")
	// A second ride of the same crew: pressing Sync on one ride must not
	// lock the crew's other rides out.
	otherRide := h.seedRide(crewID, route.Slug, cpSunday, "wilant")
	h.srv.CrewSyncLimiter = ratelimit.New(2, time.Hour)

	var logs strings.Builder
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))

	sync := func(user, crewID, rideID string) int {
		return h.code(user, "POST", "/api/crews/"+crewID+"/rides/"+rideID+"/sync", "")
	}
	for i := 0; i < 2; i++ {
		if got := sync("wilant", crewID, ride.ID); got != http.StatusOK {
			t.Fatalf("sync %d = %d, want 200", i, got)
		}
	}
	if got := sync("sam", crewID, ride.ID); got != http.StatusTooManyRequests {
		t.Errorf("third sync (a different member, same crew) = %d, want 429", got)
	}
	if got := sync("sam", crewID, ride.ID); got != http.StatusTooManyRequests {
		t.Errorf("a refused press must not extend the window or count: got %d, want 429", got)
	}
	if got := sync("wilant", crewID, otherRide.ID); got != http.StatusOK {
		t.Errorf("another ride of the same crew has its own budget: got %d, want 200", got)
	}
	// An outsider's refused request (403) never touches the budget.
	if got := h.code("mallory", "POST", "/api/crews/"+crewID+"/rides/"+otherRide.ID+"/sync", ""); got != http.StatusForbidden && got != http.StatusNotFound {
		t.Errorf("outsider = %d", got)
	}
	if got := sync("wilant", crewID, otherRide.ID); got != http.StatusOK {
		t.Errorf("the outsider consumed the budget: got %d, want the second press to pass", got)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "crew sync") {
		t.Errorf("logs = %q, want a Warn for the limit hit", logs.String())
	}
}
