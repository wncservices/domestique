package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
	"github.com/wncservices/domestique/apps/api/internal/blocklist"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

// fakePeople is PeopleConnector for tests — no real Auth0 tenant, matching
// every other connector fake in this package.
type fakePeople struct {
	people []auth0mgmt.Person

	invited      []auth0mgmt.Person
	invitedRoles map[string][]string // by email
	invitedEmail []string            // SendInviteEmail's own calls
	setRoles     map[string][]string // by user id, last call wins

	findByEmail map[string][]auth0mgmt.Person // by email — empty/absent means no existing identity

	updatedName map[string]string // by user id, last call wins

	enrollments        map[string][]auth0mgmt.Enrollment // by user id
	deletedEnrollments []string                          // DeleteEnrollment's own calls, in order

	blockedUsers map[string]bool // by user id, last call wins
	deletedUsers []string        // DeleteUser's own calls, in order

	listErr       error
	inviteErr     error
	emailErr      error
	rolesErr      error
	findErr       error
	updateNameErr error
	enrollListErr error
	ticketErr     error
	deleteErr     error
	setBlockedErr error
	deleteUserErr error
}

func (f *fakePeople) ListPeople(context.Context, string, ...string) ([]auth0mgmt.Person, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.people, nil
}

func (f *fakePeople) Invite(_ context.Context, email, name string, roleNames []string) (auth0mgmt.Person, error) {
	if f.inviteErr != nil {
		return auth0mgmt.Person{}, f.inviteErr
	}
	if f.invitedRoles == nil {
		f.invitedRoles = map[string][]string{}
	}
	person := auth0mgmt.Person{UserID: "auth0|new-" + email, Email: email, Name: name}
	f.invited = append(f.invited, person)
	f.invitedRoles[email] = roleNames
	return person, nil
}

func (f *fakePeople) SetRoles(_ context.Context, userID string, roleNames []string) error {
	if f.rolesErr != nil {
		return f.rolesErr
	}
	if f.setRoles == nil {
		f.setRoles = map[string][]string{}
	}
	f.setRoles[userID] = roleNames
	return nil
}

func (f *fakePeople) SendInviteEmail(_ context.Context, email string) error {
	if f.emailErr != nil {
		return f.emailErr
	}
	f.invitedEmail = append(f.invitedEmail, email)
	return nil
}

func (f *fakePeople) FindByEmail(_ context.Context, email string) ([]auth0mgmt.Person, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.findByEmail[email], nil
}

func (f *fakePeople) UpdateName(_ context.Context, userID, name string) (auth0mgmt.Person, error) {
	if f.updateNameErr != nil {
		return auth0mgmt.Person{}, f.updateNameErr
	}
	if f.updatedName == nil {
		f.updatedName = map[string]string{}
	}
	f.updatedName[userID] = name
	return auth0mgmt.Person{UserID: userID, Name: name}, nil
}

func (f *fakePeople) ListEnrollments(_ context.Context, userID string) ([]auth0mgmt.Enrollment, error) {
	if f.enrollListErr != nil {
		return nil, f.enrollListErr
	}
	return f.enrollments[userID], nil
}

func (f *fakePeople) CreateGuardianEnrollmentTicket(_ context.Context, userID string) (string, error) {
	if f.ticketErr != nil {
		return "", f.ticketErr
	}
	return "https://fake-issuer.example/guardian/ticket/" + userID, nil
}

func (f *fakePeople) DeleteEnrollment(_ context.Context, enrollmentID string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedEnrollments = append(f.deletedEnrollments, enrollmentID)
	return nil
}

func (f *fakePeople) SetBlocked(_ context.Context, userID string, blocked bool) error {
	if f.setBlockedErr != nil {
		return f.setBlockedErr
	}
	if f.blockedUsers == nil {
		f.blockedUsers = map[string]bool{}
	}
	f.blockedUsers[userID] = blocked
	return nil
}

func (f *fakePeople) DeleteUser(_ context.Context, userID string) error {
	if f.deleteUserErr != nil {
		return f.deleteUserErr
	}
	f.deletedUsers = append(f.deletedUsers, userID)
	return nil
}

type peopleHarness struct {
	t      *testing.T
	client *http.Client
	base   string
	people *fakePeople
	source *source.DB
}

// newPeopleHarness builds a working People-page server. opts can wire
// additional Server fields (Accounts, Links, Crew, Schedule, Blocklist) —
// purgeRiderData nil-checks every one of them, so a test that only cares
// about the Auth0 side of block/delete does not need to set any of these up.
func newPeopleHarness(t *testing.T, people *fakePeople, opts ...func(*api.Server)) *peopleHarness {
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

	authenticator, err := auth.New(auth.Config{
		Mode:          auth.ModeProxy,
		RequiredGroup: "domestique-users",
		Roles: auth.RoleMapping{
			Admin: []string{"domestique-admins"},
			Rider: []string{"cyclists"},
		},
		DefaultRole: "viewer",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := &api.Server{Source: db, Store: store, Auth: authenticator}
	// Assigned only when non-nil: srv.People is the PeopleConnector
	// interface, and assigning a nil *fakePeople to it would make the
	// interface itself non-nil (a typed nil), defeating the "no connector
	// configured" tests below.
	if people != nil {
		srv.People = people
	}
	for _, opt := range opts {
		opt(srv)
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	return &peopleHarness{t: t, client: server.Client(), base: server.URL, people: people, source: db}
}

func (h *peopleHarness) as(user, groups, method, path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, strings.NewReader(body))
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

func TestPeopleListRequiresAdmin(t *testing.T) {
	h := newPeopleHarness(t, &fakePeople{})

	resp := h.as("wilant", "domestique-users,cyclists", http.MethodGet, "/api/people", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rider status = %d, want 403", resp.StatusCode)
	}

	resp = h.as("wilant", "domestique-users,domestique-admins", http.MethodGet, "/api/people", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("admin status = %d, want 200", resp.StatusCode)
	}
}

func TestPeopleListResolvesRoleFromAuth0RoleNames(t *testing.T) {
	fake := &fakePeople{people: []auth0mgmt.Person{
		{UserID: "u1", Email: "admin@example.com", Roles: []string{"domestique-users", "domestique-admins"}},
		{UserID: "u2", Email: "rider@example.com", Roles: []string{"domestique-users", "cyclists"}},
		{UserID: "u3", Email: "gateonly@example.com", Roles: []string{"domestique-users"}},
	}}
	h := newPeopleHarness(t, fake)

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodGet, "/api/people", "")
	var out []struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	byEmail := map[string]string{}
	for _, p := range out {
		byEmail[p.Email] = p.Role
	}
	if byEmail["admin@example.com"] != "admin" {
		t.Errorf("admin's role = %q", byEmail["admin@example.com"])
	}
	if byEmail["rider@example.com"] != "rider" {
		t.Errorf("rider's role = %q", byEmail["rider@example.com"])
	}
	// Gate-only membership resolves to the configured default_role, exactly
	// what Identify itself would compute at sign-in — not a special case
	// this page invents on its own.
	if byEmail["gateonly@example.com"] != "viewer" {
		t.Errorf("gate-only's role = %q, want viewer", byEmail["gateonly@example.com"])
	}
}

// likelyRider closes a real gap: a rider who has been invited and signed in
// but never uploaded a route, linked a provider account, or created a crew
// has no other way to show up in the crew add-member picker's suggestions
// (CrewsPage.vue's knownRiders) — found live. This exercises the priority
// order (name, then nickname, then the Auth0 user id) an admin actually
// sees through the wire response, not just the unexported helper in
// isolation, since this package's tests are external (package api_test)
// and cannot call it directly.
func TestPeopleListGuessesALikelyRiderIdentity(t *testing.T) {
	fake := &fakePeople{people: []auth0mgmt.Person{
		// A legal name is used as-is — the common case, an admin-typed
		// invite name or a database-connection username.
		{UserID: "auth0|1", Email: "a@example.com", Name: "pieter.hollevoet", Roles: []string{"domestique-users"}},
		// A display name with a space fails RiderPattern (see
		// accounts.RiderPattern), falling through to nickname — the Google
		// sign-in case this was actually built for.
		{UserID: "google-oauth2|2", Email: "b@example.com", Name: "Pieter Hollevoet", Nickname: "pieter.h", Roles: []string{"domestique-users"}},
		// Neither name nor nickname is usable: falls all the way through
		// to the Auth0 user id itself, which always satisfies the pattern.
		{UserID: "google-oauth2|3", Email: "c@example.com", Name: "Pieter Hollevoet", Roles: []string{"domestique-users"}},
	}}
	h := newPeopleHarness(t, fake)

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodGet, "/api/people", "")
	var out []struct {
		Email       string `json:"email"`
		LikelyRider string `json:"likelyRider"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	byEmail := map[string]string{}
	for _, p := range out {
		byEmail[p.Email] = p.LikelyRider
	}
	if byEmail["a@example.com"] != "pieter.hollevoet" {
		t.Errorf("legal name: likelyRider = %q, want pieter.hollevoet", byEmail["a@example.com"])
	}
	if byEmail["b@example.com"] != "pieter.h" {
		t.Errorf("illegal name, legal nickname: likelyRider = %q, want pieter.h", byEmail["b@example.com"])
	}
	if byEmail["c@example.com"] != "google-oauth2|3" {
		t.Errorf("neither usable: likelyRider = %q, want the Auth0 user id", byEmail["c@example.com"])
	}
}

func TestPeopleInviteGrantsGateAndPermissionRoleThenSendsEmail(t *testing.T) {
	fake := &fakePeople{}
	h := newPeopleHarness(t, fake)

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPost, "/api/people",
		`{"email":"New@Example.com","name":"New Rider","role":"rider"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	// Lower-cased and trimmed the same way every other rider-facing email
	// in this codebase is normalized.
	roles := fake.invitedRoles["new@example.com"]
	if len(roles) != 2 || roles[0] != "domestique-users" || roles[1] != "cyclists" {
		t.Errorf("invited roles = %v, want [domestique-users cyclists]", roles)
	}
	if len(fake.invitedEmail) != 1 || fake.invitedEmail[0] != "new@example.com" {
		t.Errorf("invite email sent to %v, want [new@example.com]", fake.invitedEmail)
	}
}

// Inviting as "viewer" grants the gate role and nothing else — there is no
// Auth0 role for "viewer" in this deployment, it is the absence of the
// other two.
func TestPeopleInviteAsViewerGrantsOnlyTheGateRole(t *testing.T) {
	fake := &fakePeople{}
	h := newPeopleHarness(t, fake)

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPost, "/api/people",
		`{"email":"viewer@example.com","role":"viewer"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	roles := fake.invitedRoles["viewer@example.com"]
	if len(roles) != 1 || roles[0] != "domestique-users" {
		t.Errorf("invited roles = %v, want [domestique-users]", roles)
	}
}

// The shape a Google sign-in actually produces: an Auth0 identity exists
// before anyone told this app about that person. Inviting that email must
// grant access to the identity that already exists, not create a second one
// alongside it.
func TestPeopleInviteGrantsAccessToAnExistingIdentityInsteadOfCreatingASecond(t *testing.T) {
	fake := &fakePeople{findByEmail: map[string][]auth0mgmt.Person{
		"already-signed-in@example.com": {{UserID: "google-oauth2|123", Email: "already-signed-in@example.com", Name: "Tiebe"}},
	}}
	h := newPeopleHarness(t, fake)

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPost, "/api/people",
		`{"email":"Already-Signed-In@Example.com","role":"rider"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (granted, not created)", resp.StatusCode)
	}
	if len(fake.invited) != 0 {
		t.Errorf("invited = %v, want no new account created", fake.invited)
	}
	if len(fake.invitedEmail) != 0 {
		t.Errorf("invite email sent to %v, want none — they can already sign in", fake.invitedEmail)
	}
	roles := fake.setRoles["google-oauth2|123"]
	if len(roles) != 2 || roles[0] != "domestique-users" || roles[1] != "cyclists" {
		t.Errorf("granted roles = %v, want [domestique-users cyclists] on the existing identity", roles)
	}

	var out struct {
		Granted bool `json:"granted"`
		Person  struct {
			ID string `json:"id"`
		} `json:"person"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Granted {
		t.Error("granted = false, want true")
	}
	if out.Person.ID != "google-oauth2|123" {
		t.Errorf("person id = %q, want the existing identity's id", out.Person.ID)
	}
}

// Two separate identities sharing an email (a Google one and a database one,
// say) is exactly the case FindByEmail can't resolve on its own — surfaced
// to the admin instead of guessed at.
func TestPeopleInviteRejectsAmbiguousMultipleExistingIdentities(t *testing.T) {
	fake := &fakePeople{findByEmail: map[string][]auth0mgmt.Person{
		"dup@example.com": {
			{UserID: "google-oauth2|1", Email: "dup@example.com"},
			{UserID: "auth0|2", Email: "dup@example.com"},
		},
	}}
	h := newPeopleHarness(t, fake)

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPost, "/api/people",
		`{"email":"dup@example.com","role":"rider"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", resp.StatusCode)
	}
	if len(fake.invited) != 0 || len(fake.setRoles) != 0 {
		t.Errorf("invited=%v setRoles=%v, want neither touched on an ambiguous match", fake.invited, fake.setRoles)
	}
}

func TestPeopleInviteRejectsAnUnknownRole(t *testing.T) {
	h := newPeopleHarness(t, &fakePeople{})

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPost, "/api/people",
		`{"email":"x@example.com","role":"superuser"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// The account and its access already exist by the time the email step can
// fail — that must not look like the whole invite failed, or a retry would
// try (and fail) to create the same account again.
func TestPeopleInviteSurvivesAnEmailFailureAfterCreating(t *testing.T) {
	fake := &fakePeople{emailErr: assertErr}
	h := newPeopleHarness(t, fake)

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPost, "/api/people",
		`{"email":"x@example.com","role":"rider"}`)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (created, email failed)", resp.StatusCode)
	}
	if len(fake.invited) != 1 {
		t.Errorf("invited = %v, want the account still created", fake.invited)
	}
}

func TestPeopleSetRoleRequiresAdmin(t *testing.T) {
	h := newPeopleHarness(t, &fakePeople{})

	resp := h.as("wilant", "domestique-users,cyclists", http.MethodPut, "/api/people/u1/role", `{"role":"admin"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestPeopleSetRoleChangesRoles(t *testing.T) {
	fake := &fakePeople{}
	h := newPeopleHarness(t, fake)

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPut, "/api/people/u1/role", `{"role":"admin"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	roles := fake.setRoles["u1"]
	if len(roles) != 2 || roles[1] != "domestique-admins" {
		t.Errorf("set roles = %v, want [domestique-users domestique-admins]", roles)
	}
}

func TestPeopleEndpointsWithoutAConnectorAreUnavailable(t *testing.T) {
	h := newPeopleHarness(t, nil)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/people", ""},
		{http.MethodPost, "/api/people", `{"email":"x@example.com","role":"rider"}`},
		{http.MethodPut, "/api/people/u1/role", `{"role":"admin"}`},
		{http.MethodPut, "/api/people/u1/blocked", `{"blocked":true,"email":"x@example.com"}`},
		{http.MethodDelete, "/api/people/u1", ""},
	} {
		resp := h.as("wilant", "domestique-users,domestique-admins", tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Errorf("%s %s status = %d, want 412", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func newTestBlocklist(t *testing.T) *blocklist.Store {
	t.Helper()
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "blocklist.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bl, err := blocklist.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	return bl
}

func TestPeopleBlockRequiresAdmin(t *testing.T) {
	h := newPeopleHarness(t, &fakePeople{})

	resp := h.as("wilant", "domestique-users,cyclists", http.MethodPut, "/api/people/u1/blocked",
		`{"blocked":true,"email":"rider@example.com"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// Blocking writes to both Auth0 (the identity an admin is actually looking
// at) and the local blocklist (checked at every OIDC callback, regardless
// of which identity signs in next) — see internal/blocklist's own package
// doc for why neither alone is enough.
func TestPeopleBlockSetsAuth0AndLocalBlocklist(t *testing.T) {
	fake := &fakePeople{}
	bl := newTestBlocklist(t)
	h := newPeopleHarness(t, fake, func(s *api.Server) { s.Blocklist = bl })

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPut, "/api/people/u1/blocked",
		`{"blocked":true,"email":"Rider@Example.com","reason":"spamming crews"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !fake.blockedUsers["u1"] {
		t.Error("Auth0 blocked flag was not set")
	}
	blocked, err := bl.IsBlocked(t.Context(), "rider@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("local blocklist was not updated")
	}
}

func TestPeopleUnblockClearsBothAuth0AndLocalBlocklist(t *testing.T) {
	fake := &fakePeople{}
	bl := newTestBlocklist(t)
	if err := bl.Block(t.Context(), "rider@example.com", "admin", ""); err != nil {
		t.Fatal(err)
	}
	fake.blockedUsers = map[string]bool{"u1": true}
	h := newPeopleHarness(t, fake, func(s *api.Server) { s.Blocklist = bl })

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPut, "/api/people/u1/blocked",
		`{"blocked":false,"email":"rider@example.com"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if fake.blockedUsers["u1"] {
		t.Error("Auth0 blocked flag is still set")
	}
	blocked, err := bl.IsBlocked(t.Context(), "rider@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("local blocklist still reports this email as blocked")
	}
}

func TestPeopleBlockRequiresAnEmail(t *testing.T) {
	h := newPeopleHarness(t, &fakePeople{})
	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPut, "/api/people/u1/blocked", `{"blocked":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPeopleDeleteRequiresAdmin(t *testing.T) {
	h := newPeopleHarness(t, &fakePeople{})

	resp := h.as("wilant", "domestique-users,cyclists", http.MethodDelete, "/api/people/u1", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// Purge-before-identity: the local data for the rider named in ?rider= is
// removed first, and only then is the Auth0 identity itself deleted — the
// same ordering handleDeleteMe uses, so a failure never leaves an identity
// gone with local data still attached to it.
func TestPeopleDeletePurgesLocalDataThenAuth0Identity(t *testing.T) {
	fake := &fakePeople{}
	h := newPeopleHarness(t, fake)

	if _, err := h.source.Create(t.Context(), source.CreateRequest{
		Name: "Departed Rider's Loop", UploadedBy: "gone",
		GPX: []byte(`<gpx version="1.1"><trk><trkseg><trkpt lat="50" lon="3"/><trkpt lat="50.001" lon="3.001"/></trkseg></trk></gpx>`),
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodDelete, "/api/people/auth0|gone?rider=gone", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(fake.deletedUsers) != 1 || fake.deletedUsers[0] != "auth0|gone" {
		t.Errorf("deletedUsers = %v, want [auth0|gone]", fake.deletedUsers)
	}

	routes, _, err := h.source.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Errorf("routes = %+v, want the departed rider's route purged", routes)
	}
}

// No ?rider= means the admin chose not to (or could not) confirm a local
// rider identity to purge — the Auth0 identity is still deleted, but no
// local data is touched, rather than guessing.
func TestPeopleDeleteWithoutRiderOnlyDeletesTheIdentity(t *testing.T) {
	fake := &fakePeople{}
	h := newPeopleHarness(t, fake)

	if _, err := h.source.Create(t.Context(), source.CreateRequest{
		Name: "Someone's Loop", UploadedBy: "someone",
		GPX: []byte(`<gpx version="1.1"><trk><trkseg><trkpt lat="50" lon="3"/><trkpt lat="50.001" lon="3.001"/></trkseg></trk></gpx>`),
	}); err != nil {
		t.Fatalf("seed route: %v", err)
	}

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodDelete, "/api/people/auth0|someone", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(fake.deletedUsers) != 1 {
		t.Errorf("deletedUsers = %v, want the identity still deleted", fake.deletedUsers)
	}
	routes, _, err := h.source.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Errorf("routes = %+v, want the route untouched (no rider named)", routes)
	}
}

// An Auth0 failure after the purge already succeeded is surfaced, not
// hidden — the caller needs to know the identity itself is still there.
func TestPeopleDeleteSurfacesAnAuth0Failure(t *testing.T) {
	fake := &fakePeople{deleteUserErr: assertErr}
	h := newPeopleHarness(t, fake)

	resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodDelete, "/api/people/auth0|gone?rider=gone", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

// assertErr is a stand-in error for tests that only care that Invite's
// email step failed, not what the failure was.
var assertErr = &testError{"auth0mgmt: sending the invite email failed"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
