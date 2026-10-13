package auth0mgmt

import "testing"

// email_verified is not in the role-members listing either; the identity
// bootstrap needs it to decide which addresses may stand for a person, so it
// rides the same search lookup as nickname and blocked.
func TestListPeopleReportsWhetherTheEmailIsVerified(t *testing.T) {
	f := newFakeTenant(t)
	f.users["u1"] = struct{ email, name string }{"verified@example.com", "Verified Person"}
	f.users["u2"] = struct{ email, name string }{"unverified@example.com", "Unverified Person"}
	f.roleMembers["role-gate"] = []string{"u1", "u2"}
	f.lastSeen["u1"] = struct{ createdAt, lastLogin, nickname string }{"2026-01-01T00:00:00.000Z", "", ""}
	f.lastSeen["u2"] = struct{ createdAt, lastLogin, nickname string }{"2026-01-01T00:00:00.000Z", "", ""}
	f.verified["u1"] = true
	c := newTestClient(t, f)

	people, err := c.ListPeople(t.Context(), "domestique-users")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range people {
		got[p.UserID] = p.EmailVerified
	}
	if !got["u1"] || got["u2"] || len(got) != 2 {
		t.Errorf("EmailVerified by user = %v, want u1 true and u2 false", got)
	}
}
