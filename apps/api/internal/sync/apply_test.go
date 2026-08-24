package sync

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/targets"
)

// fakeTarget records what it was asked to do, and can be told to fail.
type fakeTarget struct {
	creates, updates, deletes []string
	err                       error
}

func (f *fakeTarget) Create(_ context.Context, route model.Route) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.creates = append(f.creates, route.Slug)
	return "remote-" + route.Slug, nil
}

func (f *fakeTarget) Update(_ context.Context, remoteID string, route model.Route) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.updates = append(f.updates, route.Slug)
	return remoteID, nil
}

func (f *fakeTarget) Delete(_ context.Context, remoteID string) error {
	if f.err != nil {
		return f.err
	}
	f.deletes = append(f.deletes, remoteID)
	return nil
}

// forAccount keeps the assertions readable now that the read can fail.
func forAccount(t *testing.T, store state.Store, accountID string) map[string]state.Entry {
	t.Helper()
	entries, err := store.ForAccount(t.Context(), accountID)
	if err != nil {
		t.Fatalf("ForAccount(%s): %v", accountID, err)
	}
	return entries
}

// mustPlan is the same idea for BuildPlan. crewSharing is always true here —
// sync_test.go's own fixtures are all a route shared to a crew, specifically
// so BuildPlan's diff engine (create/update/delete detection, exercised by
// every test in that file) gets run across more than one account without
// each test inventing its own multi-owner fixture. That crew-aware
// resolution is exactly what backs the crew ride scheduler's own "sync now"
// action now — see BuildPlan's own doc comment for the other, narrower mode
// every general-purpose push uses instead, covered separately by
// TestBuildPlanGeneralPushNeverReachesACrewFellow.
func mustPlan(t *testing.T, routes []model.Route, linked []model.Account, store state.Store, crews crew.Snapshot) model.Plan {
	t.Helper()
	plan, err := BuildPlan(t.Context(), routes, linked, store, crews, true)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	return plan
}

func route(slug, hash string) model.Route {
	return model.Route{
		RouteMeta:   model.RouteMeta{Name: slug},
		Slug:        slug,
		ContentHash: hash,
	}
}

func TestApplyCreatesAndRecordsState(t *testing.T) {
	store, target := newStore(t), &fakeTarget{}
	plan := model.Plan{Items: []model.PlanItem{{
		Op: model.OpCreate, AccountID: "garmin:one", Slug: "loop",
		Route: &model.Route{RouteMeta: model.RouteMeta{Name: "Loop"}, Slug: "loop", ContentHash: "v1"},
	}}}

	if failures := Apply(t.Context(), plan, store, map[string]targets.Target{"garmin:one": target}, nil); len(failures) != 0 {
		t.Fatalf("failures: %v", failures)
	}

	if len(target.creates) != 1 {
		t.Errorf("adapter saw %v, want one create", target.creates)
	}

	entry, ok := forAccount(t, store, "garmin:one")["loop"]
	if !ok {
		t.Fatal("nothing recorded; the next run would create it again")
	}
	if entry.RemoteID != "remote-loop" || entry.ContentHash != "v1" {
		t.Errorf("recorded %+v", entry)
	}
}

func TestApplyUpdateKeepsRemoteID(t *testing.T) {
	store, target := newStore(t), &fakeTarget{}
	plan := model.Plan{Items: []model.PlanItem{{
		Op: model.OpUpdate, AccountID: "garmin:one", Slug: "loop",
		RemoteID: "remote-abc",
		Route:    &model.Route{RouteMeta: model.RouteMeta{Name: "Loop"}, Slug: "loop", ContentHash: "v2"},
	}}}

	if failures := Apply(t.Context(), plan, store, map[string]targets.Target{"garmin:one": target}, nil); len(failures) != 0 {
		t.Fatalf("failures: %v", failures)
	}

	entry := forAccount(t, store, "garmin:one")["loop"]
	if entry.RemoteID != "remote-abc" {
		t.Errorf("remote id = %q, want it preserved across an update", entry.RemoteID)
	}
	if entry.ContentHash != "v2" {
		t.Errorf("hash = %q, want the new one", entry.ContentHash)
	}
}

func TestApplyDeleteForgetsState(t *testing.T) {
	store, target := newStore(t), &fakeTarget{}
	if err := store.Record(t.Context(), state.Entry{
		AccountID: "garmin:one", Slug: "loop", RemoteID: "remote-abc", ContentHash: "v1",
	}); err != nil {
		t.Fatal(err)
	}

	plan := model.Plan{Items: []model.PlanItem{{
		Op: model.OpDelete, AccountID: "garmin:one", Slug: "loop", RemoteID: "remote-abc",
	}}}

	if failures := Apply(t.Context(), plan, store, map[string]targets.Target{"garmin:one": target}, nil); len(failures) != 0 {
		t.Fatalf("failures: %v", failures)
	}
	if len(target.deletes) != 1 {
		t.Errorf("adapter saw %v, want one delete", target.deletes)
	}
	if len(forAccount(t, store, "garmin:one")) != 0 {
		t.Error("state kept after a delete; the route would be deleted again forever")
	}
}

// One failing provider must not stop the others, and must not record success.
func TestApplyIsolatesFailures(t *testing.T) {
	store := newStore(t)
	healthy := &fakeTarget{}
	broken := &fakeTarget{err: errors.New("provider exploded")}

	plan := model.Plan{Items: []model.PlanItem{
		{Op: model.OpCreate, AccountID: "garmin:one", Slug: "loop", Route: ptr(route("loop", "v1"))},
		{Op: model.OpCreate, AccountID: "wahoo:two", Slug: "loop", Route: ptr(route("loop", "v1"))},
	}}

	failures := Apply(t.Context(), plan, store, map[string]targets.Target{
		"garmin:one": healthy,
		"wahoo:two":  broken,
	}, nil)

	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly one", failures)
	}
	if !strings.Contains(failures[0].Error(), "wahoo:two") {
		t.Errorf("failure does not name the account: %v", failures[0])
	}
	if len(healthy.creates) != 1 {
		t.Error("the healthy account was skipped because the other failed")
	}
	if len(forAccount(t, store, "wahoo:two")) != 0 {
		t.Error("failed push was recorded as success; it would never be retried")
	}
}

func TestApplyReportsMissingAdapter(t *testing.T) {
	store := newStore(t)
	plan := model.Plan{Items: []model.PlanItem{{
		Op: model.OpCreate, AccountID: "garmin:ghost", Slug: "loop", Route: ptr(route("loop", "v1")),
	}}}

	failures := Apply(t.Context(), plan, store, map[string]targets.Target{}, nil)
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "garmin:ghost") {
		t.Errorf("failures = %v, want one naming the account", failures)
	}
}

// onResult is the seam handlePush's own push metrics hook into — prove it
// actually fires, with the right item and the right error (nil on success),
// rather than only proving that passing nil elsewhere does not break.
func TestApplyCallsOnResultForEveryChange(t *testing.T) {
	store := newStore(t)
	healthy := &fakeTarget{}
	broken := &fakeTarget{err: errors.New("provider exploded")}

	plan := model.Plan{Items: []model.PlanItem{
		{Op: model.OpCreate, AccountID: "garmin:one", Slug: "loop", Route: ptr(route("loop", "v1"))},
		{Op: model.OpCreate, AccountID: "wahoo:two", Slug: "loop", Route: ptr(route("loop", "v1"))},
		{Op: model.OpNoop, AccountID: "garmin:one", Slug: "unrelated", Route: ptr(route("unrelated", "v1"))},
	}}

	type call struct {
		accountID string
		op        model.Op
		failed    bool
	}
	var calls []call
	onResult := func(item model.PlanItem, err error) {
		calls = append(calls, call{item.AccountID, item.Op, err != nil})
	}

	Apply(t.Context(), plan, store, map[string]targets.Target{
		"garmin:one": healthy,
		"wahoo:two":  broken,
	}, onResult)

	// Noop is excluded from plan.Changes() itself (BuildPlan's own job, not
	// Apply's) — plan.Changes() here already only carries the two creates,
	// so exactly two calls, not three.
	if len(calls) != 2 {
		t.Fatalf("onResult calls = %+v, want exactly 2 (the noop item is not a change)", calls)
	}
	if calls[0] != (call{"garmin:one", model.OpCreate, false}) {
		t.Errorf("first call = %+v, want the healthy account reported as a success", calls[0])
	}
	if calls[1] != (call{"wahoo:two", model.OpCreate, true}) {
		t.Errorf("second call = %+v, want the broken account reported as a failure", calls[1])
	}
}

func TestApplySkipsNoops(t *testing.T) {
	store, target := newStore(t), &fakeTarget{}
	plan := model.Plan{Items: []model.PlanItem{{
		Op: model.OpNoop, AccountID: "garmin:one", Slug: "loop", Route: ptr(route("loop", "v1")),
	}}}

	if failures := Apply(t.Context(), plan, store, map[string]targets.Target{"garmin:one": target}, nil); len(failures) != 0 {
		t.Fatalf("failures: %v", failures)
	}
	if len(target.creates)+len(target.updates)+len(target.deletes) != 0 {
		t.Error("an up-to-date route was pushed anyway")
	}
}

func ptr(r model.Route) *model.Route { return &r }
