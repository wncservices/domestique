package auth0mgmt

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeTenant is a minimal Auth0 Management API + Authentication API,
// genuinely serving both routes a real one does — no canned response,
// since a canned one would not exercise the client's own request-building
// (query params, path escaping, headers) the way a real mux does.
type fakeTenant struct {
	server *httptest.Server

	tokenCalls atomic.Int32

	roles       map[string]string   // name -> id
	roleMembers map[string][]string // role id -> user ids
	users       map[string]struct{ email, name string }
	userRoles   map[string][]string // user id -> role ids currently held
	lastSeen    map[string]struct{ createdAt, lastLogin, nickname string }
	blocked     map[string]bool // user id -> blocked

	createdUsers   []map[string]any
	invitedEmails  []string
	grantedRoles   map[string][]string
	revokedRoles   map[string][]string
	changePassBody []map[string]string
	deletedUsers   []string
	patchedUsers   []map[string]any // every PATCH body received, in order
}

func newFakeTenant(t *testing.T) *fakeTenant {
	t.Helper()
	f := &fakeTenant{
		roles:        map[string]string{"domestique-users": "role-gate", "cyclists": "role-rider", "domestique-admins": "role-admin"},
		roleMembers:  map[string][]string{},
		users:        map[string]struct{ email, name string }{},
		userRoles:    map[string][]string{},
		lastSeen:     map[string]struct{ createdAt, lastLogin, nickname string }{},
		blocked:      map[string]bool{},
		grantedRoles: map[string][]string{},
		revokedRoles: map[string][]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-token", "token_type": "Bearer", "expires_in": 86400,
		})
	})
	mux.HandleFunc("GET /api/v2/roles", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name_filter")
		id, ok := f.roles[name]
		if !ok {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{{"id": id, "name": name}})
	})
	mux.HandleFunc("GET /api/v2/roles/{id}/users", func(w http.ResponseWriter, r *http.Request) {
		// Deliberately no created_at/last_login here — the real endpoint
		// does not return them either (confirmed against Auth0's own
		// OpenAPI schema), which is the whole reason lastSeen exists.
		var out []map[string]string
		for _, uid := range f.roleMembers[r.PathValue("id")] {
			u := f.users[uid]
			out = append(out, map[string]string{"user_id": uid, "email": u.email, "name": u.name})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /api/v2/users", func(w http.ResponseWriter, r *http.Request) {
		// Genuinely parses the Lucene q= the client built, rather than
		// trusting it blindly — exercises the same OR-of-ids shape lastSeen
		// actually sends, the way a canned response would not.
		q := r.URL.Query().Get("q")
		var out []map[string]any
		for _, term := range strings.Split(q, " OR ") {
			uid := strings.TrimSuffix(strings.TrimPrefix(term, `user_id:"`), `"`)
			seen, ok := f.lastSeen[uid]
			if !ok {
				continue
			}
			out = append(out, map[string]any{
				"user_id": uid, "created_at": seen.createdAt, "last_login": seen.lastLogin,
				"nickname": seen.nickname, "blocked": f.blocked[uid],
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/v2/users", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.createdUsers = append(f.createdUsers, body)
		uid := fmt.Sprintf("auth0|new-%d", len(f.createdUsers))
		f.users[uid] = struct{ email, name string }{body["email"].(string), body["name"].(string)}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"user_id": uid, "email": body["email"].(string), "name": body["name"].(string)})
	})
	mux.HandleFunc("GET /api/v2/users/{id}/roles", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]string
		for _, rid := range f.userRoles[r.PathValue("id")] {
			out = append(out, map[string]string{"id": rid})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/v2/users/{id}/roles", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Roles []string `json:"roles"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		uid := r.PathValue("id")
		f.grantedRoles[uid] = append(f.grantedRoles[uid], body.Roles...)
		f.userRoles[uid] = append(f.userRoles[uid], body.Roles...)
		for _, rid := range body.Roles {
			f.roleMembers[rid] = append(f.roleMembers[rid], uid)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/v2/users/{id}/roles", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Roles []string `json:"roles"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		uid := r.PathValue("id")
		f.revokedRoles[uid] = append(f.revokedRoles[uid], body.Roles...)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v2/users-by-email", func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("email")
		var out []map[string]string
		for uid, u := range f.users {
			if u.email == email {
				out = append(out, map[string]string{"user_id": uid, "email": u.email, "name": u.name})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("PATCH /api/v2/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		uid := r.PathValue("id")
		f.patchedUsers = append(f.patchedUsers, body)
		if blocked, ok := body["blocked"].(bool); ok {
			f.blocked[uid] = blocked
		}
		if name, ok := body["name"].(string); ok {
			u := f.users[uid]
			u.name = name
			f.users[uid] = u
		}
		u := f.users[uid]
		_ = json.NewEncoder(w).Encode(map[string]string{"user_id": uid, "email": u.email, "name": u.name})
	})
	mux.HandleFunc("DELETE /api/v2/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		uid := r.PathValue("id")
		f.deletedUsers = append(f.deletedUsers, uid)
		delete(f.users, uid)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /dbconnections/change_password", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.changePassBody = append(f.changePassBody, body)
		f.invitedEmails = append(f.invitedEmails, body["email"])
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("We've just sent you an email to reset your password."))
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func newTestClient(t *testing.T, f *fakeTenant) *Client {
	t.Helper()
	// f.server.URL is already "http://127.0.0.1:port" — baseURL() uses a
	// Domain that already carries a scheme as-is, exactly so tests can do
	// this without needing a custom transport or TLS.
	return New(Config{
		Domain: f.server.URL, ClientID: "mgmt-client", ClientSecret: "mgmt-secret", SignInClientID: "signin-client",
	})
}

func TestListPeopleMergesRoleMembership(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"admin@example.com", "Admin Person"}
	f.users["u2"] = struct{ email, name string }{"rider@example.com", "Rider Person"}
	f.users["u3"] = struct{ email, name string }{"gateonly@example.com", "Gate Only"}
	f.roleMembers["role-gate"] = []string{"u1", "u2", "u3"}
	f.roleMembers["role-admin"] = []string{"u1"}
	f.roleMembers["role-rider"] = []string{"u2"}

	c := newTestClient(t, f)
	people, err := c.ListPeople(t.Context(), "domestique-users", "domestique-admins", "cyclists")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 3 {
		t.Fatalf("people = %+v, want 3", people)
	}

	byEmail := map[string][]string{}
	for _, p := range people {
		byEmail[p.Email] = p.Roles
	}
	if r := byEmail["admin@example.com"]; len(r) != 1 || r[0] != "domestique-admins" {
		t.Errorf("admin's roles = %v, want [domestique-admins]", r)
	}
	if r := byEmail["rider@example.com"]; len(r) != 1 || r[0] != "cyclists" {
		t.Errorf("rider's roles = %v, want [cyclists]", r)
	}
	if r := byEmail["gateonly@example.com"]; len(r) != 0 {
		t.Errorf("gate-only's roles = %v, want none", r)
	}
}

// The real bug this guards against: GET /api/v2/roles/{id}/users (what
// ListPeople's own role listing hits) never returns created_at/last_login
// at all, on a real tenant — so every person on the People page showed
// "never signed in" regardless of their actual history. lastSeen's separate
// search-endpoint lookup is what's supposed to fill that in.
func TestListPeopleFillsInSignInHistoryFromTheSearchEndpoint(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"rider@example.com", "Rider Person"}
	f.roleMembers["role-gate"] = []string{"u1"}
	f.lastSeen["u1"] = struct{ createdAt, lastLogin, nickname string }{"2026-01-01T00:00:00.000Z", "2026-08-10T12:30:00.000Z", ""}

	c := newTestClient(t, f)
	people, err := c.ListPeople(t.Context(), "domestique-users")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 {
		t.Fatalf("people = %+v, want 1", people)
	}
	if people[0].LastLogin.IsZero() {
		t.Error("LastLogin is zero, want it filled in from the search endpoint")
	}
	if people[0].CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want it filled in from the search endpoint")
	}
}

// Nickname rides the same search-endpoint lookup as created_at/last_login,
// for the same reason: the role-members endpoint ListPeople otherwise uses
// doesn't return it either. This is what internal/api/people.go's
// likelyRider falls back to when Name doesn't satisfy accounts.RiderPattern
// (a Google display name with a space in it, most concretely).
func TestListPeopleFillsInNicknameFromTheSearchEndpoint(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"rider@example.com", "Rider Person"}
	f.roleMembers["role-gate"] = []string{"u1"}
	f.lastSeen["u1"] = struct{ createdAt, lastLogin, nickname string }{"2026-01-01T00:00:00.000Z", "2026-08-10T12:30:00.000Z", "rider.person"}

	c := newTestClient(t, f)
	people, err := c.ListPeople(t.Context(), "domestique-users")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 || people[0].Nickname != "rider.person" {
		t.Fatalf("people = %+v, want one with Nickname = rider.person", people)
	}
}

// A gate member the search endpoint has no history for (a freshly created
// account that's never actually signed in) must not error the whole page —
// it should just show as never signed in, same as before this fix.
func TestListPeopleLeavesSignInHistoryZeroWhenTheSearchEndpointHasNone(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"new@example.com", "New Person"}
	f.roleMembers["role-gate"] = []string{"u1"}

	c := newTestClient(t, f)
	people, err := c.ListPeople(t.Context(), "domestique-users")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 || !people[0].LastLogin.IsZero() {
		t.Errorf("people = %+v, want the one person with a zero LastLogin", people)
	}
}

func TestInviteCreatesUserGrantsRolesAndSendsEmail(t *testing.T) {
	f := newFakeTenant(t)
	c := newTestClient(t, f)

	person, err := c.Invite(t.Context(), "new@example.com", "New Rider", []string{"domestique-users", "cyclists"})
	if err != nil {
		t.Fatal(err)
	}
	if person.Email != "new@example.com" {
		t.Errorf("Email = %q", person.Email)
	}
	if len(f.createdUsers) != 1 {
		t.Fatalf("created %d users, want 1", len(f.createdUsers))
	}
	created := f.createdUsers[0]
	if created["connection"] != "Username-Password-Authentication" {
		t.Errorf("connection = %v", created["connection"])
	}
	if v, _ := created["email_verified"].(bool); v {
		t.Error("email_verified = true, want false — the invite email is what verifies it")
	}
	if pw, _ := created["password"].(string); len(pw) < 16 {
		t.Errorf("password looks too short/absent: %q", pw)
	}

	if granted := f.grantedRoles[person.UserID]; len(granted) != 2 {
		t.Errorf("granted roles = %v, want both role ids", granted)
	}
}

// Invite itself never calls SendInviteEmail — that is a deliberate second
// step (see the package doc). This proves the two are actually independent.
func TestInviteDoesNotItselfSendTheEmail(t *testing.T) {
	f := newFakeTenant(t)
	c := newTestClient(t, f)

	if _, err := c.Invite(t.Context(), "new@example.com", "New Rider", []string{"domestique-users"}); err != nil {
		t.Fatal(err)
	}
	if len(f.changePassBody) != 0 {
		t.Errorf("change_password called %d times, want 0 — Invite must not send the email itself", len(f.changePassBody))
	}
}

func TestSendInviteEmailUsesTheSignInClientNotManagementCredentials(t *testing.T) {
	f := newFakeTenant(t)
	c := newTestClient(t, f)

	if err := c.SendInviteEmail(t.Context(), "rider@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(f.changePassBody) != 1 {
		t.Fatalf("change_password called %d times, want 1", len(f.changePassBody))
	}
	body := f.changePassBody[0]
	if body["client_id"] != "signin-client" {
		t.Errorf("client_id = %q, want the sign-in client, not the management one", body["client_id"])
	}
	if body["email"] != "rider@example.com" {
		t.Errorf("email = %q", body["email"])
	}
}

func TestSetRolesGrantsMissingAndRevokesUnwanted(t *testing.T) {
	f := newFakeTenant(t)
	f.userRoles["u1"] = []string{"role-rider"}
	c := newTestClient(t, f)

	if err := c.SetRoles(t.Context(), "u1", []string{"domestique-admins"}); err != nil {
		t.Fatal(err)
	}
	if granted := f.grantedRoles["u1"]; len(granted) != 1 || granted[0] != "role-admin" {
		t.Errorf("granted = %v, want [role-admin]", granted)
	}
	if revoked := f.revokedRoles["u1"]; len(revoked) != 1 || revoked[0] != "role-rider" {
		t.Errorf("revoked = %v, want [role-rider]", revoked)
	}
}

// Asking for exactly the roles already held must not grant or revoke
// anything — an admin re-saving the same selection should not be a write.
func TestSetRolesIsANoopWhenAlreadyCorrect(t *testing.T) {
	f := newFakeTenant(t)
	f.userRoles["u1"] = []string{"role-rider"}
	c := newTestClient(t, f)

	if err := c.SetRoles(t.Context(), "u1", []string{"cyclists"}); err != nil {
		t.Fatal(err)
	}
	if len(f.grantedRoles["u1"]) != 0 || len(f.revokedRoles["u1"]) != 0 {
		t.Errorf("granted=%v revoked=%v, want neither", f.grantedRoles["u1"], f.revokedRoles["u1"])
	}
}

func TestAccessTokenIsCachedAcrossCalls(t *testing.T) {
	f := newFakeTenant(t)
	f.roleMembers["role-gate"] = nil
	c := newTestClient(t, f)

	if _, err := c.ListPeople(t.Context(), "domestique-users"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListPeople(t.Context(), "domestique-users"); err != nil {
		t.Fatal(err)
	}
	if got := f.tokenCalls.Load(); got != 1 {
		t.Errorf("token fetched %d times, want exactly 1 (cached across both calls)", got)
	}
}

func TestFindByEmailReturnsEveryMatchingIdentity(t *testing.T) {
	f := newFakeTenant(t)
	f.users["auth0|1"] = struct{ email, name string }{"rider@example.com", "DB Rider"}
	f.users["google-oauth2|2"] = struct{ email, name string }{"rider@example.com", "Google Rider"}
	f.users["auth0|3"] = struct{ email, name string }{"someone-else@example.com", "Someone Else"}
	c := newTestClient(t, f)

	people, err := c.FindByEmail(t.Context(), "rider@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 2 {
		t.Fatalf("people = %+v, want the 2 identities sharing that email, not the unrelated third", people)
	}
}

func TestFindByEmailReturnsNoneForAnUnknownAddress(t *testing.T) {
	f := newFakeTenant(t)
	c := newTestClient(t, f)

	people, err := c.FindByEmail(t.Context(), "nobody@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 0 {
		t.Errorf("people = %+v, want none", people)
	}
}

func TestAPIErrorSurfacesTheMessage(t *testing.T) {
	f := newFakeTenant(t)
	// Overwrite the create-user route with one that fails, the way Auth0
	// itself would for a duplicate email.
	mux := http.NewServeMux()
	f.server.Config.Handler = mux
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 86400})
	})
	mux.HandleFunc("POST /api/v2/users", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"message": "The user already exists.", "errorCode": "auth0_idp_error",
		})
	})

	c := newTestClient(t, f)
	_, err := c.Invite(t.Context(), "dupe@example.com", "Dupe", nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v, want it to name Auth0's own message", err)
	}
}

func TestSetBlockedTrue(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"rider@example.com", "Rider Person"}
	c := newTestClient(t, f)

	if err := c.SetBlocked(t.Context(), "u1", true); err != nil {
		t.Fatal(err)
	}
	if !f.blocked["u1"] {
		t.Error("blocked = false, want true after SetBlocked(true)")
	}
	if len(f.patchedUsers) != 1 || f.patchedUsers[0]["blocked"] != true {
		t.Errorf("patched bodies = %+v, want one carrying blocked:true", f.patchedUsers)
	}
}

func TestSetBlockedFalseUnblocks(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"rider@example.com", "Rider Person"}
	f.blocked["u1"] = true
	c := newTestClient(t, f)

	if err := c.SetBlocked(t.Context(), "u1", false); err != nil {
		t.Fatal(err)
	}
	if f.blocked["u1"] {
		t.Error("blocked = true, want false after SetBlocked(false)")
	}
}

// ListPeople's own blocked mapping rides the same search-endpoint lookup as
// CreatedAt/LastLogin/Nickname (see lastSeen) — this is what lets the People
// page render a Block/Unblock toggle reflecting current state, rather than
// firing the action blind.
func TestListPeopleReportsBlockedStatus(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"rider@example.com", "Rider Person"}
	f.roleMembers["role-gate"] = []string{"u1"}
	f.lastSeen["u1"] = struct{ createdAt, lastLogin, nickname string }{"2026-01-01T00:00:00.000Z", "", ""}
	f.blocked["u1"] = true
	c := newTestClient(t, f)

	people, err := c.ListPeople(t.Context(), "domestique-users")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 || !people[0].Blocked {
		t.Fatalf("people = %+v, want the one person reported as blocked", people)
	}
}

func TestDeleteUserRemovesTheIdentity(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"rider@example.com", "Rider Person"}
	c := newTestClient(t, f)

	if err := c.DeleteUser(t.Context(), "u1"); err != nil {
		t.Fatal(err)
	}
	if len(f.deletedUsers) != 1 || f.deletedUsers[0] != "u1" {
		t.Errorf("deletedUsers = %v, want [u1]", f.deletedUsers)
	}
	if _, err := c.FindByEmail(t.Context(), "rider@example.com"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteUserSurfacesAnAPIError(t *testing.T) {
	f := newFakeTenant(t)
	mux := http.NewServeMux()
	f.server.Config.Handler = mux
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 86400})
	})
	mux.HandleFunc("DELETE /api/v2/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "The user does not exist.", "errorCode": "inexistent_user"})
	})

	c := newTestClient(t, f)
	err := c.DeleteUser(t.Context(), "missing")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err = %v, want it to name Auth0's own message", err)
	}
}
