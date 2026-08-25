package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

type crewHarness struct {
	t      *testing.T
	client *http.Client
	base   string
}

func newCrewHarness(t *testing.T) *crewHarness {
	t.Helper()

	db, err := source.OpenDB(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	crewStore, err := crew.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := auth.New(auth.Config{
		Mode:  auth.ModeProxy,
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}, Viewer: []string{"guests"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{Auth: authenticator, Crew: crewStore}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	return &crewHarness{t: t, client: server.Client(), base: server.URL}
}

func (h *crewHarness) as(user, groups, method, path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Remote-User", user)
	req.Header.Set("Remote-Groups", groups)
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

type crewDTOOut struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Owners           []string `json:"owners"`
	Mine             bool     `json:"mine"`
	MembershipStatus string   `json:"membershipStatus"`
	MembershipOrigin string   `json:"membershipOrigin"`
	MemberCount      int      `json:"memberCount"`
	AutoShare        bool     `json:"autoShare"`
	Members          []struct {
		Rider  string `json:"rider"`
		Status string `json:"status"`
		Origin string `json:"origin"`
		Owner  bool   `json:"owner"`
	} `json:"members"`
}

// isOwner reports whether rider appears in a crewDTOOut's Owners list.
func (c crewDTOOut) isOwner(rider string) bool {
	for _, o := range c.Owners {
		if strings.EqualFold(o, rider) {
			return true
		}
	}
	return false
}

func decodeCrew(t *testing.T, resp *http.Response) crewDTOOut {
	t.Helper()
	var out crewDTOOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCreateCrewEnrollsTheOwner(t *testing.T) {
	h := newCrewHarness(t)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Sunday Club"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	c := decodeCrew(t, resp)
	if c.ID != "crew:sunday-club" {
		t.Errorf("id = %q", c.ID)
	}
	if !c.isOwner("wilant") || !c.Mine {
		t.Errorf("owners = %v, mine = %v", c.Owners, c.Mine)
	}
	if c.MembershipStatus != "approved" || c.MemberCount != 1 {
		t.Errorf("membershipStatus = %q, memberCount = %d, want approved/1", c.MembershipStatus, c.MemberCount)
	}
	if len(c.Members) != 1 || c.Members[0].Rider != "wilant" {
		t.Errorf("members = %v", c.Members)
	}
}

func TestListCrewsReportsEachViewersOwnStatus(t *testing.T) {
	h := newCrewHarness(t)
	h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`)

	// A rider who hasn't touched the crew sees "none", no members leaked.
	resp := h.as("other", "cyclists", http.MethodGet, "/api/crews", "")
	var list []crewDTOOut
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d crews, want 1", len(list))
	}
	if list[0].MembershipStatus != "none" || list[0].Mine {
		t.Errorf("membershipStatus = %q, mine = %v, want none/false", list[0].MembershipStatus, list[0].Mine)
	}
	if list[0].Members != nil {
		t.Error("members leaked to a non-owner")
	}
}

func TestJoinApproveRemoveFlow(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))

	joinResp := h.as("other", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/join", "")
	if joinResp.StatusCode != http.StatusOK {
		t.Fatalf("join status = %d, want 200", joinResp.StatusCode)
	}
	joined := decodeCrew(t, joinResp)
	if joined.MembershipStatus != "pending" {
		t.Fatalf("membershipStatus = %q, want pending", joined.MembershipStatus)
	}

	// Joining twice is a conflict, not a second row.
	if resp := h.as("other", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/join", ""); resp.StatusCode != http.StatusConflict {
		t.Errorf("second join status = %d, want 409", resp.StatusCode)
	}

	// A non-owner cannot approve.
	if resp := h.as("someone-else", "cyclists", http.MethodPut, "/api/crews/"+created.ID+"/members/other", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-owner approve status = %d, want 403", resp.StatusCode)
	}

	approveResp := h.as("wilant", "cyclists", http.MethodPut, "/api/crews/"+created.ID+"/members/other", "")
	if approveResp.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d, want 200", approveResp.StatusCode)
	}
	approved := decodeCrew(t, approveResp)
	if approved.MemberCount != 2 {
		t.Errorf("memberCount = %d, want 2", approved.MemberCount)
	}

	// The member can leave on their own — no ownership check needed for self.
	if resp := h.as("other", "cyclists", http.MethodDelete, "/api/crews/"+created.ID+"/members/other", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("self-leave status = %d, want 200", resp.StatusCode)
	}

	var list []crewDTOOut
	listResp := h.as("wilant", "cyclists", http.MethodGet, "/api/crews", "")
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list[0].MemberCount != 1 {
		t.Errorf("memberCount after leaving = %d, want 1", list[0].MemberCount)
	}
}

// A rider removing someone else's membership needs to be the crew's owner
// or an admin — the same ownership rule accounts and routes already keep.
// The owner's other route into a crew: adding someone directly, without
// that rider ever having requested to join. Unlike a self-request, this
// lands the invited rider pending, not approved — the owner's say-so alone
// is not consent from the other side; see TestInvitedRiderMustConfirm.
func TestOwnerCanAddMemberDirectly(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add status = %d, want 200", resp.StatusCode)
	}
	added := decodeCrew(t, resp)
	if added.MemberCount != 1 {
		t.Errorf("memberCount = %d, want 1 (invite still unconfirmed)", added.MemberCount)
	}

	// The invited rider sees a pending invite, not membership — they still
	// have to confirm it themselves.
	list := h.as("other", "cyclists", http.MethodGet, "/api/crews", "")
	var out []crewDTOOut
	if err := json.NewDecoder(list.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out[0].MembershipStatus != "pending" {
		t.Errorf("membershipStatus = %q, want pending", out[0].MembershipStatus)
	}
	if out[0].MembershipOrigin != "invite" {
		t.Errorf("membershipOrigin = %q, want invite", out[0].MembershipOrigin)
	}
}

// The invited rider's own confirmation is what actually grants the invite —
// the owner's AddMember only starts it. The same endpoint the owner uses to
// approve a self-request (PUT .../members/{rider}) does this too, when the
// caller is the rider named in the path.
func TestInvitedRiderMustConfirm(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))
	h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)

	// The owner cannot grant their own invite — that would let the owner
	// supply both sides of the consent.
	ownerAttempt := h.as("wilant", "cyclists", http.MethodPut, "/api/crews/"+created.ID+"/members/other", "")
	if ownerAttempt.StatusCode != http.StatusConflict {
		t.Fatalf("owner confirming own invite: status = %d, want 409", ownerAttempt.StatusCode)
	}

	confirmed := decodeCrew(t, h.as("other", "cyclists", http.MethodPut, "/api/crews/"+created.ID+"/members/other", ""))
	if confirmed.MemberCount != 2 {
		t.Errorf("memberCount after confirm = %d, want 2", confirmed.MemberCount)
	}

	list := h.as("other", "cyclists", http.MethodGet, "/api/crews", "")
	var out []crewDTOOut
	if err := json.NewDecoder(list.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out[0].MembershipStatus != "approved" {
		t.Errorf("membershipStatus after confirm = %q, want approved", out[0].MembershipStatus)
	}
}

// The mirror case: a rider requesting to join cannot approve themselves by
// hitting the same endpoint — that consent still needs to come from the
// owner.
func TestSelfRequestCannotBeSelfApproved(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))
	h.as("other", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/join", "")

	resp := h.as("other", "cyclists", http.MethodPut, "/api/crews/"+created.ID+"/members/other", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("self-approving a join request: status = %d, want 404", resp.StatusCode)
	}
}

func TestOnlyOwnerOrAdminCanAddMember(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))

	resp := h.as("someone-else", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner add status = %d, want 403", resp.StatusCode)
	}

	adminResp := h.as("boss", "admins", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)
	if adminResp.StatusCode != http.StatusOK {
		t.Fatalf("admin add status = %d, want 200", adminResp.StatusCode)
	}
}

func TestAddingAnAlreadyApprovedMemberIsAConflict(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))
	h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)
	h.as("other", "cyclists", http.MethodPut, "/api/crews/"+created.ID+"/members/other", "")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// Re-inviting a rider whose invite is still unconfirmed changes nothing —
// it must not silently grant what only the invited rider's own confirm may.
func TestReAddingAnUnconfirmedInviteIsANoOp(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))
	h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-invite status = %d, want 200", resp.StatusCode)
	}
	again := decodeCrew(t, resp)
	if again.MemberCount != 1 {
		t.Errorf("memberCount = %d, want 1 (still unconfirmed)", again.MemberCount)
	}
}

// Adding a rider who already has a pending request approves it in one
// step, instead of forcing the owner to deny and re-add.
func TestAddingAPendingRiderApprovesThem(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))
	h.as("other", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/join", "")

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add status = %d, want 200", resp.StatusCode)
	}
	added := decodeCrew(t, resp)
	if added.MemberCount != 2 {
		t.Errorf("memberCount = %d, want 2", added.MemberCount)
	}
}

func TestAddMemberToNonexistentCrewIs404(t *testing.T) {
	h := newCrewHarness(t)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/crew:does-not-exist/members", `{"rider":"other"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestNonOwnerCannotRemoveSomeoneElse(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))
	h.as("other", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/join", "")
	h.as("wilant", "cyclists", http.MethodPut, "/api/crews/"+created.ID+"/members/other", "")

	resp := h.as("random", "cyclists", http.MethodDelete, "/api/crews/"+created.ID+"/members/other", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	// An admin may, though.
	adminResp := h.as("boss", "admins", http.MethodDelete, "/api/crews/"+created.ID+"/members/other", "")
	if adminResp.StatusCode != http.StatusOK {
		t.Fatalf("admin remove status = %d, want 200", adminResp.StatusCode)
	}
}

func TestDeleteCrewIsOwnerOrAdminOnly(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))

	if resp := h.as("other", "cyclists", http.MethodDelete, "/api/crews/"+created.ID, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner delete status = %d, want 403", resp.StatusCode)
	}

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+created.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner delete status = %d, want 200", resp.StatusCode)
	}

	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+created.ID, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleting again status = %d, want 404", resp.StatusCode)
	}
}

func TestOnlyOwnerOrAdminCanSetAutoShare(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))
	if created.AutoShare {
		t.Fatalf("autoShare = true on create, want false")
	}

	if resp := h.as("other", "cyclists", http.MethodPatch, "/api/crews/"+created.ID, `{"autoShare":true}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner status = %d, want 403", resp.StatusCode)
	}

	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/crews/"+created.ID, `{"autoShare":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner status = %d, want 200", resp.StatusCode)
	}
	if updated := decodeCrew(t, resp); !updated.AutoShare {
		t.Errorf("autoShare = false after enabling, want true")
	}

	// An admin may too, and can turn it back off.
	adminResp := h.as("boss", "admins", http.MethodPatch, "/api/crews/"+created.ID, `{"autoShare":false}`)
	if adminResp.StatusCode != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", adminResp.StatusCode)
	}
	if updated := decodeCrew(t, adminResp); updated.AutoShare {
		t.Errorf("autoShare = true after admin disabled it, want false")
	}
}

func TestSetCrewMemberOwnerRequiresOwnerOrAdmin(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))
	h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)
	h.as("other", "cyclists", http.MethodPut, "/api/crews/"+created.ID+"/members/other", "")

	// A plain member (not yet an owner) may not promote themselves.
	if resp := h.as("other", "cyclists", http.MethodPatch, "/api/crews/"+created.ID+"/members/other/owner", `{"owner":true}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner status = %d, want 403", resp.StatusCode)
	}

	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/crews/"+created.ID+"/members/other/owner", `{"owner":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner status = %d, want 200", resp.StatusCode)
	}
	if promoted := decodeCrew(t, resp); !promoted.isOwner("other") {
		t.Errorf("owners = %v, want other included", promoted.Owners)
	}

	// An admin may also promote/demote.
	adminResp := h.as("boss", "admins", http.MethodPatch, "/api/crews/"+created.ID+"/members/other/owner", `{"owner":false}`)
	if adminResp.StatusCode != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", adminResp.StatusCode)
	}
	if demoted := decodeCrew(t, adminResp); demoted.isOwner("other") {
		t.Errorf("owners = %v, want other removed", demoted.Owners)
	}
}

func TestSetCrewMemberOwnerRejectsDemotingTheLastOwner(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))

	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/crews/"+created.ID+"/members/wilant/owner", `{"owner":false}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (cannot demote the last owner)", resp.StatusCode)
	}
}

// The whole point of multi-owner: a crew survives one owner's departure as
// long as another owner grant remains, and that co-owner can go on managing
// it — deleting a rider (purgeRiderData's RemoveRiderEverywhere) is one way
// an owner departs, but this test exercises the ordinary promote-then-leave
// path through the HTTP API rather than the purge path directly (see
// riderdelete_test.go for that one).
func TestSecondOwnerCanManageAfterFirstOwnerLeaves(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))
	h.as("wilant", "cyclists", http.MethodPost, "/api/crews/"+created.ID+"/members", `{"rider":"other"}`)
	h.as("other", "cyclists", http.MethodPut, "/api/crews/"+created.ID+"/members/other", "")
	h.as("wilant", "cyclists", http.MethodPatch, "/api/crews/"+created.ID+"/members/other/owner", `{"owner":true}`)

	// wilant leaves — now fine, since other is also an owner.
	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+created.ID+"/members/wilant", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("wilant leaving status = %d, want 200", resp.StatusCode)
	}

	// other, the remaining owner, can still manage the crew.
	resp := h.as("other", "cyclists", http.MethodPatch, "/api/crews/"+created.ID, `{"autoShare":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remaining owner status = %d, want 200", resp.StatusCode)
	}
}

// A crew whose only owner leaves (self-removal skips SetOwner's own
// last-owner guard, the same way a deleted rider's RemoveRiderEverywhere
// does — see crew.Store's own doc comment on that method) is left with zero
// owners, on purpose. It must not vanish, and an admin must still be able
// to manage it via canManageCrew's own override — the accepted outcome the
// user-deletion feature is built around.
func TestDeletingAnOwnerLeavesTheCrewIntact(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))

	if resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+created.ID+"/members/wilant", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("owner leaving status = %d, want 200", resp.StatusCode)
	}

	// The crew is still there, just with no owners left.
	list := h.as("boss", "admins", http.MethodGet, "/api/crews", "")
	var crews []crewDTOOut
	if err := json.NewDecoder(list.Body).Decode(&crews); err != nil {
		t.Fatal(err)
	}
	if len(crews) != 1 || len(crews[0].Owners) != 0 {
		t.Fatalf("crews = %+v, want the crew to survive with no owners", crews)
	}

	// A non-admin rider may not manage a crew nobody owns.
	if resp := h.as("random", "cyclists", http.MethodPatch, "/api/crews/"+created.ID, `{"autoShare":true}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403", resp.StatusCode)
	}

	// An admin still can, regardless.
	if resp := h.as("boss", "admins", http.MethodPatch, "/api/crews/"+created.ID, `{"autoShare":true}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", resp.StatusCode)
	}
}

func TestSetAutoShareOnNonexistentCrewIs404(t *testing.T) {
	h := newCrewHarness(t)
	resp := h.as("wilant", "cyclists", http.MethodPatch, "/api/crews/crew:does-not-exist", `{"autoShare":true}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestJoinNonexistentCrewIs404(t *testing.T) {
	h := newCrewHarness(t)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/crews/crew:does-not-exist/join", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// A viewer may look, not touch — crews are a rider-level feature.
func TestCrewEndpointsNeedRiderPermission(t *testing.T) {
	h := newCrewHarness(t)
	created := decodeCrew(t, h.as("wilant", "cyclists", http.MethodPost, "/api/crews", `{"name":"Family"}`))

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/crews", `{"name":"x"}`},
		{http.MethodGet, "/api/crews", ""},
		{http.MethodPost, "/api/crews/" + created.ID + "/join", ""},
		{http.MethodDelete, "/api/crews/" + created.ID, ""},
		{http.MethodPatch, "/api/crews/" + created.ID, `{"autoShare":true}`},
		{http.MethodPost, "/api/crews/" + created.ID + "/members", `{"rider":"other"}`},
		{http.MethodPut, "/api/crews/" + created.ID + "/members/wilant", ""},
	} {
		resp := h.as("guest", "guests", tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s status = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestCrewHandlersSurviveNoCrewStore(t *testing.T) {
	srv := &api.Server{Auth: noAuth(t)}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	resp, err := server.Client().Get(server.URL + "/api/crews")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
}
