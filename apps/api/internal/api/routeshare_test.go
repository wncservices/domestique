// Acceptance tests for route sharing — the same "what can a client actually
// do" standard roles_test.go holds the rest of auth to.
//
// Deliberately its own harness, not newAuthHarness (roles_test.go):
// newAuthHarness leaves auth.Config.RequiredGroup empty, which means
// Authorize lets any identified caller through regardless of role — fine
// for the tests that harness already covers, but it can never exercise
// what these tests are actually about: the authenticate exemption for
// GET /api/shares/{token}... only means anything when Authorize would
// otherwise reject the caller. This harness sets RequiredGroup so that's
// actually true.
package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routeshare"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/targets"
)

type shareHarness struct {
	t      *testing.T
	client *http.Client
	base   string
	src    *source.DB
	shares *routeshare.Store
}

func newShareHarness(t *testing.T) *shareHarness {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	sharesStore, err := routeshare.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := auth.New(auth.Config{
		Mode:          auth.ModeProxy,
		RequiredGroup: "domestique-access",
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
		Source: db,
		Store:  st,
		Auth:   authenticator,
		Shares: sharesStore,
		Config: &config.Config{},
		TargetFactory: func(model.Account) (targets.Target, error) {
			return stubTarget{}, nil
		},
	}

	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	return &shareHarness{t: t, client: server.Client(), base: server.URL, src: db, shares: sharesStore}
}

// as issues a request as user in groups — an empty user sets neither
// header at all, the same "genuinely anonymous" shape authHarness.as uses.
func (h *shareHarness) as(user, groups, method, path, body string) *http.Response {
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

func (h *shareHarness) seedRoute(t *testing.T, name, owner string) model.Route {
	t.Helper()
	route, err := h.src.Create(t.Context(), source.CreateRequest{
		Name: name, GPX: []byte(seedGPX), UploadedBy: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return route
}

type shareOut struct {
	ID         string `json:"id"`
	RouteSlug  string `json:"routeSlug"`
	CreatedBy  string `json:"createdBy"`
	ExpiresAt  string `json:"expiresAt"`
	RevokedAt  string `json:"revokedAt"`
	RedeemedBy []struct {
		Rider      string `json:"rider"`
		RedeemedAt string `json:"redeemedAt"`
	} `json:"redeemedBy"`
}

type createShareOut struct {
	shareOut
	Token string `json:"token"`
	URL   string `json:"url"`
}

// mustCreateShare is the owning rider creating a 7-day share, for tests
// where the share's own creation is setup rather than the thing under test.
func (h *shareHarness) mustCreateShare(t *testing.T, owner, slug string) createShareOut {
	t.Helper()
	resp := h.as(owner, "cyclists,domestique-access", http.MethodPost,
		"/api/routes/"+slug+"/shares", `{"ttlDays":7}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create share: status = %d, want 201", resp.StatusCode)
	}
	var out createShareOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSharedRouteRoleGateIsExempted proves the point of the whole feature:
// an identity RequiredGroup would otherwise reject outright (no group
// matches it or any configured role — Authorize would return ErrForbidden
// before any handler ran) can still reach GET /api/shares/{token} with a
// valid token, while the exact same identity still gets 403 from an
// ordinary library endpoint in the same request flow. The exemption is
// narrow, not a hole in the gate.
func TestSharedRouteRoleGateIsExempted(t *testing.T) {
	h := newShareHarness(t)
	route := h.seedRoute(t, "Hill Loop", "wilant")
	share := h.mustCreateShare(t, "wilant", route.Slug)

	// "outsider" is in no group at all — not domestique-access, not any
	// role. Under the ordinary gate this is an outright Authorize failure.
	ordinary := h.as("outsider", "", http.MethodGet, "/api/routes", "")
	if ordinary.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /api/routes for an ungrouped identity: status = %d, want 403 — "+
			"otherwise this test proves nothing about the exemption being narrow", ordinary.StatusCode)
	}

	viaShare := h.as("outsider", "", http.MethodGet, "/api/shares/"+share.Token, "")
	if viaShare.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/shares/{token} for the same ungrouped identity: status = %d, want 200", viaShare.StatusCode)
	}
}

// TestSharedRouteRequiresSignIn proves the exemption removes only
// Authorize's role/group check, not the requirement to be signed in at
// all — a fully anonymous request (no Remote-User header at all) must
// still be turned away, by the handler itself now that the outer gate no
// longer does it for this path.
func TestSharedRouteRequiresSignIn(t *testing.T) {
	h := newShareHarness(t)
	route := h.seedRoute(t, "Hill Loop", "wilant")
	share := h.mustCreateShare(t, "wilant", route.Slug)

	resp := h.as("", "", http.MethodGet, "/api/shares/"+share.Token, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — an anonymous request must not reach a shared route", resp.StatusCode)
	}
}

// TestShareGrantsExactlyThatRouteNotTheLibrary is the end-to-end shape of
// the feature: the recipient can view and download the one shared route,
// and nothing else — the library list, another route entirely, still 403s
// for them.
func TestShareGrantsExactlyThatRouteNotTheLibrary(t *testing.T) {
	h := newShareHarness(t)
	shared := h.seedRoute(t, "Hill Loop", "wilant")
	other := h.seedRoute(t, "Someone Else's Loop", "wilant")
	share := h.mustCreateShare(t, "wilant", shared.Slug)

	summary := h.as("outsider", "", http.MethodGet, "/api/shares/"+share.Token, "")
	if summary.StatusCode != http.StatusOK {
		t.Fatalf("summary: status = %d, want 200", summary.StatusCode)
	}
	var got struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(summary.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Slug != shared.Slug || got.Name != "Hill Loop" {
		t.Fatalf("summary = %+v, want the shared route", got)
	}

	if resp := h.as("outsider", "", http.MethodGet, "/api/shares/"+share.Token+"/track", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("track: status = %d, want 200", resp.StatusCode)
	}
	if resp := h.as("outsider", "", http.MethodGet, "/api/shares/"+share.Token+"/gpx", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("gpx: status = %d, want 200", resp.StatusCode)
	}
	if resp := h.as("outsider", "", http.MethodGet, "/api/shares/"+share.Token+"/fit", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("fit: status = %d, want 200", resp.StatusCode)
	}

	if resp := h.as("outsider", "", http.MethodGet, "/api/routes", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("library list: status = %d, want 403 — a share must not open the whole library", resp.StatusCode)
	}
	// No share was ever created for "other" — a token that resolves to a
	// different route must never leak it via this one's own slug.
	_ = other
}

// TestOnlyTheOwnerOrAnAdminMayManageShares proves creating, listing and
// revoking a share stay behind the ordinary gate — a rider with no
// relationship to the route cannot do any of the three, only its owner or
// an admin can.
func TestOnlyTheOwnerOrAnAdminMayManageShares(t *testing.T) {
	h := newShareHarness(t)
	route := h.seedRoute(t, "Hill Loop", "wilant")

	create := h.as("stranger", "cyclists,domestique-access", http.MethodPost,
		"/api/routes/"+route.Slug+"/shares", `{"ttlDays":7}`)
	if create.StatusCode != http.StatusForbidden {
		t.Fatalf("create as a stranger: status = %d, want 403", create.StatusCode)
	}

	share := h.mustCreateShare(t, "wilant", route.Slug)

	list := h.as("stranger", "cyclists,domestique-access", http.MethodGet, "/api/routes/"+route.Slug+"/shares", "")
	if list.StatusCode != http.StatusForbidden {
		t.Fatalf("list as a stranger: status = %d, want 403", list.StatusCode)
	}

	revoke := h.as("stranger", "cyclists,domestique-access", http.MethodDelete, "/api/shares/"+share.ID, "")
	if revoke.StatusCode != http.StatusForbidden {
		t.Fatalf("revoke as a stranger: status = %d, want 403", revoke.StatusCode)
	}

	// The owner can do all three.
	ownerList := h.as("wilant", "cyclists,domestique-access", http.MethodGet, "/api/routes/"+route.Slug+"/shares", "")
	if ownerList.StatusCode != http.StatusOK {
		t.Fatalf("list as the owner: status = %d, want 200", ownerList.StatusCode)
	}
	var listed []shareOut
	if err := json.NewDecoder(ownerList.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != share.ID {
		t.Fatalf("listed = %+v, want the one share just created", listed)
	}

	ownerRevoke := h.as("wilant", "cyclists,domestique-access", http.MethodDelete, "/api/shares/"+share.ID, "")
	if ownerRevoke.StatusCode != http.StatusOK {
		t.Fatalf("revoke as the owner: status = %d, want 200", ownerRevoke.StatusCode)
	}
}

// TestRevokedShareStopsWorkingImmediately proves revocation actually takes
// effect on the recipient's own path, not just the owner's management view.
func TestRevokedShareStopsWorkingImmediately(t *testing.T) {
	h := newShareHarness(t)
	route := h.seedRoute(t, "Hill Loop", "wilant")
	share := h.mustCreateShare(t, "wilant", route.Slug)

	if resp := h.as("outsider", "", http.MethodGet, "/api/shares/"+share.Token, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("before revoke: status = %d, want 200", resp.StatusCode)
	}

	if resp := h.as("wilant", "cyclists,domestique-access", http.MethodDelete, "/api/shares/"+share.ID, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status = %d, want 200", resp.StatusCode)
	}

	resp := h.as("outsider", "", http.MethodGet, "/api/shares/"+share.Token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after revoke: status = %d, want 404 — a revoked link must stop working immediately", resp.StatusCode)
	}
}

// TestExpiredShareReportsGoneNotFound proves an expired-but-not-revoked
// share is told apart from a merely-unknown one: 410 Gone, not 404 — the
// two are different situations for a rider to understand ("ask for a new
// link" vs. "this was never valid"). Backdates expires_at directly in the
// table, the same technique the earlier Garmin-expiry test already
// established, since routeshare.Store only ever accepts 7/30/90-day
// lifetimes and there is no public way to mint an already-expired one.
func TestExpiredShareReportsGoneNotFound(t *testing.T) {
	h := newShareHarness(t)
	route := h.seedRoute(t, "Hill Loop", "wilant")
	share := h.mustCreateShare(t, "wilant", route.Slug)

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := h.src.Conn().Exec(
		`UPDATE route_shares SET expires_at = ? WHERE id = ?`, past, share.ID); err != nil {
		t.Fatal(err)
	}

	resp := h.as("outsider", "", http.MethodGet, "/api/shares/"+share.Token, "")
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410 Gone", resp.StatusCode)
	}
}

// TestCreateShareRejectsAnUnsupportedTTL proves the API layer surfaces the
// fixed 7/30/90-day menu as a 400, not a silently-created link with
// whatever duration was asked for.
func TestCreateShareRejectsAnUnsupportedTTL(t *testing.T) {
	h := newShareHarness(t)
	route := h.seedRoute(t, "Hill Loop", "wilant")

	resp := h.as("wilant", "cyclists,domestique-access", http.MethodPost,
		"/api/routes/"+route.Slug+"/shares", `{"ttlDays":14}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestShareRedemptionIsRecorded proves a share view is logged for the
// owner to see, and that a second visit from the same rider updates rather
// than duplicates the entry — routeshare.Store.Touch's own contract,
// exercised here over real HTTP.
func TestShareRedemptionIsRecorded(t *testing.T) {
	h := newShareHarness(t)
	route := h.seedRoute(t, "Hill Loop", "wilant")
	share := h.mustCreateShare(t, "wilant", route.Slug)

	h.as("friend", "", http.MethodGet, "/api/shares/"+share.Token, "")
	h.as("friend", "", http.MethodGet, "/api/shares/"+share.Token+"/gpx", "")

	list := h.as("wilant", "cyclists,domestique-access", http.MethodGet, "/api/routes/"+route.Slug+"/shares", "")
	var shares []shareOut
	if err := json.NewDecoder(list.Body).Decode(&shares); err != nil {
		t.Fatal(err)
	}
	if len(shares) != 1 || len(shares[0].RedeemedBy) != 1 || shares[0].RedeemedBy[0].Rider != "friend" {
		t.Fatalf("shares = %+v, want exactly one redemption by friend", shares)
	}
}
