package sync

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

// testAccounts stands in for what riders linked through the UI.
func testAccounts() []model.Account {
	return []model.Account{
		{ID: "garmin:one", Provider: model.ProviderGarmin, Rider: "one"},
		{ID: "wahoo:two", Provider: model.ProviderWahoo, Rider: "two"},
	}
}

// testCrews is the shared-library scenario testRoutes' route is shared
// into: rider "one" owns it and shares it to a crew rider "two" also
// belongs to — the only way, since crews landed, for one route to reach
// more than its owner's own accounts.
func testCrews() crew.Snapshot {
	return crew.Snapshot{
		Crews:          []crew.Crew{{ID: "crew:test", Owner: "one"}},
		ApprovedRiders: crew.MemberSet{"crew:test": {"one", "two"}},
	}
}

// testRoutes is a route as the library hands one over, owned by "one" and
// shared to testCrews' crew — the diff engine only reads the slug, the
// targets, the owner and the content hash, so there is no need to go near
// a real library here.
func testRoutes(t *testing.T) []model.Route {
	t.Helper()
	shared := []string{"crew:test"}
	return []model.Route{{
		RouteMeta:   model.RouteMeta{Name: "Kemmelberg Loop", Targets: &shared},
		Slug:        "kemmelberg-loop",
		Owner:       "one",
		ContentHash: "hash-v1",
	}}
}

func newStore(t *testing.T) state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestBuildPlanCreatesUnsyncedRoutes(t *testing.T) {
	routes, linked := testRoutes(t), testAccounts()
	plan := mustPlan(t, routes, linked, newStore(t), testCrews())

	changes := plan.Changes()
	if want := len(routes) * len(linked); len(changes) != want {
		t.Fatalf("got %d changes, want %d (one per route/account pair)", len(changes), want)
	}
	for _, item := range changes {
		if item.Op != model.OpCreate {
			t.Errorf("%s: op = %s, want create on an empty state", item.Slug, item.Op)
		}
	}
}

func TestBuildPlanIsIdempotent(t *testing.T) {
	routes, linked, store := testRoutes(t), testAccounts(), newStore(t)

	for _, item := range mustPlan(t, routes, linked, store, testCrews()).Changes() {
		if err := store.Record(t.Context(), state.Entry{
			AccountID:   item.AccountID,
			Slug:        item.Slug,
			RemoteID:    "remote-" + item.Slug,
			ContentHash: item.Route.ContentHash,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if changes := mustPlan(t, routes, linked, store, testCrews()).Changes(); len(changes) != 0 {
		t.Fatalf("re-plan after a full push produced %d changes, want 0", len(changes))
	}
}

func TestBuildPlanDeletesRoutesDroppedFromLibrary(t *testing.T) {
	routes, linked, store := testRoutes(t), testAccounts(), newStore(t)

	if err := store.Record(t.Context(), state.Entry{
		AccountID:   linked[0].ID,
		Slug:        "gone-from-repo",
		RemoteID:    "remote-123",
		ContentHash: "stale",
	}); err != nil {
		t.Fatal(err)
	}

	var deletes int
	for _, item := range mustPlan(t, routes, linked, store, testCrews()).Changes() {
		if item.Op == model.OpDelete {
			deletes++
			if item.RemoteID != "remote-123" {
				t.Errorf("delete carried remote id %q, want remote-123", item.RemoteID)
			}
		}
	}
	if deletes != 1 {
		t.Fatalf("got %d deletes, want 1", deletes)
	}
}

func TestBuildPlanUpdatesChangedRoutes(t *testing.T) {
	routes, linked, store := testRoutes(t), testAccounts(), newStore(t)

	route := routes[0]
	account := linked[0].ID
	if err := store.Record(t.Context(), state.Entry{
		AccountID:   account,
		Slug:        route.Slug,
		RemoteID:    "remote-abc",
		ContentHash: "hash-from-an-older-version",
	}); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, item := range mustPlan(t, routes, linked, store, testCrews()).Changes() {
		if item.Slug == route.Slug && item.AccountID == account {
			found = true
			if item.Op != model.OpUpdate {
				t.Errorf("op = %s, want update", item.Op)
			}
			if item.RemoteID != "remote-abc" {
				t.Errorf("update lost the remote id: %q", item.RemoteID)
			}
		}
	}
	if !found {
		t.Fatal("changed route produced no plan item")
	}
}

// A route shared to a crew must not be pushed to an account outside that
// crew's current membership — this is what keeps one rider's private
// routes off a rider who isn't in the crew, integration-tested through
// BuildPlan itself rather than only at TargetsFor's own level.
func TestBuildPlanHonoursCrewMembership(t *testing.T) {
	routes, linked, store := testRoutes(t), testAccounts(), newStore(t)

	// Narrower than testCrews(): "two" is not a member here.
	ownerOnly := crew.Snapshot{
		Crews:          []crew.Crew{{ID: "crew:test", Owner: "one"}},
		ApprovedRiders: crew.MemberSet{"crew:test": {"one"}},
	}

	for _, item := range mustPlan(t, routes, linked, store, ownerOnly).Changes() {
		if item.Slug == routes[0].Slug && item.AccountID != "garmin:one" {
			t.Errorf("targeted route planned for %s as well", item.AccountID)
		}
	}
}

// A general-purpose push (crewSharing: false — see BuildPlan's own doc
// comment) must never reach a crew fellow's account, even for a route
// explicitly shared to a crew both riders currently, approvedly, belong
// to. This is the actual security boundary the crewSharing parameter
// exists to draw: only the crew ride scheduler's own deliberate "sync
// now" action (BuildPlan called with crewSharing: true, exercised by
// every other test in this file via mustPlan) may cross from one rider's
// own accounts into another's.
func TestBuildPlanGeneralPushNeverReachesACrewFellow(t *testing.T) {
	routes, linked, store := testRoutes(t), testAccounts(), newStore(t)

	plan, err := BuildPlan(t.Context(), routes, linked, store, testCrews(), false)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	for _, item := range plan.Changes() {
		if item.AccountID != "garmin:one" {
			t.Errorf("general push planned %s for %s, want only the route's own owner (garmin:one)",
				item.Slug, item.AccountID)
		}
	}
}

// A route a crew push put on a fellow's device is not the general push's to
// send, but not its to take away either: while it is still in the library and
// shared with a crew that rider is in, a general plan leaves it alone. Only a
// route that left the library, or stopped being shared with them, is deleted.
func TestBuildPlanGeneralPushKeepsACrewSharedRouteOnAFellowsDevice(t *testing.T) {
	routes, linked, store := testRoutes(t), testAccounts(), newStore(t)
	for _, slug := range []string{"kemmelberg-loop", "gone-from-repo"} {
		if err := store.Record(t.Context(), state.Entry{
			AccountID: "wahoo:two", Slug: slug, RemoteID: "remote-" + slug, ContentHash: "hash-v1",
		}); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := BuildPlan(t.Context(), routes, linked, store, testCrews(), false)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	deleted := map[string]bool{}
	for _, item := range plan.Changes() {
		if item.Op == model.OpDelete && item.AccountID == "wahoo:two" {
			deleted[item.Slug] = true
		}
	}
	if deleted["kemmelberg-loop"] {
		t.Error("a general push deleted the crew-shared route from a fellow's device")
	}
	if !deleted["gone-from-repo"] {
		t.Error("a route that left the library was not deleted from the fellow's device")
	}

	// Once the fellow is no longer in the crew, the route goes.
	left := crew.Snapshot{Crews: testCrews().Crews, ApprovedRiders: crew.MemberSet{"crew:test": {"one"}}}
	plan, err = BuildPlan(t.Context(), routes, linked, store, left, false)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var gone bool
	for _, item := range plan.Changes() {
		if item.Op == model.OpDelete && item.AccountID == "wahoo:two" && item.Slug == "kemmelberg-loop" {
			gone = true
		}
	}
	if !gone {
		t.Error("the route stayed on the device of a rider who left the crew")
	}
}
