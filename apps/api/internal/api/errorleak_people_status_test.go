package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
)

// Two Auth0 answers an admin can act on get their own short message; the rest
// stay generic.
func TestPeopleActionsMapAuth0ConflictAndRateLimit(t *testing.T) {
	admin := "domestique-users,domestique-admins"
	for _, tc := range []struct {
		name       string
		status     int
		wantStatus int
		wantText   string
	}{
		{"already exists", 409, http.StatusConflict, "already exists"},
		{"rate limited", 429, http.StatusTooManyRequests, "try again"},
		{"anything else", 500, http.StatusBadGateway, "could not complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := &auth0mgmt.APIError{Status: tc.status, Message: "tenant=domestique-prod scope=read:users", Code: "x"}
			h := newPeopleHarness(t, &fakePeople{inviteErr: secret})
			resp := h.as("wilant", admin, http.MethodPost, "/api/people", `{"email":"new@example.com","name":"New","role":"rider"}`)
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.wantStatus, b)
			}
			if !strings.Contains(strings.ToLower(string(b)), tc.wantText) {
				t.Errorf("body %s lacks %q", b, tc.wantText)
			}
			if strings.Contains(string(b), "domestique-prod") {
				t.Errorf("the upstream's text leaked: %s", b)
			}
		})
	}
}
