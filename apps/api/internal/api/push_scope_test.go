package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/targets"
)

// recordingTarget remembers which route slugs reached which account, so a
// test can see where a push actually went rather than only what the
// response reports.
type recordingTarget struct {
	account model.Account
	mu      *sync.Mutex
	pushed  map[string][]string
	// deleted, when set, records the remote ids deleted per account.
	deleted map[string][]string
}

func (r recordingTarget) Create(_ context.Context, rt model.Route) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushed[r.account.ID] = append(r.pushed[r.account.ID], rt.Slug)
	return "remote-" + rt.Slug, nil
}

func (r recordingTarget) Update(_ context.Context, id string, rt model.Route) (string, error) {
	return r.Create(context.Background(), rt)
}

func (r recordingTarget) Delete(_ context.Context, id string) error {
	if r.deleted == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted[r.account.ID] = append(r.deleted[r.account.ID], id)
	return nil
}

// recordingHarness is an auth harness whose pushes and deletes are recorded.
func recordingHarness(t *testing.T) (h *authHarness, mu *sync.Mutex, pushed, deleted map[string][]string) {
	mu = &sync.Mutex{}
	pushed, deleted = map[string][]string{}, map[string][]string{}
	h = newAuthHarnessWithTargets(t, nil, func(a model.Account) (targets.Target, error) {
		return recordingTarget{account: a, mu: mu, pushed: pushed, deleted: deleted}, nil
	})
	return h, mu, pushed, deleted
}

// Leaving a fellow's private route out of the plan must not turn it into a
// delete: a route missing from the routes a plan is built from reads as
// removed from the library. Friend's own route is already on friend's device
// when wilant pushes; it has to stay there, and wilant's plan must not name it.
func TestAPushNeverDeletesAFellowRidersPrivateRoute(t *testing.T) {
	h, mu, _, deleted := recordingHarness(t)
	if resp := h.as("friend", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("friend linking wahoo: status = %d", resp.StatusCode)
	}
	h.seedApprovedCrew(t, "wilant", "friend")
	friendRoute := h.seedRoute(t, "Friend Private", "friend")
	if resp := h.as("friend", "cyclists", http.MethodPost, "/api/push", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("friend push: status = %d", resp.StatusCode)
	}

	plan := h.as("wilant", "cyclists", http.MethodGet, "/api/plan", "")
	var planned struct {
		Items []struct {
			Slug string `json:"slug"`
		} `json:"items"`
	}
	if err := json.NewDecoder(plan.Body).Decode(&planned); err != nil {
		t.Fatal(err)
	}
	for _, item := range planned.Items {
		if item.Slug == friendRoute.Slug {
			t.Errorf("wilant's plan names friend's private route: %+v", planned.Items)
		}
	}
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/push", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("wilant push: status = %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deleted["wahoo:friend"]) != 0 {
		t.Errorf("wilant's push deleted %v from friend's device", deleted["wahoo:friend"])
	}
}

// A crew ride sends one route. Every other route already on a member's device
// is not part of that and must stay: built from the one route alone, the plan
// would read all of them as removed from the library.
func TestSchedulingACrewRideLeavesAMembersOtherRoutes(t *testing.T) {
	h, mu, pushed, deleted := recordingHarness(t)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	if resp := h.as("friend", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("friend linking wahoo: status = %d", resp.StatusCode)
	}
	h.seedRoute(t, "Friend Own", "friend")
	if resp := h.as("friend", "cyclists", http.MethodPost, "/api/push", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("friend push: status = %d", resp.StatusCode)
	}
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})
	ride := h.mustScheduleRide(t, "wilant", crewID, route.Slug, "2026-09-05")
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides/"+ride.ID+"/sync", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("sync now: status = %d", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deleted["wahoo:friend"]) != 0 {
		t.Errorf("the crew ride deleted %v from friend's device", deleted["wahoo:friend"])
	}
	if !slugIn(pushed["wahoo:friend"], route.Slug) {
		t.Errorf("the ride's route did not reach friend's device; pushed = %v", pushed)
	}
}

// TestAPushNeverReachesAFellowRidersPrivateRoute is the crew case of
// handlePush: a rider's "Push to devices" acts only on the routes that rider
// may see, not the whole library against every crew-visible account. Without
// that, a crew fellow's own unshared route is pushed to that fellow's head
// unit, and its slug comes back in the response.
func TestAPushNeverReachesAFellowRidersPrivateRoute(t *testing.T) {
	var mu sync.Mutex
	pushed := map[string][]string{}
	h := newAuthHarnessWithTargets(t, nil, func(a model.Account) (targets.Target, error) {
		return recordingTarget{account: a, mu: &mu, pushed: pushed}, nil
	})

	if resp := h.as("friend", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("friend linking wahoo: status = %d", resp.StatusCode)
	}
	h.seedApprovedCrew(t, "wilant", "friend")

	// friend's own route, owned by friend, shared with nobody.
	friendRoute := h.seedRoute(t, "Friend Private", "friend")
	// wilant's own route, to show the push still works for the caller.
	ownRoute := h.seedRoute(t, "Wilant Own", "wilant")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/push", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wilant push: status = %d", resp.StatusCode)
	}
	var result struct {
		Items []struct {
			Slug string `json:"slug"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	for _, item := range result.Items {
		if item.Slug == friendRoute.Slug {
			t.Errorf("wilant's push response names friend's private route %q", item.Slug)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, slug := range pushed["wahoo:friend"] {
		if slug == friendRoute.Slug {
			t.Errorf("friend's private route reached friend's wahoo account on wilant's push")
		}
	}
	if !slugIn(pushed["garmin:wilant"], ownRoute.Slug) {
		t.Errorf("wilant's own route did not reach wilant's garmin account; pushed = %v", pushed)
	}
}

func slugIn(slugs []string, want string) bool {
	for _, s := range slugs {
		if s == want {
			return true
		}
	}
	return false
}
