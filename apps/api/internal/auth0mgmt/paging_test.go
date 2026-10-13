package auth0mgmt

import (
	"fmt"
	"testing"
)

// The role-members and search endpoints answer 50 at a time. A tenant with
// more people than that must still be listed whole: the identity bootstrap
// decides who may be bound from this list, and a person who silently drops off
// it is a rider left unbound (or, worse, a conflict nobody sees).
func TestListPeopleCoversMoreThanOnePage(t *testing.T) {
	f := newFakeTenant(t)
	const n = 130
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("auth0|u%03d", i)
		f.users[id] = struct{ email, name string }{id + "@example.com", fmt.Sprintf("Person %03d", i)}
		f.roleMembers["role-gate"] = append(f.roleMembers["role-gate"], id)
		f.lastSeen[id] = struct{ createdAt, lastLogin, nickname string }{"2026-01-01T00:00:00.000Z", "", ""}
		f.verified[id] = true
	}
	c := newTestClient(t, f)

	people, err := c.ListPeople(t.Context(), "domestique-users")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != n {
		t.Fatalf("listed %d people, want all %d", len(people), n)
	}
	for _, p := range people {
		if !p.ProfileKnown || !p.EmailVerified || p.CreatedAt.IsZero() {
			t.Fatalf("%s has no profile data from the search (%+v): the search was not paged", p.UserID, p)
		}
	}
}

// A person the search has nothing on is unknown, which is not the same as
// unverified: nothing may be decided about them from missing data.
func TestAPersonMissingFromTheSearchIsUnknown(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"a@example.com", "A"}
	f.users["u2"] = struct{ email, name string }{"b@example.com", "B"}
	f.roleMembers["role-gate"] = []string{"u1", "u2"}
	f.lastSeen["u1"] = struct{ createdAt, lastLogin, nickname string }{"2026-01-01T00:00:00.000Z", "", ""}
	c := newTestClient(t, f)

	people, err := c.ListPeople(t.Context(), "domestique-users")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, p := range people {
		known[p.UserID] = p.ProfileKnown
	}
	if !known["u1"] || known["u2"] {
		t.Errorf("ProfileKnown = %v, want u1 true and u2 false", known)
	}
}
