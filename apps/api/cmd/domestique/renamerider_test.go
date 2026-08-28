package main

import (
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

// seedRider gives a rider one route, one Garmin account with sync state
// behind it, and one provider sign-in — the whole shape a rename has to
// carry across correctly, in one place so every test below starts from the
// same known state.
func seedRider(t *testing.T, dsn, rider string) {
	t.Helper()

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Create(t.Context(), source.CreateRequest{
		Filename: "ride.gpx", Name: "A Ride", UploadedBy: rider, GPX: []byte(exampleGPX),
	}); err != nil {
		t.Fatal(err)
	}

	acctStore, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acctStore.Link(t.Context(), model.ProviderGarmin, rider, ""); err != nil {
		t.Fatal(err)
	}

	stateStore, err := state.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if err := stateStore.Record(t.Context(), state.Entry{
		AccountID: accounts.ID(model.ProviderGarmin, rider), Slug: "a-ride",
		RemoteID: "remote-1", ContentHash: "hash-1", UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := links.Save(string(model.ProviderGarmin), rider, providerlink.Connection{
		Email: rider + "@example.com", DisplayName: rider, Secret: "token",
	}); err != nil {
		t.Fatal(err)
	}
}

// assertGone confirms nothing under the old rider name survives — a rename
// that copied without deleting would double-count everything on the next run.
func assertGone(t *testing.T, dsn, oldRider string) {
	t.Helper()
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
	for _, a := range list {
		if a.Rider == oldRider {
			t.Errorf("account %s still carries the old rider %q", a.ID, oldRider)
		}
	}
}

func TestRenameRiderMovesEveryTable(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")

	out := mustRun(t, "rename-rider", "wilant", "auth0|64f2a1b2c3d4e5f6")
	for _, want := range []string{"routes:", "1", "accounts:", "sync state rows:", "provider sign-ins:",
		`renamed "wilant" to "auth0|64f2a1b2c3d4e5f6"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// routes.uploaded_by
	routes, _, err := db.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Owner != "auth0|64f2a1b2c3d4e5f6" {
		t.Fatalf("routes = %+v, want the one route reassigned", routes)
	}

	// accounts.id and accounts.rider
	acctStore, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	newID := accounts.ID(model.ProviderGarmin, "auth0|64f2a1b2c3d4e5f6")
	account, err := acctStore.Get(t.Context(), newID)
	if err != nil {
		t.Fatalf("Get(%s): %v", newID, err)
	}
	if account.Rider != "auth0|64f2a1b2c3d4e5f6" {
		t.Errorf("account.Rider = %q", account.Rider)
	}

	// sync_state.account_id, carried by the same key the account now has
	stateStore, err := state.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := stateStore.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].AccountID != newID {
		t.Fatalf("sync state = %+v, want one row keyed to %s", entries, newID)
	}

	// provider_links.rider
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := links.Get(string(model.ProviderGarmin), "auth0|64f2a1b2c3d4e5f6"); err != nil {
		t.Errorf("provider link not found under the new rider: %v", err)
	}

	assertGone(t, dsn, "wilant")
}

// A target that already has an account must abort the whole rename rather
// than silently overwrite it — and abort means nothing at all was written,
// proven by reading the old rider's data back afterward.
func TestRenameRiderAbortsOnConflictAndWritesNothing(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")
	seedRider(t, dsn, "friend") // already owns a garmin:friend account

	_, err := capture(t, "rename-rider", "wilant", "friend")
	if err == nil {
		t.Fatal("a rename onto an existing account succeeded")
	}
	if !strings.Contains(err.Error(), "already has a") {
		t.Errorf("err = %v, want it to name the conflict", err)
	}

	// Nothing moved: wilant's account, and its sync state, are both still
	// exactly where they were.
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	acctStore, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acctStore.Get(t.Context(), accounts.ID(model.ProviderGarmin, "wilant")); err != nil {
		t.Errorf("wilant's account is gone after an aborted rename: %v", err)
	}
	stateStore, err := state.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := stateStore.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var stillWilant bool
	for _, e := range entries {
		if e.AccountID == accounts.ID(model.ProviderGarmin, "wilant") {
			stillWilant = true
		}
	}
	if !stillWilant {
		t.Error("wilant's sync state is gone after an aborted rename")
	}
}

// --replace is the escape hatch for exactly what the plain conflict-abort
// test above proves is otherwise refused: the new rider already has its own
// account, sync state and provider sign-in. The old rider's row wins — the
// real scenario this exists for is consolidating two logins onto one
// identity where the newer login is the one actually still working.
func TestRenameRiderReplaceKeepsTheOldRidersRowOnConflict(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")
	seedRider(t, dsn, "friend")

	out := mustRun(t, "rename-rider", "--replace", "wilant", "friend")
	if !strings.Contains(out, "replaced on conflict") {
		t.Errorf("output does not mention what --replace did:\n%s", out)
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
	friendID := accounts.ID(model.ProviderGarmin, "friend")
	account, err := acctStore.Get(t.Context(), friendID)
	if err != nil {
		t.Fatalf("Get(%s): %v", friendID, err)
	}
	if account.Rider != "friend" {
		t.Errorf("account.Rider = %q", account.Rider)
	}

	// Exactly one sync state row survives, under friend's id — friend's own
	// original row was the conflict --replace deleted; wilant's took its
	// place rather than colliding with it.
	stateStore, err := state.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := stateStore.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].AccountID != friendID {
		t.Fatalf("sync state = %+v, want exactly one row under %s", entries, friendID)
	}

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := links.Get(string(model.ProviderGarmin), "friend")
	if err != nil {
		t.Fatalf("provider link not found under friend: %v", err)
	}
	// seedRider ties a connection's email to the rider it was seeded under —
	// wilant's email surviving under friend's rider is proof wilant's row is
	// the one that took over, not friend's original.
	if conn.Email != "wilant@example.com" {
		t.Errorf("Email = %q, want wilant's connection to have taken over", conn.Email)
	}

	assertGone(t, dsn, "wilant")
}

// The real shape this was found against: an account gets unlinked (which
// only ever deletes the accounts row — accounts.Store.Unlink never touches
// sync_state) rather than renamed, orphaning its sync_state rows. Nothing
// references them, so the plain accounts-conflict check above sees no
// conflict at all — but they still sit at the exact (account_id, slug) pair
// the old rider's own rows are about to move into.
func TestRenameRiderReplaceClearsOrphanedSyncStateWithNoAccount(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")
	seedRider(t, dsn, "friend")

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	acctStore, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	// Unlink friend's account — its sync_state (slug "a-ride", same slug
	// seedRider always uses) is left behind, orphaned, exactly as it was in
	// production after a UI unlink.
	if err := acctStore.Unlink(t.Context(), accounts.ID(model.ProviderGarmin, "friend")); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Without --replace, this must still abort rather than let Postgres/SQLite
	// surface a raw primary-key violation later.
	if _, err := capture(t, "rename-rider", "wilant", "friend"); err == nil {
		t.Fatal("a rename onto a colliding orphaned sync_state row succeeded without --replace")
	} else if !strings.Contains(err.Error(), "orphaned sync state") {
		t.Errorf("err = %v, want it to name the orphaned sync state conflict", err)
	}

	out := mustRun(t, "rename-rider", "--replace", "wilant", "friend")
	if !strings.Contains(out, "replaced on conflict") {
		t.Errorf("output does not mention what --replace did:\n%s", out)
	}

	db, err = source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stateStore, err := state.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := stateStore.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	friendID := accounts.ID(model.ProviderGarmin, "friend")
	if len(entries) != 1 || entries[0].AccountID != friendID || entries[0].RemoteID != "remote-1" {
		t.Fatalf("sync state = %+v, want exactly wilant's one row now under %s", entries, friendID)
	}
}

// --dry-run and --replace together must report what would be replaced
// without touching anything — the same promise plain --dry-run makes,
// extended to the new counts.
func TestRenameRiderReplaceDryRunWritesNothing(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")
	seedRider(t, dsn, "friend")

	out := mustRun(t, "rename-rider", "--dry-run", "--replace", "wilant", "friend")
	if !strings.Contains(out, "dry run") || !strings.Contains(out, "replaced on conflict") {
		t.Errorf("output missing dry-run or replace summary:\n%s", out)
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
	if account, err := acctStore.Get(t.Context(), accounts.ID(model.ProviderGarmin, "friend")); err != nil {
		t.Errorf("dry run deleted friend's account: %v", err)
	} else if account.Rider != "friend" {
		t.Errorf("account.Rider = %q, want untouched", account.Rider)
	}
	if _, err := acctStore.Get(t.Context(), accounts.ID(model.ProviderGarmin, "wilant")); err != nil {
		t.Errorf("dry run moved wilant's account: %v", err)
	}

	stateStore, err := state.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := stateStore.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("sync state has %d rows after a dry run, want both riders' untouched", len(entries))
	}
}

// Without --replace, a conflict on provider_links specifically still aborts
// with nothing written — the accounts-conflict case is already covered by
// TestRenameRiderAbortsOnConflictAndWritesNothing; this is the same
// guarantee for the other conflict --replace can resolve.
func TestRenameRiderAbortsOnProviderLinkConflictWithoutReplace(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")
	seedRider(t, dsn, "friend")

	_, err := capture(t, "rename-rider", "wilant", "friend")
	if err == nil {
		t.Fatal("a rename onto an existing provider sign-in succeeded")
	}

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := links.Get(string(model.ProviderGarmin), "wilant"); err != nil {
		t.Errorf("wilant's provider sign-in is gone after an aborted rename: %v", err)
	}
	if conn, err := links.Get(string(model.ProviderGarmin), "friend"); err != nil {
		t.Errorf("friend's provider sign-in is gone after an aborted rename: %v", err)
	} else if conn.Email != "friend@example.com" {
		t.Errorf("friend's provider sign-in was overwritten without --replace: %q", conn.Email)
	}
}

func TestRenameRiderDryRunWritesNothing(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")

	out := mustRun(t, "rename-rider", "--dry-run", "wilant", "auth0|abc")
	if !strings.Contains(out, "dry run") {
		t.Errorf("output does not say dry run:\n%s", out)
	}
	// The counts should still be accurate, even though nothing was written.
	for _, want := range []string{"routes:", "accounts:", "sync state rows:", "provider sign-ins:"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output missing %q:\n%s", want, out)
		}
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
	if _, err := acctStore.Get(t.Context(), accounts.ID(model.ProviderGarmin, "wilant")); err != nil {
		t.Errorf("dry run moved the account: %v", err)
	}
	if _, err := acctStore.Get(t.Context(), accounts.ID(model.ProviderGarmin, "auth0|abc")); err == nil {
		t.Error("dry run created the new account")
	}
}

// A second run after a successful rename finds nothing left under the old
// name and does nothing — the retry-safety a partial-failure recovery
// depends on.
func TestRenameRiderIsIdempotent(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")

	mustRun(t, "rename-rider", "wilant", "auth0|abc")
	out := mustRun(t, "rename-rider", "wilant", "auth0|abc")

	for _, want := range []string{"routes:              0", "accounts:            0",
		"sync state rows:     0", "provider sign-ins:   0"} {
		if !strings.Contains(out, want) {
			t.Errorf("second run output missing %q:\n%s", want, out)
		}
	}
}

func TestRenameRiderRejectsBadInput(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no arguments", []string{"rename-rider"}},
		{"one argument", []string{"rename-rider", "wilant"}},
		{"same rider twice", []string{"rename-rider", "wilant", "wilant"}},
		{"new rider has an illegal character", []string{"rename-rider", "wilant", "has space"}},
		{"empty new rider", []string{"rename-rider", "wilant", "  "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := capture(t, tc.args...); err == nil {
				t.Errorf("%v was accepted", tc.args)
			}
		})
	}
}
