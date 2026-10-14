package main

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

func ownerOfRoutes(t *testing.T, dsn string) map[string]int {
	t.Helper()
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	routes, _, err := db.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, r := range routes {
		out[r.Owner]++
	}
	return out
}

// The dry run says, per table, what moves and what collides, and writes
// nothing; the real run then does exactly that.
func TestMergeRiderDryRunReportsAndWritesNothing(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant.nackaerts")
	seedRider(t, dsn, "wilant") // both have a Garmin account, so that one collides

	out := mustRun(t, "merge-rider", "--dry-run", "wilant.nackaerts", "wilant")
	for _, want := range []string{"moved", "kept-target", "dropped-source", "routes", "accounts", "provider_links", "dry run — nothing written"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
	if got := ownerOfRoutes(t, dsn); got["wilant.nackaerts"] != 1 || got["wilant"] != 1 {
		t.Fatalf("a dry run moved routes: %v", got)
	}

	out = mustRun(t, "merge-rider", "wilant.nackaerts", "wilant")
	if !strings.Contains(out, `merged "wilant.nackaerts" into "wilant"`) {
		t.Errorf("output does not say it merged:\n%s", out)
	}
	if got := ownerOfRoutes(t, dsn); got["wilant.nackaerts"] != 0 || got["wilant"] != 2 {
		t.Errorf("routes after the merge: %v, want both under wilant", got)
	}

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	acctStore, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	list, err := acctStore.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != accounts.ID(model.ProviderGarmin, "wilant") {
		t.Errorf("accounts = %+v, want only the target's own Garmin account", list)
	}
}

func TestMergeRiderNeedsTwoDifferentRiders(t *testing.T) {
	workspace(t)
	for _, args := range [][]string{
		{"merge-rider"},
		{"merge-rider", "only-one"},
		{"merge-rider", "a", "A"},
	} {
		if _, err := capture(t, args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
}
