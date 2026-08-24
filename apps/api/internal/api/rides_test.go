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
	Time      string `json:"time"`
	CreatedBy string `json:"createdBy"`
}

type upcomingRideOut struct {
	rideOut
	CrewName string `json:"crewName"`
}

func (h *authHarness) decodeUpcomingRides(t *testing.T, resp *http.Response) []upcomingRideOut {
	t.Helper()
	var out []upcomingRideOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
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

// mustScheduleRideAt is mustScheduleRide plus an explicit time of day, for
// tests exercising the time field itself — kept separate so every existing
// mustScheduleRide call site (which only ever cared about the date) didn't
// need to grow an argument it has no use for.
func (h *authHarness) mustScheduleRideAt(t *testing.T, user, crewID, slug, date, timeOfDay string) rideOut {
	t.Helper()
	resp := h.as(user, "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides",
		`{"slug":"`+slug+`","date":"`+date+`","time":"`+timeOfDay+`"}`)
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

// TestGrantedMemberSchedulingAnUnsharedRouteIsRejected proves two things at
// once: CanSchedule is not a route-edit permission (a member who may
// schedule rides for the crew still cannot retarget somebody else's route
// themselves — it must already be shared before they can plan around it),
// and that rejection must not leak the route's existence — a route the
// caller cannot otherwise see (config.VisibleTo) has to come back 404, the
// same as a genuinely nonexistent slug, not a 400 that would tell an
// unrelated rider "that one exists, it's just not shared with you."
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
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — friend cannot see this route, so it must look nonexistent", resp.StatusCode)
	}
}

// TestSchedulingAnInvisibleRouteDoesNotLeakItsExistence is
// TestGrantedMemberSchedulingAnUnsharedRouteIsRejected's own scenario
// generalized: literally any rider may schedule for a crew they own (owning
// any crew grants Mine, and Mine always grants CanSchedule — see
// crewAuthorityFor), which without the visibility filter in
// handleCreateRide would turn this endpoint into a way to probe whether an
// arbitrary slug exists anywhere in the deployment, regardless of any
// relationship to it. A route with no owner and no crew is invisible to
// everyone but an admin (config.VisibleTo), so it stands in for "a route
// this rider has absolutely nothing to do with."
func TestSchedulingAnInvisibleRouteDoesNotLeakItsExistence(t *testing.T) {
	h := newAuthHarness(t, nil)
	ownCrewID := h.seedApprovedCrew(t, "prober") // owner — always CanSchedule
	invisible := h.seedRoute(t, "Somebody Else's Route", "wilant")

	resp := h.as("prober", "cyclists", http.MethodPost, "/api/crews/"+ownCrewID+"/rides",
		`{"slug":"`+invisible.Slug+`","date":"2026-09-06"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — indistinguishable from a slug that doesn't exist at all", resp.StatusCode)
	}

	// A slug that truly doesn't exist must produce the exact same response.
	respFake := h.as("prober", "cyclists", http.MethodPost, "/api/crews/"+ownCrewID+"/rides",
		`{"slug":"no-such-route-at-all","date":"2026-09-06"}`)
	if respFake.StatusCode != resp.StatusCode {
		t.Fatalf("a real-but-invisible slug and a fake one must return the same status, got %d vs %d",
			resp.StatusCode, respFake.StatusCode)
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

	t.Run("a creator who has since left the crew can no longer delete it", func(t *testing.T) {
		ride := h.mustScheduleRide(t, "friend", crewID, route.Slug, "2026-09-05")
		if resp := h.as("friend", "cyclists", http.MethodDelete,
			"/api/crews/"+crewID+"/members/friend", ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("leave crew: status = %d", resp.StatusCode)
		}

		resp := h.as("friend", "cyclists", http.MethodDelete,
			"/api/crews/"+crewID+"/rides/"+ride.ID, "")
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 — friend is no longer a member", resp.StatusCode)
		}

		// The crew owner can still clean it up.
		if resp := h.as("wilant", "cyclists", http.MethodDelete,
			"/api/crews/"+crewID+"/rides/"+ride.ID, ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("owner cleanup: status = %d, want 200", resp.StatusCode)
		}
	})
}

// TestRideSurvivesItsRouteBeingDeleted proves a ride whose route has since
// been deleted still lists cleanly — routeName falls back to the slug
// instead of coming back empty — and can still be removed like any other
// ride.
func TestRideSurvivesItsRouteBeingDeleted(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})
	ride := h.mustScheduleRide(t, "wilant", crewID, route.Slug, "2026-09-05")

	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/routes/"+route.Slug, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete route: status = %d", resp.StatusCode)
	}

	rides := h.decodeRides(t, h.as("wilant", "cyclists", http.MethodGet, "/api/crews/"+crewID+"/rides", ""))
	if len(rides) != 1 || rides[0].RouteName != route.Slug {
		t.Fatalf("rides = %+v, want one ride whose routeName fell back to %q", rides, route.Slug)
	}

	if resp := h.as("wilant", "cyclists", http.MethodDelete,
		"/api/crews/"+crewID+"/rides/"+ride.ID, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete ride: status = %d", resp.StatusCode)
	}
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

// TestDeletingACrewPurgesItsRides proves the fix for a real stale-data leak:
// crew ids are deterministic slugs and freed for reuse the moment a crew is
// deleted (crew.Store.uniqueID only checks currently-existing crews), so a
// brand new, completely unrelated crew that happens to get the same name
// would otherwise inherit the old crew's scheduled rides the instant
// anyone lists them.
func TestDeletingACrewPurgesItsRides(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})
	h.mustScheduleRide(t, "wilant", crewID, route.Slug, "2026-09-05")

	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+crewID, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete crew: status = %d", resp.StatusCode)
	}

	resp := h.as("stranger", "cyclists", http.MethodPost, "/api/crews", `{"name":"Crew"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("recreate: status = %d", resp.StatusCode)
	}
	var recreated struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&recreated); err != nil {
		t.Fatal(err)
	}
	if recreated.ID != crewID {
		t.Fatalf("recreated crew id = %q, want the reused %q — this test's whole premise depends on id reuse", recreated.ID, crewID)
	}

	rides := h.decodeRides(t, h.as("stranger", "cyclists", http.MethodGet, "/api/crews/"+recreated.ID+"/rides", ""))
	if len(rides) != 0 {
		t.Fatalf("a brand new crew that reused the old one's id should start with no rides, got %+v", rides)
	}
}

type syncOut struct {
	Applied int `json:"applied"`
	Items   []struct {
		AccountID string `json:"accountId"`
	} `json:"items"`
}

func (h *authHarness) decodeSync(t *testing.T, resp *http.Response) syncOut {
	t.Helper()
	var out syncOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func syncAccountIDs(out syncOut) map[string]bool {
	got := make(map[string]bool, len(out.Items))
	for _, item := range out.Items {
		got[item.AccountID] = true
	}
	return got
}

// TestSyncRide proves the crew ride scheduler's own explicit "sync now"
// action is the one deliberate exception to the rule general-purpose push
// now follows everywhere else (config.PushTargetsFor — see that function's
// own doc comment): it may still reach every one of a crew's own
// currently-approved members' accounts, not just the clicking rider's own,
// for the one route named on this one ride.
func TestSyncRide(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})
	if resp := h.as("friend", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("friend linking wahoo: status = %d", resp.StatusCode)
	}
	ride := h.mustScheduleRide(t, "wilant", crewID, route.Slug, "2026-09-05")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides/"+ride.ID+"/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := h.decodeSync(t, resp)
	if out.Applied != 2 {
		t.Fatalf("applied = %d, want 2 (wilant's own account and friend's)", out.Applied)
	}
	got := syncAccountIDs(out)
	if !got["garmin:wilant"] || !got["wahoo:friend"] {
		t.Fatalf("items = %+v, want garmin:wilant and wahoo:friend", out.Items)
	}
}

// TestSyncRideNeverLeaksIntoAnotherCrew proves the second, defense-in-depth
// scope handleSyncRide applies on top of crew-aware TargetsFor: a route
// independently shared to two crews (a real possibility — Targets is just
// a list) must only sync to the crew whose ride was actually clicked, not
// spill into the other crew the same route's owner also happens to belong
// to.
func TestSyncRideNeverLeaksIntoAnotherCrew(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewA := h.seedApprovedCrew(t, "wilant", "friend")
	crewB := h.seedApprovedCrew(t, "wilant", "stranger")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewA, crewB})
	if resp := h.as("stranger", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"garmin"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("stranger linking garmin: status = %d", resp.StatusCode)
	}
	ride := h.mustScheduleRide(t, "wilant", crewA, route.Slug, "2026-09-05")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+crewA+"/rides/"+ride.ID+"/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := h.decodeSync(t, resp)
	if got := syncAccountIDs(out); got["garmin:stranger"] {
		t.Fatalf("items = %+v, syncing crewA's ride must never reach crewB's stranger", out.Items)
	}
}

// TestSyncRideRequiresApprovedMembership proves a rider outside the crew
// entirely cannot trigger a sync just by knowing a ride id.
func TestSyncRideRequiresApprovedMembership(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})
	ride := h.mustScheduleRide(t, "wilant", crewID, route.Slug, "2026-09-05")

	resp := h.as("stranger", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides/"+ride.ID+"/sync", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestSyncRideDoesNotRequireCanSchedule proves any approved member may
// trigger a sync, not only whoever holds the CanSchedule grant — matching
// the frontend, which shows "Sync now" to every crew member on a ride,
// same as it shows the ride itself.
func TestSyncRideDoesNotRequireCanSchedule(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant", "friend")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})
	ride := h.mustScheduleRide(t, "wilant", crewID, route.Slug, "2026-09-05")

	resp := h.as("friend", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides/"+ride.ID+"/sync", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an approved member with no CanSchedule grant may still sync", resp.StatusCode)
	}
}

// TestSyncRideMissingRide proves a ride id that doesn't belong to this
// crew — wrong crew, or doesn't exist at all — 404s rather than syncing
// nothing silently.
func TestSyncRideMissingRide(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides/nope/sync", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestScheduleRideWithTimeRoundTrips proves the optional time-of-day field
// survives create and list intact, and that leaving it out still works
// exactly as before this field existed.
func TestScheduleRideWithTimeRoundTrips(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})

	timed := h.mustScheduleRideAt(t, "wilant", crewID, route.Slug, "2026-09-05", "09:30")
	if timed.Time != "09:30" {
		t.Fatalf("timed.Time = %q, want 09:30", timed.Time)
	}

	untimed := h.mustScheduleRide(t, "wilant", crewID, route.Slug, "2026-09-06")
	if untimed.Time != "" {
		t.Fatalf("untimed.Time = %q, want empty — no time was given", untimed.Time)
	}

	list := h.decodeRides(t, h.as("wilant", "cyclists", http.MethodGet, "/api/crews/"+crewID+"/rides", ""))
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	for _, ride := range list {
		if ride.ID == timed.ID && ride.Time != "09:30" {
			t.Errorf("listed timed ride.Time = %q, want 09:30", ride.Time)
		}
		if ride.ID == untimed.ID && ride.Time != "" {
			t.Errorf("listed untimed ride.Time = %q, want empty", ride.Time)
		}
	}
}

// TestScheduleRideRejectsAMalformedTime proves the API layer surfaces
// schedule.Store's own time validation as a 400, the same as it already
// does for a malformed date.
func TestScheduleRideRejectsAMalformedTime(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+crewID+"/rides",
		`{"slug":"`+route.Slug+`","date":"2026-09-05","time":"25:99"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestUpcomingRidesSpansOwnCrewsOnly proves GET /api/rides/upcoming shows a
// rider every upcoming ride across every crew they belong to, and nothing
// from a crew they have no relationship to — the same audience rule
// handleListRides already applies per-crew, just aggregated.
func TestUpcomingRidesSpansOwnCrewsOnly(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewA := h.seedApprovedCrew(t, "wilant", "friend")
	crewB := h.seedApprovedCrew(t, "stranger")
	routeA := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewA})
	routeB := h.seedRouteWithTargets(t, "Flat Loop", "stranger", []string{crewB})

	h.mustScheduleRideAt(t, "wilant", crewA, routeA.Slug, "2026-09-05", "09:30")
	h.mustScheduleRide(t, "stranger", crewB, routeB.Slug, "2026-09-06")

	list := h.decodeUpcomingRides(t, h.as("friend", "cyclists", http.MethodGet, "/api/rides/upcoming", ""))
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1 — friend is only in crewA", len(list))
	}
	if list[0].Slug != routeA.Slug || list[0].Time != "09:30" || list[0].CrewName == "" {
		t.Fatalf("list[0] = %+v, want crewA's timed ride with a crewName", list[0])
	}
}

// TestUpcomingRidesOmitsPastRides proves the from-date filter actually
// excludes what's already happened, not just what hasn't.
func TestUpcomingRidesOmitsPastRides(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})
	h.mustScheduleRide(t, "wilant", crewID, route.Slug, "2020-01-01")

	list := h.decodeUpcomingRides(t, h.as("wilant", "cyclists", http.MethodGet, "/api/rides/upcoming", ""))
	if len(list) != 0 {
		t.Fatalf("list = %+v, want no rides — the only one scheduled is long past", list)
	}
}

// TestUpcomingRidesUsesTheCallersOwnFrom proves the caller's own ?from=
// cutoff is actually honored, not just accepted — a rider's own idea of
// "today" (their browser's local day) decides what counts as upcoming, not
// the server's, since the server has no reliable notion of the rider's
// timezone. Without this, a ride the caller considers already past could
// still show as upcoming (or vice versa) purely because the server's clock
// disagrees with the rider's own.
func TestUpcomingRidesUsesTheCallersOwnFrom(t *testing.T) {
	h := newAuthHarness(t, nil)
	crewID := h.seedApprovedCrew(t, "wilant")
	route := h.seedRouteWithTargets(t, "Hill Loop", "wilant", []string{crewID})
	h.mustScheduleRide(t, "wilant", crewID, route.Slug, "2026-09-05")

	// From a point after the ride: it must not show, even though the
	// server's own unqualified default (real today, long before 2026-09-05)
	// would otherwise include it.
	after := h.decodeUpcomingRides(t, h.as("wilant", "cyclists", http.MethodGet, "/api/rides/upcoming?from=2026-09-06", ""))
	if len(after) != 0 {
		t.Fatalf("list = %+v, want no rides — from is after the only ride scheduled", after)
	}

	// From the ride's own date: inclusive, so it must show.
	on := h.decodeUpcomingRides(t, h.as("wilant", "cyclists", http.MethodGet, "/api/rides/upcoming?from=2026-09-05", ""))
	if len(on) != 1 {
		t.Fatalf("list = %+v, want the one ride scheduled exactly on from", on)
	}
}

// TestUpcomingRidesRejectsAMalformedFrom proves a caller-supplied ?from=
// that isn't a real date 400s rather than silently falling back to
// something else or reaching the database with it.
func TestUpcomingRidesRejectsAMalformedFrom(t *testing.T) {
	h := newAuthHarness(t, nil)
	h.seedApprovedCrew(t, "wilant")

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/rides/upcoming?from=not-a-date", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
