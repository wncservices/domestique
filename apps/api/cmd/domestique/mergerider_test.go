package main

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"

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

func tableExists(t *testing.T, dsn, table string) bool {
	t.Helper()
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// A dry run must not create anything: the provider sign-ins table is made by the
// server, and the rename path creating it is the very thing a dry run of this
// command must not copy.
func TestMergeRiderDryRunCreatesNoTable(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRoute(t, dsn, "wilant.nackaerts")
	if tableExists(t, dsn, "provider_links") {
		t.Skip("another command made provider_links already; nothing to prove")
	}
	mustRun(t, "merge-rider", "--dry-run", "wilant.nackaerts", "wilant")
	if tableExists(t, dsn, "provider_links") {
		t.Error("a dry run created the provider_links table")
	}
}

func seedRoute(t *testing.T, dsn, rider string) {
	t.Helper()
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Create(t.Context(), source.CreateRequest{Filename: "ride.gpx", Name: "A Ride", UploadedBy: rider, GPX: []byte(exampleGPX)}); err != nil {
		t.Fatal(err)
	}
}

// What a merge cannot sort out is printed where the operator reads it: the two
// plans that will overlap, and by name what is dropped and what that costs.
func TestMergeRiderPrintsWarningsAndWhatItDrops(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant.nackaerts")
	seedRider(t, dsn, "wilant")

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workout.UseDB(db.Conn(), db.DSN()); err != nil {
		t.Fatal(err)
	}
	for _, rider := range []string{"wilant.nackaerts", "wilant"} {
		if _, err := db.Conn().Exec(`INSERT INTO goals (id, rider, name, created_at, updated_at) VALUES (?, ?, 'Goal', 'x', 'x')`, "g-"+rider, rider); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Conn().Exec(`INSERT INTO workouts (id, rider, name, date, steps, created_at, updated_at) VALUES (?, ?, 'W', '2099-01-01', '[]', 'x', 'x')`, "w-"+rider, rider); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	out := mustRun(t, "merge-rider", "--dry-run", "wilant.nackaerts", "wilant")
	for _, want := range []string{"warning:", "two plans will overlap on 1 day", "will be dropped", "provider_links", "garmin", "reconnect"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}

	// The real run says the same first, then merges.
	out = mustRun(t, "merge-rider", "wilant.nackaerts", "wilant")
	if i, j := strings.Index(out, "will be dropped"), strings.Index(out, "merged \"wilant.nackaerts\""); i < 0 || j < 0 || i > j {
		t.Errorf("the real run must list what it drops before it merges:\n%s", out)
	}
}
