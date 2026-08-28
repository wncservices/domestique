package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/komoot"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/targets"
)

// A disabled deployment must make zero third-party calls, not merely skip
// acting on what it found — confirmed by checking nothing at all reached
// the library, with a Garmin course sitting there ready to be picked up.
func TestAutoImportTickDoesNothingWhenDisabled(t *testing.T) {
	h := newConnectHarness(t, true)
	h.connectGarmin("wilant")
	h.garmin.listCourses = []garmin.Course{
		{ID: "1", Name: "Should not import", DistanceM: 1000, AscentM: 10, ActivityType: "cycling"},
	}
	// auto-sync's flag defaults off — never explicitly enabled in this test.

	h.srv.AutoImportTick(context.Background())

	routes, _, err := h.db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes = %+v, want none — auto-sync is off", routes)
	}
}

// The heart of the feature: a new Garmin course gets pulled in on its own,
// a course that looks like a duplicate of something already in the library
// is left for a rider to look at instead, and a second tick does not
// import the same course twice — the same tracked-state check the manual
// Import button already relies on.
func TestAutoImportTickImportsNewGarminCoursesAndSkipsLikelyDuplicates(t *testing.T) {
	h := newConnectHarness(t, true)
	h.connectGarmin("wilant")
	if err := h.settings.SetFlag(api.FlagAutoSync, true, "test"); err != nil {
		t.Fatal(err)
	}

	existing, err := h.db.Create(context.Background(), source.CreateRequest{
		Filename: "kemmelberg-loop.gpx", Name: "Kemmelberg Loop", GPX: exampleGPX(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	h.garmin.listCourses = []garmin.Course{
		// Close enough to the existing route to count as a likely duplicate
		// (distance within 2%, same start point) — an unattended poller
		// must not silently create a second copy of this.
		{
			ID: "dup", Name: "Kemmelberg (from my Edge)",
			DistanceM: existing.Stats.DistanceM * 1.005,
			StartLat:  existing.Stats.StartLat, StartLng: existing.Stats.StartLng,
			ActivityType: "cycling",
		},
		// Genuinely new.
		{ID: "new", Name: "Fresh Loop", DistanceM: 30000, AscentM: 200, ActivityType: "cycling"},
	}
	h.garmin.gpxByID = map[string][]byte{"new": exampleGPX(t)}

	h.srv.AutoImportTick(context.Background())

	routes, _, err := h.db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %+v, want the pre-existing one plus exactly one import", routes)
	}
	var gotNew bool
	for _, r := range routes {
		if r.Name == "Fresh Loop" {
			gotNew = true
		}
		if r.Name == "Kemmelberg (from my Edge)" {
			t.Error("the likely-duplicate course was imported anyway")
		}
	}
	if !gotNew {
		t.Error("the genuinely new course was not imported")
	}

	// A second tick must not import "new" again — it is now tracked.
	h.srv.AutoImportTick(context.Background())
	routes, _, err = h.db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes after a second tick = %+v, want no change", routes)
	}
}

// The reconcile half of the same tick: a route already in the library, with
// nothing new for the poller to import, still gets pushed to a linked,
// auto-push-enabled account it never reached — the only retry a
// transiently-failed background push, or a device that was offline, ever
// gets. Without the reconcile-every-tick change this stays red forever: the
// old code only pushed when imported > 0.
func TestAutoImportTickReconcilesPushEvenWithNothingNewToImport(t *testing.T) {
	ledger := &fakeLedger{}
	h := newConnectHarness(t, true, func(s *api.Server) {
		s.TargetFactory = func(account model.Account) (targets.Target, error) {
			return &fakeTarget{account: account, ledger: ledger}, nil
		}
	})
	if err := h.settings.SetFlag(api.FlagAutoSync, true, "test"); err != nil {
		t.Fatal(err)
	}

	// seedRoleAccounts already links a Garmin account for "wilant", auto-push
	// on by default — this route was never pushed to it, and nothing below
	// makes it look importable, so autoImportGarmin finds nothing new.
	if _, err := h.db.Create(context.Background(), source.CreateRequest{
		Filename: "loop.gpx", Name: "Loop", GPX: exampleGPX(t), UploadedBy: "wilant",
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoImportTick(context.Background())

	if len(ledger.creates) != 1 {
		t.Fatalf("creates = %v, want exactly one — the reconcile pass should have pushed the pre-existing route", ledger.creates)
	}
}

// A Garmin session close to the end of its ~year-long life gets a Warn log
// on the reconcile tick, before anything has actually failed — the whole
// point being a rider finds out before the start of a ride, not after a
// push already broke against an expired session.
func TestAutoImportTickWarnsWhenGarminSessionIsExpiringSoon(t *testing.T) {
	h := newConnectHarness(t, true)
	h.connectGarmin("wilant")

	// TokenExpiry has no public way to set directly — reach into the table
	// the way providerlink's own adoption tests already do, and back-date
	// the session to leave it a handful of days inside the warn window.
	backdated := time.Now().Add(-360 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := h.db.Conn().Exec(
		`UPDATE provider_links SET updated_at = ? WHERE provider = 'garmin' AND rider = 'wilant'`,
		backdated); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))

	h.srv.AutoImportTick(context.Background())

	if !strings.Contains(logs.String(), "garmin session expiring soon") {
		t.Fatalf("log = %q, want a warning about the expiring garmin session", logs.String())
	}
}

// Komoot's own side of the same pipeline — no fuzzy duplicate check there
// (see autoImportKomoot's own doc comment for why), so every listed tour is
// simply offered and the exact-tag dedup inside importKomootTours is what
// keeps a second tick from creating a second copy.
func TestAutoImportTickImportsNewKomootTours(t *testing.T) {
	h := newConnectHarness(t, true)
	// fakeKomoot (not the connector's own default fakeImporter) — its GPX
	// returns real, parseable content, the same reason roles_test.go's own
	// Komoot import tests use it rather than the connect fake's placeholder
	// "<gpx/>", which source.Create rejects as a trackless file.
	h.connector.importer = fakeKomoot{tours: []komoot.Tour{{ID: "42", Name: "A tour", Type: komoot.TypePlanned}}}
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/connection",
		`{"email":"rider@example.com","password":"hunter2"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connecting komoot: status = %d", resp.StatusCode)
	}

	if err := h.settings.SetFlag(api.FlagAutoSync, true, "test"); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoImportTick(context.Background())

	routes, _, err := h.db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Name != "A tour" {
		t.Fatalf("routes = %+v, want the one tour imported", routes)
	}

	h.srv.AutoImportTick(context.Background())
	routes, _, err = h.db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes after a second tick = %+v, want no duplicate", routes)
	}
}
