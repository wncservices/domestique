// Acceptance tests for authentication and roles, over real HTTP.
//
// These are the tests that decide whether the app is safe to expose. They
// check what a *client* can actually do, not what the auth package believes.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/komoot"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/targets"
)

// authHarness serves a database library behind proxy-mode auth.
type authHarness struct {
	t      *testing.T
	client *http.Client
	base   string
	src    *source.DB
	// links lets a test seed a rider's own personal provider connection —
	// see TestKomootTourDeleteRefusesTheSharedAccount, the reason this
	// harness needs one at all now that delete no longer falls back to the
	// shared client the way listing and importing still do.
	links *providerlink.Store
	// crew lets a test seed crew membership directly — see
	// seedApprovedCrew, the reason this harness needs one at all now that
	// route visibility depends on it (TestRouteVisibility).
	crew *crew.Store
	// schedule lets a test read a scheduled ride's raw state directly,
	// same reason crew above does for membership.
	schedule *schedule.Store
}

func newAuthHarness(t *testing.T, komootClient api.KomootImporter) *authHarness {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
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
	links, err := providerlink.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	crewStore, err := crew.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	scheduleStore, err := schedule.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	appSettings, err := settings.UseDB(db.Conn(), db.DSN(), nil)
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := auth.New(auth.Config{
		Mode: auth.ModeProxy,
		Roles: auth.RoleMapping{
			Admin:  []string{"domestique-admins"},
			Rider:  []string{"cyclists"},
			Viewer: []string{"guests"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{
		Source:   db,
		Store:    store,
		Auth:     authenticator,
		Komoot:   komootClient,
		Links:    links,
		Crew:     crewStore,
		Schedule: scheduleStore,
		Settings: appSettings,
		// Resume ignores the userID/token it is handed and always returns
		// the same fake — this harness only ever needed one Komoot client
		// to test against, whether reached via the shared fallback or (once
		// a test seeds a links row) a rider's own resolved connection.
		Connector: &fakeConnector{importer: komootClient},
		Accounts:  seedRoleAccounts(t, db),
		Config: &config.Config{
			Komoot: config.KomootConfig{Enabled: komootClient != nil},
		},
		TargetFactory: func(model.Account) (targets.Target, error) {
			return stubTarget{}, nil
		},
	}

	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	return &authHarness{t: t, client: server.Client(), base: server.URL, src: db, links: links, crew: crewStore, schedule: scheduleStore}
}

// seedApprovedCrew creates a crew owned by owner and lands every member as
// an approved rider directly in the store — bypassing the invite-then-
// confirm HTTP round trip (see internal/crew's own package doc for why that
// exists) since these tests are about route visibility, not consent itself.
func (h *authHarness) seedApprovedCrew(t *testing.T, owner string, members ...string) string {
	t.Helper()
	c, err := h.crew.Create(context.Background(), "Crew", owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if _, err := h.crew.RequestJoin(context.Background(), c.ID, m); err != nil {
			t.Fatal(err)
		}
		if err := h.crew.Approve(context.Background(), c.ID, m, owner); err != nil {
			t.Fatal(err)
		}
	}
	return c.ID
}

// seedRoleAccounts links one head unit, owned by "wilant".
func seedRoleAccounts(t *testing.T, db *source.DB) *accounts.Store {
	t.Helper()

	store, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Link(t.Context(), model.ProviderGarmin, "wilant", ""); err != nil {
		t.Fatal(err)
	}
	return store
}

type stubTarget struct{}

func (stubTarget) Create(context.Context, model.Route) (string, error) { return "remote-1", nil }
func (stubTarget) Update(context.Context, string, model.Route) (string, error) {
	return "remote-1", nil
}
func (stubTarget) Delete(context.Context, string) error { return nil }

// as issues a request as a user in the given groups.
func (h *authHarness) as(user, groups, method, path string, body string) *http.Response {
	h.t.Helper()

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}

	req, err := http.NewRequest(method, h.base+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if user != "" {
		req.Header.Set(auth.HeaderUser, user)
		req.Header.Set(auth.HeaderGroups, groups)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (h *authHarness) seedRoute(t *testing.T, name, owner string) model.Route {
	t.Helper()
	route, err := h.src.Create(t.Context(), source.CreateRequest{
		Name:       name,
		GPX:        []byte(seedGPX),
		UploadedBy: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return route
}

// seedRouteWithTargets is seedRoute plus an explicit sharing choice — for
// TestRouteVisibility, which needs a route shared to a crew rather than
// left at the owner-only default.
func (h *authHarness) seedRouteWithTargets(t *testing.T, name, owner string, targets []string) model.Route {
	t.Helper()
	route, err := h.src.Create(t.Context(), source.CreateRequest{
		Name:       name,
		GPX:        []byte(seedGPX),
		UploadedBy: owner,
		Targets:    &targets,
	})
	if err != nil {
		t.Fatal(err)
	}
	return route
}

// multipartUpload builds a browser-shaped upload body.
func multipartUpload(t *testing.T, fields map[string]string, gpxBody []byte, filename string) (*bytes.Buffer, string) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(gpxBody); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, writer.FormDataContentType()
}

const seedGPX = `<?xml version="1.0"?>
<gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>
<trkpt lat="50.79" lon="2.81"><ele>42</ele></trkpt>
<trkpt lat="50.80" lon="2.84"><ele>128</ele></trkpt>
</trkseg></trk></gpx>`

// ---------- authentication ----------

// With auth on, an unauthenticated caller gets nothing from the API.
func TestUnauthenticatedIsRejected(t *testing.T) {
	h := newAuthHarness(t, nil)

	for _, path := range []string{"/api/routes", "/api/plan", "/api/accounts"} {
		resp := h.as("", "", http.MethodGet, path, "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, resp.StatusCode)
		}
	}
}

// The health endpoint must stay open, or a liveness probe needs credentials.
func TestHealthIsUnauthenticated(t *testing.T) {
	h := newAuthHarness(t, nil)
	if resp := h.as("", "", http.MethodGet, "/api/health", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("health: status = %d, want 200", resp.StatusCode)
	}
}

func TestMeReportsRoleAndPermissions(t *testing.T) {
	h := newAuthHarness(t, nil)

	for _, tc := range []struct {
		groups   string
		wantRole string
		canPush  bool
	}{
		{"domestique-admins", "admin", true},
		{"cyclists", "rider", true},
		{"guests", "viewer", false},
		{"unmapped", "viewer", false},
	} {
		resp := h.as("someone", tc.groups, http.MethodGet, "/api/me", "")
		var me struct {
			Role        string   `json:"role"`
			Permissions []string `json:"permissions"`
			User        string   `json:"user"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
			t.Fatal(err)
		}

		if me.Role != tc.wantRole {
			t.Errorf("groups %q: role = %q, want %q", tc.groups, me.Role, tc.wantRole)
		}
		canPush := false
		for _, p := range me.Permissions {
			if p == string(auth.PermPush) {
				canPush = true
			}
		}
		if canPush != tc.canPush {
			t.Errorf("groups %q: push permission = %v, want %v", tc.groups, canPush, tc.canPush)
		}
	}
}

// ---------- role enforcement ----------

func TestViewerIsReadOnly(t *testing.T) {
	h := newAuthHarness(t, nil)
	// Owned by the viewer themselves — reading their own route is the
	// permission this test is actually about; a viewer's route-level
	// visibility (own vs. an unrelated rider's) is TestRouteVisibility's
	// own concern, not this one's.
	route := h.seedRoute(t, "Guest's own route", "guest")

	// Reading is allowed.
	if resp := h.as("guest", "guests", http.MethodGet, "/api/routes", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("viewer cannot read routes: %d", resp.StatusCode)
	}
	if resp := h.as("guest", "guests", http.MethodGet, "/api/gpx/"+route.Slug, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("viewer cannot download GPX: %d", resp.StatusCode)
	}

	// Everything that changes something is not.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/push", ""},
		{http.MethodDelete, "/api/routes/" + route.Slug, ""},
		{http.MethodPatch, "/api/routes/" + route.Slug, `{"name":"nope"}`},
		{http.MethodPost, "/api/routes", ""},
	} {
		resp := h.as("guest", "guests", tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403 for a viewer", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestRiderCanPushAndUpload(t *testing.T) {
	h := newAuthHarness(t, nil)

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/push", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("rider cannot push: %d", resp.StatusCode)
	}
}

// Auto-sync changes every rider's upload/edit behavior at once, not just
// the caller's own — admin-only, the same as the Garmin OAuth1 consumer.
func TestAutoSyncSettingRequiresAdmin(t *testing.T) {
	h := newAuthHarness(t, nil)

	if resp := h.as("wilant", "cyclists", http.MethodGet, "/api/settings/auto-sync", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("rider cannot even read the setting: %d", resp.StatusCode)
	}
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/settings/auto-sync", `{"enabled":true}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("rider changed a deployment-wide setting: status = %d, want 403", resp.StatusCode)
	}
	if resp := h.as("boss", "domestique-admins", http.MethodPut, "/api/settings/auto-sync", `{"enabled":true}`); resp.StatusCode != http.StatusOK {
		t.Errorf("admin cannot change it: %d", resp.StatusCode)
	}
}

// The heart of the ownership rule: a rider may not touch someone else's route,
// but an admin may.
func TestRouteOwnership(t *testing.T) {
	h := newAuthHarness(t, nil)
	theirs := h.seedRoute(t, "Friend's route", "friend")
	mine := h.seedRoute(t, "My route", "wilant")

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/routes/"+theirs.Slug, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rider deleted another rider's route: status = %d", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodPatch, "/api/routes/"+theirs.Slug, `{"name":"hijacked"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rider edited another rider's route: status = %d", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodDelete, "/api/routes/"+mine.Slug, "")
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("rider cannot delete their own route: status = %d", resp.StatusCode)
	}

	resp = h.as("boss", "domestique-admins", http.MethodDelete, "/api/routes/"+theirs.Slug, "")
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("admin cannot delete another rider's route: status = %d", resp.StatusCode)
	}
}

// The security-critical case this whole harness change exists for: a rider
// with no crew membership and nothing shared to them must not see, list, or
// download a route they have no relationship to — found live, an
// authenticated rider with nothing connected could still see every route in
// the deployment. Covers all four read surfaces (list, track, GPX, FIT) plus
// the crew-sharing and admin-bypass cases config.VisibleTo itself doesn't
// exercise end to end.
func TestRouteVisibility(t *testing.T) {
	h := newAuthHarness(t, nil)
	mine := h.seedRoute(t, "My route", "wilant")
	unrelated := h.seedRoute(t, "Stranger's route", "friend")
	crewID := h.seedApprovedCrew(t, "friend", "wilant")
	shared := h.seedRouteWithTargets(t, "Shared with the crew", "friend", []string{crewID})

	list := func(user, groups string) map[string]bool {
		resp := h.as(user, groups, http.MethodGet, "/api/routes", "")
		var body struct {
			Routes []struct {
				Slug string `json:"slug"`
			} `json:"routes"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		slugs := map[string]bool{}
		for _, r := range body.Routes {
			slugs[r.Slug] = true
		}
		return slugs
	}

	seen := list("wilant", "cyclists")
	if !seen[mine.Slug] {
		t.Error("own route missing from the list")
	}
	if seen[unrelated.Slug] {
		t.Error("an unrelated rider's route is visible in the list")
	}
	if !seen[shared.Slug] {
		t.Error("a route shared to a crew wilant belongs to is missing from the list")
	}

	// Hiding it from the list is not enough on its own — a rider who
	// already knows (or guesses) the slug must not be able to reach it any
	// other way either.
	for _, path := range []string{
		"/api/tracks/" + unrelated.Slug,
		"/api/gpx/" + unrelated.Slug,
		"/api/fit/" + unrelated.Slug,
	} {
		resp := h.as("wilant", "cyclists", http.MethodGet, path, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s for an unrelated route: status = %d, want 404", path, resp.StatusCode)
		}
	}
	// The identical 404 an actually-nonexistent slug gets — existence
	// itself is what this is scoped to hide, so the two must not be
	// distinguishable.
	unknownResp := h.as("wilant", "cyclists", http.MethodGet, "/api/gpx/does-not-exist", "")
	unrelatedResp := h.as("wilant", "cyclists", http.MethodGet, "/api/gpx/"+unrelated.Slug, "")
	var unknownBody, unrelatedBody map[string]string
	_ = json.NewDecoder(unknownResp.Body).Decode(&unknownBody)
	_ = json.NewDecoder(unrelatedResp.Body).Decode(&unrelatedBody)
	if unknownBody["error"] != unrelatedBody["error"] {
		t.Errorf("bodies differ: unknown slug = %v, unrelated route = %v — existence is leaking", unknownBody, unrelatedBody)
	}

	// The crew-shared route is reachable the same way the owner's own is.
	for _, path := range []string{
		"/api/tracks/" + shared.Slug,
		"/api/gpx/" + shared.Slug,
		"/api/fit/" + shared.Slug,
	} {
		resp := h.as("wilant", "cyclists", http.MethodGet, path, "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s for a crew-shared route: status = %d, want 200", path, resp.StatusCode)
		}
	}

	// An admin's own list and single-route reads are never filtered.
	adminSeen := list("boss", "domestique-admins")
	if !adminSeen[unrelated.Slug] {
		t.Error("admin's list is missing an unrelated rider's route")
	}
	if resp := h.as("boss", "domestique-admins", http.MethodGet, "/api/gpx/"+unrelated.Slug, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("admin GET of an unrelated route: status = %d, want 200", resp.StatusCode)
	}
}

// A rider must see their own linked head units and a crew fellow's — the
// only relationships that can ever make an account relevant to them, since
// a shared crew is the only way a route reaches another rider's device in
// the first place — but never an unrelated stranger's, regardless of
// PermReadRoutes being the lowest permission tier there is. Found live: a
// test account with no relationship to anyone still saw every account in
// the deployment on its own Settings page.
func TestAccountVisibility(t *testing.T) {
	h := newAuthHarness(t, nil)
	// seedRoleAccounts already links garmin:wilant.

	resp := h.as("friend", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("friend linking wahoo: status = %d", resp.StatusCode)
	}

	h.seedApprovedCrew(t, "wilant", "buddy")
	resp = h.as("buddy", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"garmin"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("buddy linking garmin: status = %d", resp.StatusCode)
	}

	list := func(user, groups string) map[string]bool {
		resp := h.as(user, groups, http.MethodGet, "/api/accounts", "")
		var accounts []struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&accounts); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, a := range accounts {
			ids[a.ID] = true
		}
		return ids
	}

	seen := list("wilant", "cyclists")
	if !seen["garmin:wilant"] {
		t.Error("own account missing from the list")
	}
	if !seen["garmin:buddy"] {
		t.Error("a crew fellow's account is missing from the list")
	}
	if seen["wahoo:friend"] {
		t.Error("an unrelated rider's account is visible in the list")
	}

	// The accounts list is what a rider (or admin) sees on their own
	// Settings page — a directory of who links which head unit, not a grant
	// of authority. An admin's PermEditAny bypass is for acting on routes
	// and accounts (see TestPlanAndPushScopedToVisibleAccounts below), not
	// for browsing every rider's linked devices; that would make every
	// admin's own Settings page a deployment-wide accounts directory for no
	// reason this page actually needs.
	// "boss" has no account of their own here and is in nobody's crew, so a
	// correctly scoped list is empty — same rule an ordinary rider in that
	// position would get, PermEditAny notwithstanding.
	adminSeen := list("boss", "domestique-admins")
	if len(adminSeen) != 0 {
		t.Errorf("admin with no account and no crew overlap should see nothing listed, got %v", adminSeen)
	}

	// Put the admin in wilant's crew and confirm the ordinary crew-fellow
	// rule applies to them too, not a deployment-wide bypass.
	h.seedApprovedCrew(t, "wilant", "boss")
	adminSeen = list("boss", "domestique-admins")
	if !adminSeen["garmin:wilant"] {
		t.Error("admin should see a crew fellow's account, same as anyone else in that crew")
	}
	if adminSeen["wahoo:friend"] {
		t.Error("admin's list should not include an unrelated rider's account — listing is not authority")
	}
}

// The same visibility boundary applies to the pending-changes plan and to
// push itself — not just the accounts list. Found live: an unrelated test
// rider saw dozens of pending *deletes* queued against another rider's real
// Garmin account, and nothing stopped a plain "Push to devices" (or a
// request naming that account id directly in its selection) from actually
// applying them.
func TestPlanAndPushScopedToVisibleAccounts(t *testing.T) {
	h := newAuthHarness(t, nil)
	// seedRoleAccounts already links garmin:wilant.

	if resp := h.as("friend", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("friend linking wahoo: status = %d", resp.StatusCode)
	}

	body, contentType := multipartUpload(t, map[string]string{"name": "Friend's Route"}, []byte(seedGPX), "route.gpx")
	req, err := http.NewRequest(http.MethodPost, h.base+"/api/routes", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(auth.HeaderUser, "friend")
	req.Header.Set(auth.HeaderGroups, "cyclists")
	uploadResp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer uploadResp.Body.Close()
	var route struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(uploadResp.Body).Decode(&route); err != nil {
		t.Fatal(err)
	}

	// A real push, as friend, so wahoo:friend has recorded sync state to
	// go stale.
	if resp := h.as("friend", "cyclists", http.MethodPost, "/api/push", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("initial push: status = %d", resp.StatusCode)
	}
	// Removing it from the library leaves wahoo:friend's recorded state
	// pointing at a route that no longer exists — exactly the "removed
	// from the library or no longer targeted" pending delete reported live.
	if resp := h.as("friend", "cyclists", http.MethodDelete, "/api/routes/"+route.Slug, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d", resp.StatusCode)
	}

	type planDTO struct {
		Items []struct {
			AccountID string `json:"accountId"`
		} `json:"items"`
	}
	hasWahooFriend := func(user, groups string) bool {
		resp := h.as(user, groups, http.MethodGet, "/api/plan", "")
		var plan planDTO
		if err := json.NewDecoder(resp.Body).Decode(&plan); err != nil {
			t.Fatal(err)
		}
		for _, item := range plan.Items {
			if item.AccountID == "wahoo:friend" {
				return true
			}
		}
		return false
	}

	if hasWahooFriend("wilant", "cyclists") {
		t.Error("an unrelated rider's pending delete is visible in the plan")
	}

	// Neither a full push nor one whose selection names the account
	// directly may touch it.
	for _, pushBody := range []string{"", `{"items":[{"accountId":"wahoo:friend","slug":"` + route.Slug + `"}]}`} {
		resp := h.as("wilant", "cyclists", http.MethodPost, "/api/push", pushBody)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("wilant push (body %q): status = %d", pushBody, resp.StatusCode)
		}
	}

	// Proof nothing above actually reached it: the pending delete for
	// wahoo:friend is exactly as it was.
	if !hasWahooFriend("friend", "cyclists") {
		t.Error("the pending delete for wahoo:friend disappeared — an unrelated rider's push applied it")
	}
	if !hasWahooFriend("boss", "domestique-admins") {
		t.Error("admin's plan is missing wahoo:friend's pending delete")
	}
}

// A route's SyncState answers "what would happen on my own devices," not
// "who else has this" — every viewer, including the route's own owner and
// an admin, sees only their own account(s) there, never a crew fellow's.
// TargetsFor (used by handlePlan/handlePush to decide where a push
// actually goes) still resolves a shared route against every crew member's
// account, unfiltered — only this read-only display is narrowed to one
// person's own devices.
func TestRouteSyncStatusShowsOnlyYourOwnAccounts(t *testing.T) {
	h := newAuthHarness(t, nil)
	// seedRoleAccounts already links garmin:wilant.

	crewA := h.seedApprovedCrew(t, "wilant", "buddy")
	crewB := h.seedApprovedCrew(t, "wilant", "stranger")

	if resp := h.as("buddy", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("buddy linking wahoo: status = %d", resp.StatusCode)
	}
	if resp := h.as("stranger", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"garmin"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("stranger linking garmin: status = %d", resp.StatusCode)
	}

	body, contentType := multipartUpload(t, map[string]string{
		"name":    "Shared with both crews",
		"targets": crewA + "," + crewB,
	}, []byte(seedGPX), "route.gpx")
	req, err := http.NewRequest(http.MethodPost, h.base+"/api/routes", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(auth.HeaderUser, "wilant")
	req.Header.Set(auth.HeaderGroups, "cyclists")
	uploadResp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer uploadResp.Body.Close()

	type routeDTO struct {
		Slug      string `json:"slug"`
		SyncState []struct {
			AccountID string `json:"accountId"`
		} `json:"syncState"`
	}
	accountIDs := func(dto routeDTO) map[string]bool {
		out := map[string]bool{}
		for _, s := range dto.SyncState {
			out[s.AccountID] = true
		}
		return out
	}

	var uploaded routeDTO
	if err := json.NewDecoder(uploadResp.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	// wilant, the uploader and owner, still sees only their own account —
	// owning a route shared to two crews is not the same question as
	// "whose devices does this reach."
	seen := accountIDs(uploaded)
	if !seen["garmin:wilant"] {
		t.Error("owner cannot see their own account in sync state")
	}
	if seen["wahoo:buddy"] || seen["garmin:stranger"] {
		t.Errorf("owner's own upload response = %v, want only garmin:wilant", seen)
	}

	// buddy (crewA) sees only their own account — not the owner's, not
	// stranger's (a crewB member they share no crew with either).
	listResp := h.as("buddy", "cyclists", http.MethodGet, "/api/routes", "")
	var list struct {
		Routes []routeDTO `json:"routes"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Routes) != 1 {
		t.Fatalf("routes = %+v, want the one shared route", list.Routes)
	}
	seen = accountIDs(list.Routes[0])
	if !seen["wahoo:buddy"] {
		t.Error("buddy cannot see their own account in sync state")
	}
	if seen["garmin:wilant"] || seen["garmin:stranger"] {
		t.Errorf("buddy sees %v, want only wahoo:buddy", seen)
	}

	// An admin sees every route (visibleRoutes' own, separate bypass —
	// untouched here), but that is authority to manage a route, not a
	// stake in whose devices it reaches. boss has no linked account of
	// their own, so they see none here either.
	adminResp := h.as("boss", "domestique-admins", http.MethodGet, "/api/routes", "")
	var adminList struct {
		Routes []routeDTO `json:"routes"`
	}
	if err := json.NewDecoder(adminResp.Body).Decode(&adminList); err != nil {
		t.Fatal(err)
	}
	if len(adminList.Routes) != 1 {
		t.Fatalf("admin routes = %+v, want the one shared route (visibleRoutes still bypasses)", adminList.Routes)
	}
	seen = accountIDs(adminList.Routes[0])
	if seen["wahoo:buddy"] || seen["garmin:stranger"] || seen["garmin:wilant"] {
		t.Errorf("admin sees %v — an admin with no linked account of their own should see no accounts in this route's sync state, only that the route itself exists", seen)
	}
}

// Ownership comes from the session, never the form — otherwise a rider could
// upload as someone else and put the route beyond their own reach.
func TestUploadOwnershipComesFromIdentity(t *testing.T) {
	h := newAuthHarness(t, nil)

	body, contentType := multipartUpload(t, map[string]string{
		"name":       "Sneaky",
		"uploadedBy": "someone-else",
	}, []byte(seedGPX), "route.gpx")

	req, err := http.NewRequest(http.MethodPost, h.base+"/api/routes", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(auth.HeaderUser, "wilant")
	req.Header.Set(auth.HeaderGroups, "cyclists")

	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload failed: %d", resp.StatusCode)
	}

	var created struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	// The uploader owns it, so they can delete it.
	del := h.as("wilant", "cyclists", http.MethodDelete, "/api/routes/"+created.Slug, "")
	if del.StatusCode != http.StatusNoContent {
		t.Errorf("uploader cannot delete their own upload (status %d) — "+
			"the form's uploadedBy was trusted over the session", del.StatusCode)
	}
}

// ---------- komoot ----------

type fakeKomoot struct {
	tours []komoot.Tour
	err   error
}

// Tours mirrors the real client's own filtering (see komoot.go's Tours) so
// tests can rely on the same guarantee production code does: passing
// includeRecorded=false — what handleKomootDuplicates and
// handleKomootTourDelete always do — never hands back a recorded ride.
func (f fakeKomoot) Tours(_ context.Context, includeRecorded bool) ([]komoot.Tour, error) {
	if f.err != nil {
		return nil, f.err
	}
	if includeRecorded {
		return f.tours, nil
	}
	planned := make([]komoot.Tour, 0, len(f.tours))
	for _, t := range f.tours {
		if t.Planned() {
			planned = append(planned, t)
		}
	}
	return planned, nil
}

func (f fakeKomoot) GPX(_ context.Context, id string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []byte(seedGPX), nil
}

func (f fakeKomoot) DeleteTour(context.Context, string) error {
	return f.err
}

func TestKomootRequiresRider(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{tours: []komoot.Tour{{ID: "1", Name: "A loop"}}})

	if resp := h.as("guest", "guests", http.MethodGet, "/api/komoot/tours", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer listed Komoot tours: %d", resp.StatusCode)
	}
	if resp := h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/tours", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("rider cannot list Komoot tours: %d", resp.StatusCode)
	}
}

func TestKomootImportIsIdempotent(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{tours: []komoot.Tour{
		{ID: "42", Name: "Kemmelberg via Komoot", Type: komoot.TypePlanned},
	}})

	first := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/import", `{"tourIds":["42"]}`)
	var result struct {
		Imported []string          `json:"imported"`
		Skipped  map[string]string `json:"skipped"`
	}
	if err := json.NewDecoder(first.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Imported) != 1 {
		t.Fatalf("first import = %+v, want one route", result)
	}

	// Importing again must not duplicate: a rider would not know which copy
	// their device is following.
	second := h.as("wilant", "cyclists", http.MethodPost, "/api/komoot/import", `{"tourIds":["42"]}`)
	result.Imported = nil
	if err := json.NewDecoder(second.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Imported) != 0 {
		t.Errorf("re-import created a duplicate: %+v", result)
	}
	if result.Skipped["42"] == "" {
		t.Error("re-import gave no reason for skipping")
	}
}

// Komoot's API is undocumented and can vanish. That must read as an upstream
// failure, not as this app being broken.
func TestKomootUpstreamFailureIsBadGateway(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{err: fmt.Errorf("komoot: could not decode response")})

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/tours", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestKomootDisabledWhenNotConfigured(t *testing.T) {
	h := newAuthHarness(t, nil)

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/tours", "")
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 when Komoot is not configured", resp.StatusCode)
	}
}

// komootDuplicateGroupJSON mirrors api.komootDuplicateGroupDTO's wire shape —
// duplicated here rather than exported, since this is package api_test and
// only the fields these tests actually assert on are needed.
type komootDuplicateGroupJSON struct {
	Name  string `json:"name"`
	Tours []struct {
		ID string `json:"id"`
	} `json:"tours"`
}

func TestKomootDuplicatesRequiresRider(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{tours: []komoot.Tour{
		{ID: "1", Name: "Kemmelberg Loop", DistanceM: 55000, Type: komoot.TypePlanned},
		{ID: "2", Name: "Kemmelberg Loop", DistanceM: 55050, Type: komoot.TypePlanned},
	}})

	if resp := h.as("guest", "guests", http.MethodGet, "/api/komoot/tours/duplicates", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer listed komoot duplicates: %d", resp.StatusCode)
	}

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/tours/duplicates", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rider cannot list komoot duplicates: %d", resp.StatusCode)
	}
	var groups []komootDuplicateGroupJSON
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Tours) != 2 {
		t.Fatalf("groups = %+v, want one group of two repeated tours", groups)
	}
}

// A recorded ride must never turn up as a "duplicate" to clean up — deleting
// one is a real ride gone, not a redundant plotted route.
func TestKomootDuplicatesExcludesRecordedRides(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{tours: []komoot.Tour{
		{ID: "1", Name: "Tuesday ride", DistanceM: 31000, Type: komoot.TypeRecorded},
		{ID: "2", Name: "Tuesday ride", DistanceM: 31010, Type: komoot.TypeRecorded},
	}})

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/komoot/tours/duplicates", "")
	var groups []komootDuplicateGroupJSON
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("recorded rides grouped as duplicates: %+v", groups)
	}
}

// seedKomootLink gives rider a personal Komoot connection — delete
// (unlike listing and importing) needs one, see komootOwnConnectionFor.
// What's actually stored does not matter: this harness's fakeConnector
// ignores the userID/token it is handed and always resolves to the same
// fake client either way.
func (h *authHarness) seedKomootLink(t *testing.T, rider string) {
	t.Helper()
	if _, err := h.links.Save("komoot", rider, providerlink.Connection{
		Email: rider + "@example.test", Secret: "token",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKomootTourDeleteRequiresRider(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{tours: []komoot.Tour{
		{ID: "1", Name: "Loop", Type: komoot.TypePlanned},
	}})

	if resp := h.as("guest", "guests", http.MethodDelete, "/api/komoot/tours/1", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer deleted a komoot tour: %d", resp.StatusCode)
	}
}

// The security-critical case: a rider with no personal Komoot connection of
// their own must not be able to delete from the deployment-wide shared
// account, even though listing and importing both fall back to it freely —
// see komootOwnConnectionFor's own doc comment for why delete is different.
func TestKomootTourDeleteRefusesWithNoPersonalConnection(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{tours: []komoot.Tour{
		{ID: "1", Name: "Loop", Type: komoot.TypePlanned},
	}})

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/komoot/tours/1", "")
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412 — wilant never connected Komoot personally", resp.StatusCode)
	}
}

func TestKomootTourDeleteSucceedsForATourOnTheAccount(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{tours: []komoot.Tour{
		{ID: "1", Name: "Loop", Type: komoot.TypePlanned},
	}})
	h.seedKomootLink(t, "wilant")

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/komoot/tours/1", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 deleting a tour actually on the account", resp.StatusCode)
	}
}

// The id in the URL is never trusted on its own — it must still be on the
// account's own (planned-only) tour list, the same "re-list, don't trust the
// client" rule handleKomootImport already follows.
func TestKomootTourDeleteRefusesAnIDNotOnTheAccount(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{tours: []komoot.Tour{
		{ID: "1", Name: "Loop", Type: komoot.TypePlanned},
	}})
	h.seedKomootLink(t, "wilant")

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/komoot/tours/not-mine", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for an id not on this Komoot account", resp.StatusCode)
	}
}

// The same guarantee TestKomootDuplicatesExcludesRecordedRides checks for
// listing: a recorded ride's id can never pass the delete handler's own
// re-check either, because that check also lists with includeRecorded=false.
func TestKomootTourDeleteRefusesARecordedRide(t *testing.T) {
	h := newAuthHarness(t, fakeKomoot{tours: []komoot.Tour{
		{ID: "1", Name: "Tuesday ride", Type: komoot.TypeRecorded},
	}})
	h.seedKomootLink(t, "wilant")

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/komoot/tours/1", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — a recorded ride must never be deletable here", resp.StatusCode)
	}
}

// ---------- linking head units ----------

// An account is yours because you linked it. The rider comes from the session,
// never the body — otherwise someone could create an account they cannot then
// unlink, or plant one on somebody else.
func TestLinkingUsesTheSessionRider(t *testing.T) {
	h := newAuthHarness(t, nil)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo","rider":"somebody-else"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for linking as another rider", resp.StatusCode)
	}

	resp = h.as("wilant", "cyclists", http.MethodPost, "/api/accounts", `{"provider":"wahoo"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("linking own account: status = %d", resp.StatusCode)
	}

	var created struct {
		ID    string `json:"id"`
		Rider string `json:"rider"`
		Mine  bool   `json:"mine"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Rider != "wilant" || created.ID != "wahoo:wilant" {
		t.Errorf("created = %+v, want it owned by the session user", created)
	}
	if !created.Mine {
		t.Error("the linker cannot manage what they just linked")
	}
}

func TestLinkingTwiceIsAConflict(t *testing.T) {
	h := newAuthHarness(t, nil)

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first link: %d", resp.StatusCode)
	}
	// One head unit per rider per provider; a duplicate would give two rows
	// claiming the same device.
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("second link: status = %d, want 409", resp.StatusCode)
	}
}

func TestUnlinkingRespectsOwnership(t *testing.T) {
	h := newAuthHarness(t, nil)

	// The harness links garmin:wilant. Another rider must not remove it.
	if resp := h.as("friend", "cyclists", http.MethodDelete,
		"/api/accounts/garmin:wilant", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("another rider unlinked it: status = %d, want 403", resp.StatusCode)
	}

	// An admin may.
	if resp := h.as("boss", "domestique-admins", http.MethodDelete,
		"/api/accounts/garmin:wilant", ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("admin unlink: status = %d, want 204", resp.StatusCode)
	}
}

func TestViewerCannotLink(t *testing.T) {
	h := newAuthHarness(t, nil)

	if resp := h.as("guest", "guests", http.MethodPost, "/api/accounts",
		`{"provider":"garmin"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer linked an account: status = %d, want 403", resp.StatusCode)
	}
}

func TestUnlinkingSomethingMissing(t *testing.T) {
	h := newAuthHarness(t, nil)

	if resp := h.as("wilant", "cyclists", http.MethodDelete,
		"/api/accounts/garmin:nobody", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// A route with no targets of its own goes to every linked head unit, so
// linking one immediately gives the routes somewhere to go.
// Linking a new account must not silently expand what an already-existing
// route reaches — that was the exact gap crews exist to close. A route
// with no explicit targets reaches only its owner's own accounts,
// regardless of who else links something afterwards; reaching a stranger's
// account now requires that stranger to be an approved member of a crew
// the route was deliberately shared to.
func TestLinkingANewAccountDoesNotExpandExistingRoutes(t *testing.T) {
	h := newAuthHarness(t, nil)
	h.seedRoute(t, "Private route", "wilant")

	before := h.routeTargets(t)
	if len(before) != 1 || before[0] != "garmin:wilant" {
		t.Fatalf("targets = %v, want only garmin:wilant", before)
	}

	if resp := h.as("friend", "cyclists", http.MethodPost, "/api/accounts",
		`{"provider":"wahoo"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("link: %d", resp.StatusCode)
	}

	after := h.routeTargets(t)
	if len(after) != 1 || after[0] != "garmin:wilant" {
		t.Errorf("targets = %v after a stranger linked an account, want unchanged at [garmin:wilant]", after)
	}
}

// routeTargets reads what the first route currently resolves to —
// SyncState, not Targets: Targets holds the crew ids a route names, not
// the accounts it actually reaches (see config.TargetsFor).
func (h *authHarness) routeTargets(t *testing.T) []string {
	t.Helper()

	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/routes", "")
	var library struct {
		Routes []struct {
			SyncState []struct {
				AccountID string `json:"accountId"`
			} `json:"syncState"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&library); err != nil {
		t.Fatal(err)
	}
	if len(library.Routes) == 0 {
		return nil
	}
	out := make([]string, 0, len(library.Routes[0].SyncState))
	for _, s := range library.Routes[0].SyncState {
		out = append(out, s.AccountID)
	}
	return out
}

// /api/config's Source names the database host and port — internal cluster
// topology, not a rider's business, even though it carries no password (see
// dbx.Redact). Only an admin should see it at all.
func TestConfigSourceIsAdminOnly(t *testing.T) {
	h := newAuthHarness(t, nil)

	configFor := func(user, groups string) string {
		t.Helper()
		resp := h.as(user, groups, http.MethodGet, "/api/config", "")
		var body struct {
			Source string `json:"source"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Source
	}

	if got := configFor("wilant", "domestique-admins"); got == "" {
		t.Error("admin got no source, want the database description")
	}
	if got := configFor("wilant", "cyclists"); got != "" {
		t.Errorf("rider got source %q, want it withheld", got)
	}
	if got := configFor("guest", "guests"); got != "" {
		t.Errorf("viewer got source %q, want it withheld", got)
	}
}
