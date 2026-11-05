package api_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
)

// AGENTS.md: never log a sub next to a rider name. A log line about a person
// may name the rider, or carry the Auth0 user id, but not both. These run the
// two paths that log a rider beside a person and scan every line written.
func TestPeopleLogsNeverPairAPersonIDWithARiderName(t *testing.T) {
	const id = "auth0|u-7731"

	t.Run("delete", func(t *testing.T) {
		var logs bytes.Buffer
		h := newPeopleHarness(t, &fakePeople{}, func(s *api.Server) { s.Log = scanLogger(&logs); s.Box = testPeopleBox(t) })

		resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodDelete,
			"/api/people/"+id+"?rider=departed-rider", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		assertNoLineHoldsIDAndRider(t, logs.String(), "person deleted", id, "departed-rider", "wilant")
		if !strings.Contains(logs.String(), "ref=") {
			t.Errorf("the line carries no keyed ref for the person: %s", logs.String())
		}
	})

	t.Run("role change", func(t *testing.T) {
		var logs bytes.Buffer
		h := newPeopleHarness(t, &fakePeople{}, func(s *api.Server) { s.Log = scanLogger(&logs); s.Box = testPeopleBox(t) })

		resp := h.as("wilant", "domestique-users,domestique-admins", http.MethodPut,
			"/api/people/"+id+"/role", `{"role":"admin"}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		assertNoLineHoldsIDAndRider(t, logs.String(), "person role changed", id, "wilant")
	})
}

// assertNoLineHoldsIDAndRider fails when a log line carries the person's id
// and any of the given names. It also requires the line the test is about to
// have been written, so an empty log cannot pass by accident.
func assertNoLineHoldsIDAndRider(t *testing.T, logs, want, id string, names ...string) {
	t.Helper()
	found := false
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, want) {
			found = true
		}
		if !strings.Contains(line, id) {
			continue
		}
		for _, name := range names {
			if name != id && strings.Contains(line, name) {
				t.Errorf("log line holds the person id and a rider name %q: %s", name, line)
			}
		}
	}
	if !found {
		t.Fatalf("no %q line was logged; logs were:\n%s", want, logs)
	}
}

// testPeopleBox gives the server the key a deployment has, so the log's ref
// is the keyed one and not the empty value an unkeyed server writes.
func testPeopleBox(t *testing.T) *secrets.Box {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}
