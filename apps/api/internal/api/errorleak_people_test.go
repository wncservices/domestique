package api_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The People page talks to Auth0's Management API, whose errors carry request
// ids, tenant names and sometimes a token's scope list. The admin who clicked
// gets "Auth0 ..." and the log gets the rest.
func TestPeopleAuth0FailuresDoNotEchoTheUpstreamError(t *testing.T) {
	secret := errors.New(`auth0mgmt: 403 insufficient_scope tenant=domestique-prod.eu.auth0.com scope=read:users`)
	admin := "domestique-users,domestique-admins"

	cases := []struct {
		name   string
		fake   *fakePeople
		method string
		path   string
		body   string
	}{
		{"invite", &fakePeople{inviteErr: secret}, http.MethodPost, "/api/people", `{"email":"new@example.com","name":"New","role":"rider"}`},
		{"lookup before invite", &fakePeople{findErr: secret}, http.MethodPost, "/api/people", `{"email":"new@example.com","name":"New","role":"rider"}`},
		{"change roles", &fakePeople{rolesErr: secret}, http.MethodPut, "/api/people/u1/role", `{"role":"admin"}`},
		{"block", &fakePeople{setBlockedErr: secret}, http.MethodPut, "/api/people/u1/blocked", `{"blocked":true,"email":"x@example.com"}`},
		{"delete", &fakePeople{deleteUserErr: secret}, http.MethodDelete, "/api/people/u1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPeopleHarness(t, tc.fake)
			resp := h.as("wilant", admin, tc.method, tc.path, tc.body)
			b := new(strings.Builder)
			buf := make([]byte, 4096)
			for {
				n, err := resp.Body.Read(buf)
				b.Write(buf[:n])
				if err != nil {
					break
				}
			}
			if resp.StatusCode < 500 {
				t.Fatalf("status = %d, want the failing call to be reached (a 5xx)", resp.StatusCode)
			}
			for _, leak := range []string{"insufficient_scope", "domestique-prod", "read:users"} {
				if strings.Contains(b.String(), leak) {
					t.Fatalf("response leaks %q: %s", leak, b.String())
				}
			}
		})
	}
}

// A pacing push that Garmin refuses used to echo Garmin's own error text.
func TestAPacingPushFailureDoesNotEchoTheProvidersError(t *testing.T) {
	h := newPacingPushHarness(t)
	h.connectGarmin("wilant")
	slug := h.route("wilant", "Demo Hills")
	g := h.goal("wilant", slug)

	h.courses.failNext = true
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/routes/"+slug+"/pacing/push",
		`{"provider":"garmin","goal":"`+g.ID+`"}`)
	body := string(readAll(t, resp))
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(body, "garmin said no") {
		t.Fatalf("the provider's own error is in the response: %s", body)
	}
	if !strings.Contains(body, "Garmin") {
		t.Fatalf("the response does not say which provider failed: %s", body)
	}
}
