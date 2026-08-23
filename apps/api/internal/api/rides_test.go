package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

type rideOut struct {
	ID        string `json:"id"`
	CrewID    string `json:"crewId"`
	Slug      string `json:"slug"`
	RouteName string `json:"routeName"`
	Date      string `json:"date"`
	CreatedBy string `json:"createdBy"`
}

func (h *authHarness) decodeRides(t *testing.T, resp *http.Response) []rideOut {
	t.Helper()
	var out []rideOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *authHarness) decodeRide(t *testing.T, resp *http.Response) rideOut {
	t.Helper()
	var out rideOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// mustScheduleRide is scheduleRide plus a 201 assertion, for tests where the
// ride's own creation is setup rather than the thing under test.
func (h *authHarness) mustScheduleRide(t *testing.T, user, crewID, slug, date string) rideOut {
	t.Helper()
	resp := h.as(user, "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides",
		`{"slug":"`+slug+`","date":"`+date+`"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("schedule ride: status = %d, want 201", resp.StatusCode)
	}
	return h.decodeRide(t, resp)
}

func (h *authHarness) routeTargetsFor(t *testing.T, slug string) []string {
	t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/routes", "")
	var library struct {
		Routes []struct {
			Slug    string   `json:"slug"`
			Targets []string `json:"targets"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&library); err != nil {
		t.Fatal(err)
	}
	for _, r := range library.Routes {
		if r.Slug == slug {
			return r.Targets
		}
	}
	t.Fatalf("no route with slug %q", slug)
	return nil
}

// TestOwnerSchedulesAnAlreadySharedRoute proves the base case: the crew
// owner, scheduling a route already targeted to their own crew, needs no
// retargeting at all.
func TestOwnerSchedulesAnAlreadySharedRoute(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides",
		`{"slug":"`+route.Slug+`","date":"2026-09-05"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	ride := h.decodeRide(t, resp)
	if ride.Slug != route.Slug || ride.Date != "2026-09-05" || ride.CreatedBy != "wilant" {
		t.Fatalf("ride = %+v", ride)
	}
	if ride.RouteName != "Hill Loop" {
		t.Errorf("routeName = %q, want Hill Loop", ride.RouteName)
	}

	list := h.decodeRides(t, h.as("friend", "cyclists", http.MethodGet, "/api/crews/"+crewID+"/rides", ""))
	if len(list) != 1 || list[0].ID != ride.ID {
		t.Fatalf("an approved member should see the scheduled ride, got %+v", list)
	}
}

// TestOwnerSchedulingAnUnsharedRouteSharesItToo proves scheduling shares
// the route to the crew when it isn't already, as long as the caller has
// the authority to retarget it (here, the owner of both the crew and the
// route).
func TestOwnerSchedulingAnUnsharedRouteSharesItToo(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	route := h.seedRoute(t, "Flat Loop", "wilant") // no targets — owner-only default

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides",
		`{"slug":"`+route.Slug+`","date":"2026-09-06"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	targets := h.routeTargetsFor(t, route.Slug)
	if len(targets) != 1 || targets[0] != crewID {
		t.Fatalf("route targets = %v, want [%s] — scheduling should have shared it", targets, crewID)
	}
}

// TestGrantedMemberSchedulingAnUnsharedRouteIsRejected proves CanSchedule is
// not a route-edit permission: a member who may schedule rides for the crew
// still cannot retarget somebody else's route themselves — the route must
// already be shared before they can plan around it.
func TestGrantedMemberSchedulingAnUnsharedRouteIsRejected(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	route := h.seedRoute(t, "Flat Loop", "wilant")

	if resp := h.as("wilant", "cyclists", http.MethodPatch,
		"/api/crews/"+crewID+"/members/friend/schedule", `{"canSchedule":true}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("grant: status = %d", resp.StatusCode)
	}

	resp := h.as("friend", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides",
		`{"slug":"`+route.Slug+`","date":"2026-09-06"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (route not shared with the crew yet)", resp.StatusCode)
	}
}

// TestUngrantedMemberCannotSchedule proves an approved member with no grant,
// and no ownership, is refused outright — even for a route already shared.
func TestUngrantedMemberCannotSchedule(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})

	resp := h.as("friend", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides",
		`{"slug":"`+route.Slug+`","date":"2026-09-05"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestGrantedMemberSchedulesAnAlreadySharedRoute is the success path the two
// tests above bracket: once granted, and the route is already shared, a
// non-owner member may schedule it.
func TestGrantedMemberSchedulesAnAlreadySharedRoute(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})

	if resp := h.as("wilant", "cyclists", http.MethodPatch,
		"/api/crews/"+crewID+"/members/friend/schedule", `{"canSchedule":true}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("grant: status = %d", resp.StatusCode)
	}

	resp := h.as("friend", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides",
		`{"slug":"`+route.Slug+`","date":"2026-09-05"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
}

// TestOnlyOwnerOrAdminMayGrantCanSchedule proves the grant itself is
// owner/admin-gated, the same rule auto-share already uses.
func TestOnlyOwnerOrAdminMayGrantCanSchedule(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend", "third")

	resp := h.as("friend", "cyclists", http.MethodPatch,
		"/api/crews/"+crewID+"/members/third/schedule", `{"canSchedule":true}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestNonMemberCannotListRides proves rides are not visible to a rider
// outside the crew, the same audience rule as the roster/read side.
func TestNonMemberCannotListRides(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant")

	resp := h.as("stranger", "cyclists", http.MethodGet, "/api/crews/"+crewID+"/rides", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestDeleteRide proves the three eligible deleters (creator, owner, admin)
// each work, and a fourth rider does not.
func TestDeleteRide(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend", "stranger")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})
	if resp := h.as("wilant", "cyclists", http.MethodPatch,
		"/api/crews/"+crewID+"/members/friend/schedule", `{"canSchedule":true}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("grant: status = %d", resp.StatusCode)
	}

	t.Run("an uninvolved member cannot delete", func(t *testing.T) {
		ride := h.mustScheduleRide(t, "friend", crewID, route.Slug, "2026-09-05")
		resp := h.as("stranger", "cyclists", http.MethodDelete,
			"/api/crews/"+crewID+"/rides/"+ride.ID, "")
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("its own creator can delete it", func(t *testing.T) {
		ride := h.mustScheduleRide(t, "friend", crewID, route.Slug, "2026-09-05")
		resp := h.as("friend", "cyclists", http.MethodDelete,
			"/api/crews/"+crewID+"/rides/"+ride.ID, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("the crew owner can delete somebody else's", func(t *testing.T) {
		ride := h.mustScheduleRide(t, "friend", crewID, route.Slug, "2026-09-05")
		resp := h.as("wilant", "cyclists", http.MethodDelete,
			"/api/crews/"+crewID+"/rides/"+ride.ID, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("an admin can delete somebody else's", func(t *testing.T) {
		ride := h.mustScheduleRide(t, "friend", crewID, route.Slug, "2026-09-05")
		resp := h.as("boss", "domestique-admins", http.MethodDelete,
			"/api/crews/"+crewID+"/rides/"+ride.ID, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}

// TestCrewDTOReportsRosterToApprovedMembersOnly proves who-is-in-the-crew is
// visible to any approved member, not only the owner — the roster a crew
// detail popup shows — while a rider who has only requested to join (or
// isn't involved at all) still sees nothing more than the count.
func TestCrewDTOReportsRosterToApprovedMembersOnly(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	if resp := h.as("stranger", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/join", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("request to join: status = %d", resp.StatusCode)
	}

	type out struct {
		Roster []string `json:"roster"`
	}
	decodeFirst := func(user string) out {
		t.Helper()
		resp := h.as(user, "cyclists", http.MethodGet, "/api/crews", "")
		var list []out
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 {
			t.Fatalf("len(list) = %d, want 1", len(list))
		}
		return list[0]
	}

	for _, user := range []string{"wilant", "friend"} {
		roster := decodeFirst(user)
		if len(roster.Roster) != 2 {
			t.Errorf("%s: roster = %v, want both approved members", user, roster.Roster)
		}
	}
	if roster := decodeFirst("stranger"); roster.Roster != nil {
		t.Errorf("a pending requester should not see the roster yet, got %v", roster.Roster)
	}
}

// TestCrewDTOReportsCanSchedule proves the crew list response tells each
// viewer their own scheduling standing, and the owner's roster view the
// per-member grant — the two things the frontend needs to gate its UI.
func TestCrewDTOReportsCanSchedule(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	if resp := h.as("wilant", "cyclists", http.MethodPatch,
		"/api/crews/"+crewID+"/members/friend/schedule", `{"canSchedule":true}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("grant: status = %d", resp.StatusCode)
	}

	var asFriend struct {
		CanSchedule bool `json:"canSchedule"`
	}
	resp := h.as("friend", "cyclists", http.MethodGet, "/api/crews", "")
	var list []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if err := json.Unmarshal(list[0], &asFriend); err != nil {
		t.Fatal(err)
	}
	if !asFriend.CanSchedule {
		t.Error("friend's own crewDTO.canSchedule should be true after the grant")
	}

	var asOwner struct {
		Members []struct {
			Rider       string `json:"rider"`
			CanSchedule bool   `json:"canSchedule"`
		} `json:"members"`
	}
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/crews", "")
	list = nil
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(list[0], &asOwner); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range asOwner.Members {
		if m.Rider == "friend" {
			found = true
			if !m.CanSchedule {
				t.Error("owner's roster view should show friend's grant")
			}
		}
	}
	if !found {
		t.Fatal("friend not found in the owner's own member list")
	}
}
