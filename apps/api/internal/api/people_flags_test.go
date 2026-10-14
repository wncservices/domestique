package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type flagRow struct {
	ID            string `json:"id"`
	BoundRider    string `json:"boundRider"`
	BindingStatus string `json:"bindingStatus"`
	FlagReason    string `json:"flagReason"`
	FlagText      string `json:"flagText"`
	SuggestRider  string `json:"suggestRider"`
}

func peopleRows(t *testing.T, h *bindingHarness) map[string]flagRow {
	t.Helper()
	var out []flagRow
	if code := h.admin.json("/api/people", &out); code != http.StatusOK {
		t.Fatalf("GET /api/people = %d", code)
	}
	rows := map[string]flagRow{}
	for _, p := range out {
		rows[p.ID] = p
	}
	return rows
}

func (h *bindingHarness) dataFor(t *testing.T, rider string, when time.Time) {
	t.Helper()
	tr, err := workout.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	tr.Clock = func() time.Time { return when }
	if _, err := tr.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: rider, Name: "Goal", Sport: model.SportCycling}); err != nil {
		t.Fatal(err)
	}
}

func withRoles(p auth0mgmt.Person, roles ...string) auth0mgmt.Person {
	p.Roles = roles
	return p
}

// A person the bootstrap held back says why on the People page, and the reason
// is still there after a restart (the bootstrap ran, then the page is read by a
// later process; here the Management API no longer reproduces the condition).
func TestPeopleListExplainsWhyTheBootstrapHeldAnAccountBack(t *testing.T) {
	late := at(person("google-oauth2|renamer", "Wilant", "renamer@example.com", true), afterData)
	h := newBindingHarness(t, late)
	h.dataFor(t, "wilant", dataTime)
	if _, err := h.srv.BootstrapIdentities(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Nothing about the person reproduces the flag any more.
	h.people.people = []auth0mgmt.Person{at(person("google-oauth2|renamer", "Wilant", "renamer@example.com", true), before)}

	row := peopleRows(t, h)["google-oauth2|renamer"]
	if row.FlagReason != "newer-than-data" {
		t.Fatalf("flagReason = %q, want newer-than-data (row %+v)", row.FlagReason, row)
	}
	if !strings.Contains(row.FlagText, "wilant") {
		t.Errorf("flagText = %q, want the reason in words, naming the rider", row.FlagText)
	}
}

// Held back, then signed in anyway as "wilant-2": the row says why, and offers
// the one-click way to the real rider.
func TestPeopleListOffersTheRealRiderToAnAccountOnASuffixedFallback(t *testing.T) {
	late := at(person("google-oauth2|renamer", "Wilant", "renamer@example.com", true), afterData)
	h := newBindingHarness(t, late)
	h.dataFor(t, "wilant", dataTime)
	if _, err := h.srv.BootstrapIdentities(context.Background()); err != nil {
		t.Fatal(err)
	}
	bindTo(t, h.ssoHarness, "google-oauth2|renamer", "wilant-2", "renamer@example.com")

	row := peopleRows(t, h)["google-oauth2|renamer"]
	if row.BindingStatus != "bound" || row.BoundRider != "wilant-2" {
		t.Fatalf("row = %+v, want bound to wilant-2", row)
	}
	if row.FlagReason != "newer-than-data" {
		t.Errorf("flagReason = %q, want the held-back reason to stay on a bound row", row.FlagReason)
	}
	if row.SuggestRider != "wilant" {
		t.Errorf("suggestRider = %q, want wilant", row.SuggestRider)
	}
}

// No suggestion when the plain name has no data, or somebody is bound to it.
func TestPeopleListSuggestsNothingWithoutAnUnboundRiderWithData(t *testing.T) {
	h := newBindingHarness(t,
		person("auth0|a", "Alex", "a@example.com", true),
		person("auth0|b", "Bo", "b@example.com", true),
	)
	bindTo(t, h.ssoHarness, "auth0|a", "alex-2", "a@example.com") // alex has no data
	bindTo(t, h.ssoHarness, "auth0|b", "bo-2", "b@example.com")
	h.dataFor(t, "bo", dataTime)
	bindTo(t, h.ssoHarness, "auth0|bo-owner", "bo", "") // and bo is taken

	rows := peopleRows(t, h)
	for _, id := range []string{"auth0|a", "auth0|b"} {
		if s := rows[id].SuggestRider; s != "" {
			t.Errorf("%s: suggestRider = %q, want none", id, s)
		}
	}
}

// The owner case: signed in as "wilant-2" while "wilant" holds their data and
// nobody is bound to it, the row carries the way back.
func TestPeopleListOffersAnAdminTheirOwnRiderBack(t *testing.T) {
	owner := withRoles(at(person("auth0|owner", "Wilant", "owner@example.com", true), afterData), "admins")
	h := newBindingHarness(t, owner)
	h.dataFor(t, "wilant", dataTime)
	h.dataFor(t, "wilant.nackaerts", dataTime)
	bindTo(t, h.ssoHarness, "auth0|owner", "wilant-2", "owner@example.com")

	row := peopleRows(t, h)["auth0|owner"]
	if row.SuggestRider != "wilant" {
		t.Errorf("suggestRider = %q, want wilant", row.SuggestRider)
	}
}
