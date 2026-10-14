package api_test

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
)

// The People admin flows used to log the invitee's address on every outcome
// (audit N10). The mailer package's policy is the opposite, and a log line is
// kept far longer and read by far more people than the admin page.
func TestPeopleFlowsDoNotLogEmailAddresses(t *testing.T) {
	const admin = "domestique-users,domestique-admins"
	invite := func(fake *fakePeople, email string) string {
		var logs strings.Builder
		bl := newTestBlocklist(t)
		h := newPeopleHarness(t, fake, func(s *api.Server) {
			s.Log = slog.New(slog.NewTextHandler(&logs, nil))
			s.Blocklist = bl
		})
		h.as("wilant", admin, http.MethodPost, "/api/people", `{"email":"`+email+`","name":"Someone","role":"rider"}`)
		return logs.String()
	}

	for name, fake := range map[string]*fakePeople{
		"created":           {},
		"email send failed": {emailErr: errors.New("smtp down")},
		"invite failed":     {inviteErr: errors.New("auth0 down")},
		"lookup failed":     {findErr: errors.New("auth0 down")},
		"existing identity": {findByEmail: map[string][]auth0mgmt.Person{"invitee@example.com": {{UserID: "google-oauth2|1", Email: "invitee@example.com"}}}},
		"roles failed": {
			findByEmail: map[string][]auth0mgmt.Person{"invitee@example.com": {{UserID: "google-oauth2|1", Email: "invitee@example.com"}}},
			rolesErr:    errors.New("auth0 down"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if logs := invite(fake, "invitee@example.com"); strings.Contains(logs, "invitee") || strings.Contains(logs, "example.com") {
				t.Errorf("logs carry the address:\n%s", logs)
			}
		})
	}

	t.Run("block", func(t *testing.T) {
		var logs strings.Builder
		bl := newTestBlocklist(t)
		h := newPeopleHarness(t, &fakePeople{}, func(s *api.Server) {
			s.Log = slog.New(slog.NewTextHandler(&logs, nil))
			s.Blocklist = bl
		})
		h.as("wilant", admin, http.MethodPut, "/api/people/u1/blocked", `{"blocked":true,"email":"invitee@example.com","reason":"x"}`)
		if strings.Contains(logs.String(), "invitee") || strings.Contains(logs.String(), "example.com") {
			t.Errorf("logs carry the address:\n%s", logs.String())
		}
	})
}
