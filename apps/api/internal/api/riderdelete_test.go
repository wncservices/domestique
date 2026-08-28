package api

import (
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

var minimalGPX = []byte(`<gpx version="1.1"><trk><trkseg><trkpt lat="50" lon="3"/><trkpt lat="50.001" lon="3.001"/></trkseg></trk></gpx>`)

// purgeHarness wires every store purgeRiderData touches against one shared
// database, so a test can seed data through the real stores and then assert
// what purgeRiderData actually removed.
type purgeHarness struct {
	srv    *Server
	src    *source.DB
	links  *providerlink.Store
	crew   *crew.Store
	sched  *schedule.Store
	accts  *accounts.Store
	states state.Store
}

func newPurgeHarness(t *testing.T) *purgeHarness {
	t.Helper()

	src, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })

	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	acctStore, err := accounts.UseDB(src.Conn(), src.DSN())
	if err != nil {
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
	linkStore, err := providerlink.UseDB(src.Conn(), src.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}

	crewStore, err := crew.UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	schedStore, err := schedule.UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		Source:   src,
		Store:    st,
		Accounts: acctStore,
		Links:    linkStore,
		Crew:     crewStore,
		Schedule: schedStore,
	}

	return &purgeHarness{srv: srv, src: src, links: linkStore, crew: crewStore, sched: schedStore, accts: acctStore, states: st}
}

func TestPurgeRiderDataRemovesRoutesAccountsProviderLinksAndCrewMembership(t *testing.T) {
	h := newPurgeHarness(t)
	ctx := t.Context()

	if _, err := h.src.Create(ctx, source.CreateRequest{
		Name: "Departed Rider's Loop", GPX: minimalGPX, UploadedBy: "gone",
	}); err != nil {
		t.Fatalf("create route: %v", err)
	}
	if _, err := h.src.Create(ctx, source.CreateRequest{
		Name: "Someone Else's Loop", GPX: minimalGPX, UploadedBy: "stays",
	}); err != nil {
		t.Fatalf("create route: %v", err)
	}

	acct, err := h.accts.Link(ctx, model.ProviderGarmin, "gone", "")
	if err != nil {
		t.Fatalf("link account: %v", err)
	}
	if err := h.states.Record(ctx, state.Entry{AccountID: acct.ID, Slug: "departed-riders-loop", RemoteID: "remote-1", ContentHash: "abc"}); err != nil {
		t.Fatalf("record sync state: %v", err)
	}

	if _, err := h.links.Save("garmin", "gone", providerlink.Connection{Secret: "a-session-token"}); err != nil {
		t.Fatalf("save provider link: %v", err)
	}

	c, err := h.crew.Create(ctx, "Sunday Club", "gone")
	if err != nil {
		t.Fatalf("create crew: %v", err)
	}
	if _, err := h.crew.AddMember(ctx, c.ID, "stays", "gone"); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if err := h.crew.Confirm(ctx, c.ID, "stays"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	sum, err := h.srv.purgeRiderData(ctx, "gone")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if sum.Routes != 1 {
		t.Errorf("Routes = %d, want 1", sum.Routes)
	}
	if sum.AccountsUnlinked != 1 {
		t.Errorf("AccountsUnlinked = %d, want 1", sum.AccountsUnlinked)
	}
	if sum.SyncStateRows != 1 {
		t.Errorf("SyncStateRows = %d, want 1", sum.SyncStateRows)
	}
	if sum.ProviderLinks != 3 {
		t.Errorf("ProviderLinks = %d, want 3 (garmin, wahoo, komoot all attempted)", sum.ProviderLinks)
	}
	// gone was both the crew's creator (an owner grant) and, separately,
	// nothing else — one crew_members row.
	if sum.CrewMemberships != 1 {
		t.Errorf("CrewMemberships = %d, want 1", sum.CrewMemberships)
	}

	routes, _, err := h.src.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Owner != "stays" {
		t.Fatalf("routes = %+v, want only the other rider's route left", routes)
	}

	if _, err := h.accts.Get(ctx, acct.ID); err == nil {
		t.Error("account still linked after purge")
	}
	entries, err := h.states.ForAccount(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("sync state = %v, want none left", entries)
	}
	if _, err := h.links.Get("garmin", "gone"); err == nil {
		t.Error("provider link still present after purge")
	}

	snap, err := h.crew.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ApprovedRiders.Has(c.ID, "gone") {
		t.Error("deleted rider is still a crew member")
	}
	if !snap.ApprovedRiders.Has(c.ID, "stays") {
		t.Error("the crew's other member should be untouched")
	}
}

func TestPurgeRiderDataOrphansSharedRideAuthorshipWithoutDeletingTheRide(t *testing.T) {
	h := newPurgeHarness(t)
	ctx := t.Context()

	c, err := h.crew.Create(ctx, "Sunday Club", "stays")
	if err != nil {
		t.Fatalf("create crew: %v", err)
	}
	ride, err := h.sched.Create(ctx, c.ID, "hill-loop", "2026-09-05", "", "gone")
	if err != nil {
		t.Fatalf("schedule ride: %v", err)
	}

	sum, err := h.srv.purgeRiderData(ctx, "gone")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if sum.RidesOrphaned != 1 {
		t.Errorf("RidesOrphaned = %d, want 1", sum.RidesOrphaned)
	}

	got, err := h.sched.Get(ctx, ride.ID)
	if err != nil {
		t.Fatalf("ride was deleted, want it kept: %v", err)
	}
	if got.CreatedBy != "" {
		t.Errorf("CreatedBy = %q, want blanked", got.CreatedBy)
	}
}

// A second purge for the same rider, after the first already removed
// everything, must succeed with nothing left to do — every step purge
// composes tolerates "already gone", which is what makes a retry after a
// partial failure safe.
func TestPurgeRiderDataIsRetrySafeOnAnAlreadyPurgedRider(t *testing.T) {
	h := newPurgeHarness(t)
	ctx := t.Context()

	if _, err := h.src.Create(ctx, source.CreateRequest{
		Name: "Departed Rider's Loop", GPX: minimalGPX, UploadedBy: "gone",
	}); err != nil {
		t.Fatalf("create route: %v", err)
	}
	if _, err := h.accts.Link(ctx, model.ProviderGarmin, "gone", ""); err != nil {
		t.Fatalf("link account: %v", err)
	}

	if _, err := h.srv.purgeRiderData(ctx, "gone"); err != nil {
		t.Fatalf("first purge: %v", err)
	}

	sum, err := h.srv.purgeRiderData(ctx, "gone")
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if sum.Routes != 0 || sum.AccountsUnlinked != 0 {
		t.Errorf("second purge = %+v, want nothing left to remove", sum)
	}
}

// A Server with only Source wired (the other stores nil, the same shape a
// directory-backed deployment's Server has for everything except Source)
// must not panic — every step is individually nil-guarded.
func TestPurgeRiderDataToleratesUnwiredStores(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })

	srv := &Server{Source: src}
	if _, err := srv.purgeRiderData(t.Context(), "anyone"); err != nil {
		t.Fatalf("purge with only Source wired: %v", err)
	}
}

func TestPurgeRiderDataRejectsAnEmptyRider(t *testing.T) {
	srv := &Server{}
	if _, err := srv.purgeRiderData(t.Context(), "   "); err == nil {
		t.Error("purge with a blank rider should be an error, not a silent no-op")
	}
}
