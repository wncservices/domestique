package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// patch is ssoHarness's own client/base/cookies, plus a body — the sso
// harness only ever needed GET and a bodyless POST until now.
func (h *ssoHarness) patch(path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPatch, h.base+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (h *ssoHarness) delete(path string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodDelete, h.base+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func jsonBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMeReportsWhetherTheProfileCardHasAnythingToOffer(t *testing.T) {
	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	me := jsonBody(t, h.get("/api/me"))
	if me["canEditName"] != true {
		t.Errorf("canEditName = %v, want true", me["canEditName"])
	}
	if me["canChangePassword"] != true {
		t.Errorf("canChangePassword = %v, want true (a database-connection sub)", me["canChangePassword"])
	}
}

// A People-less deployment (no Management API credentials) has nothing this
// card can act on — same "not available" degradation Komoot/Garmin use for
// their own optional credentials, not a 500.
func TestMeReportsNoProfileEditingWithoutPeopleConfigured(t *testing.T) {
	h := newSSOHarness(t)
	h.login([]string{"cyclists"})

	me := jsonBody(t, h.get("/api/me"))
	if me["canEditName"] != false || me["canChangePassword"] != false {
		t.Errorf("me = %v, want both false with no People connector", me)
	}
}

// A Google-linked identity has no password on this app's side — the sub
// prefix says so before any Management API call is even attempted.
func TestMeReportsNoPasswordChangeForAGoogleIdentity(t *testing.T) {
	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com", "sub": "google-oauth2|123",
	})

	me := jsonBody(t, h.get("/api/me"))
	if me["canEditName"] != true {
		t.Errorf("canEditName = %v, want true (name is provider-agnostic)", me["canEditName"])
	}
	if me["canChangePassword"] != false {
		t.Errorf("canChangePassword = %v, want false for a google-oauth2 identity", me["canChangePassword"])
	}
}

func TestUpdateMeChangesNameAndTheCurrentSession(t *testing.T) {
	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	resp := h.patch("/api/me", `{"name":"Wilant N."}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := jsonBody(t, resp)["name"]; got != "Wilant N." {
		t.Errorf("response name = %v", got)
	}
	if got := fake.updatedName["auth0|64f2a1b2c3d4e5f6"]; got != "Wilant N." {
		t.Errorf("auth0mgmt.UpdateName called with %q, want the new name", got)
	}

	// The session was patched in place — a fresh /api/me on the same
	// cookie reflects the new name without a re-login.
	me := jsonBody(t, h.get("/api/me"))
	if me["name"] != "Wilant N." {
		t.Errorf("me.name after update = %v, want it to have picked up the change", me["name"])
	}
}

func TestUpdateMeRejectsAnEmptyName(t *testing.T) {
	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	resp := h.patch("/api/me", `{"name":"   "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if len(fake.updatedName) != 0 {
		t.Errorf("UpdateName should not have been called: %v", fake.updatedName)
	}
}

// The security-critical case: identityFromToken falls back to name (then
// nickname, then sub) as the rider string whenever an issuer sends no
// preferred_username — true of this deployment's Auth0 database connection.
// Without this check, a rider could PATCH their own name to "someone-else"
// and, on their next login, become that rider outright — inheriting their
// routes, their linked accounts, and anything stored against that identity.
func TestUpdateMeRejectsANameThatCollidesWithAnotherRider(t *testing.T) {
	db, err := source.OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	accountStore, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountStore.Link(t.Context(), model.ProviderGarmin, "someone-else", "their head unit"); err != nil {
		t.Fatal(err)
	}

	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake; s.Accounts = accountStore })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	// Case-insensitive on purpose — every rider comparison in this codebase
	// normalizes the same way, and a collision hiding behind different
	// casing would be exactly as exploitable as an exact match.
	resp := h.patch("/api/me", `{"name":"Someone-Else"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if len(fake.updatedName) != 0 {
		t.Errorf("UpdateName should not have been called: %v", fake.updatedName)
	}
}

// Renaming to a case-variant of one's own current identity is not a
// collision — self is always excluded from the check.
func TestUpdateMeAllowsRenamingToACaseVariantOfOwnIdentity(t *testing.T) {
	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	resp := h.patch("/api/me", `{"name":"Wilant"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, jsonBody(t, resp))
	}
}

func TestUpdateMeFailsClosedWithoutPeopleConfigured(t *testing.T) {
	h := newSSOHarness(t)
	h.login([]string{"cyclists"})

	resp := h.patch("/api/me", `{"name":"New Name"}`)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", resp.StatusCode)
	}
}

func TestSelfPasswordResetSendsAuth0sResetEmail(t *testing.T) {
	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	resp := h.post("/api/me/password-reset")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(fake.invitedEmail) != 1 || fake.invitedEmail[0] != "wilant@example.com" {
		t.Errorf("SendInviteEmail calls = %v, want [wilant@example.com]", fake.invitedEmail)
	}
}

// The whole point of gating on the sub prefix: a Google-linked rider must
// never reach SendInviteEmail — Auth0 would happily "reset" a password that
// rider never had, and sending that email would only confuse them.
func TestSelfPasswordResetRefusesAGoogleIdentity(t *testing.T) {
	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com", "sub": "google-oauth2|123",
	})

	resp := h.post("/api/me/password-reset")
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", resp.StatusCode)
	}
	if len(fake.invitedEmail) != 0 {
		t.Errorf("SendInviteEmail should not have been called: %v", fake.invitedEmail)
	}
}

func TestSelfPasswordResetRequiresAnAuthenticatedRider(t *testing.T) {
	h := newSSOHarness(t)
	resp := h.post("/api/me/password-reset")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// Both password-reset and MFA enrollment relay to Auth0 on the rider's own
// behalf — without a limit, this app is an unlimited, authenticated-only
// proxy for spamming a rider's own inbox or burning Auth0's send quota.
// They share one AuthActionLimiter budget per rider, not one each, so a
// rider hitting the limit on one cannot dodge it by switching to the other.
func TestAuthActionsAreRateLimitedPerRider(t *testing.T) {
	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) {
		s.People = fake
		s.AuthActionLimiter = ratelimit.New(1, time.Hour)
	})
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	if resp := h.post("/api/me/password-reset"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first password-reset: status = %d, want 200", resp.StatusCode)
	}

	// The budget is already spent — MFA enrollment must be refused too,
	// not just a second password-reset.
	resp := h.post("/api/me/mfa/enroll")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("mfa enroll after the password-reset budget was spent: status = %d, want 429", resp.StatusCode)
	}
	if len(fake.invitedEmail) != 1 {
		t.Errorf("SendInviteEmail calls = %v, want exactly the first one", fake.invitedEmail)
	}
}

func jsonArray(t *testing.T, resp *http.Response) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestListMFAReturnsTheRidersOwnEnrollments(t *testing.T) {
	fake := &fakePeople{enrollments: map[string][]auth0mgmt.Enrollment{
		"auth0|64f2a1b2c3d4e5f6": {{ID: "totp|abc", Status: "confirmed", Type: "totp"}},
	}}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	out := jsonArray(t, h.get("/api/me/mfa"))
	if len(out) != 1 || out[0]["id"] != "totp|abc" || out[0]["type"] != "totp" {
		t.Errorf("enrollments = %v", out)
	}
}

func TestEnrollMFAReturnsAGuardianTicketURL(t *testing.T) {
	fake := &fakePeople{}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	resp := h.post("/api/me/mfa/enroll")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	url, _ := jsonBody(t, resp)["ticketUrl"].(string)
	if url == "" {
		t.Error("no ticketUrl in the response")
	}
}

func TestRemoveMFADeletesAnOwnedEnrollment(t *testing.T) {
	fake := &fakePeople{enrollments: map[string][]auth0mgmt.Enrollment{
		"auth0|64f2a1b2c3d4e5f6": {{ID: "totp|abc", Status: "confirmed", Type: "totp"}},
	}}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	resp := h.delete("/api/me/mfa/totp%7Cabc")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(fake.deletedEnrollments) != 1 || fake.deletedEnrollments[0] != "totp|abc" {
		t.Errorf("DeleteEnrollment calls = %v, want [totp|abc]", fake.deletedEnrollments)
	}
}

// The security-critical case: Guardian's own delete endpoint is keyed by
// enrollment id alone with no user scoping, so the handler itself must
// refuse to delete an enrollment it did not first confirm belongs to the
// caller — otherwise any signed-in rider could strip a stranger's MFA by
// guessing or having ever seen their enrollment id.
func TestRemoveMFARefusesAnEnrollmentBelongingToSomeoneElse(t *testing.T) {
	fake := &fakePeople{enrollments: map[string][]auth0mgmt.Enrollment{
		"auth0|64f2a1b2c3d4e5f6": {{ID: "totp|mine", Status: "confirmed", Type: "totp"}},
		// Someone else's enrollment, never returned by ListEnrollments for
		// this rider's own sub — the handler must not just trust the id in
		// the URL.
	}}
	h := newSSOHarness(t, func(s *api.Server) { s.People = fake })
	h.loginWithUser([]string{"cyclists"}, map[string]any{
		"preferred_username": "wilant", "email": "wilant@example.com",
	})

	resp := h.delete("/api/me/mfa/totp%7Csomeone-elses")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if len(fake.deletedEnrollments) != 0 {
		t.Errorf("DeleteEnrollment should not have been called: %v", fake.deletedEnrollments)
	}
}

func TestMFAEndpointsFailClosedWithoutPeopleConfigured(t *testing.T) {
	h := newSSOHarness(t)
	h.login([]string{"cyclists"})

	if resp := h.get("/api/me/mfa"); resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("GET status = %d, want 412", resp.StatusCode)
	}
	if resp := h.post("/api/me/mfa/enroll"); resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("POST enroll status = %d, want 412", resp.StatusCode)
	}
	if resp := h.delete("/api/me/mfa/totp%7Cabc"); resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("DELETE status = %d, want 412", resp.StatusCode)
	}
}
