package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// goalFor gives rider a row of data, so it counts as a rider that has some.
func goalFor(t *testing.T, h *bindingHarness, rider string) {
	t.Helper()
	_, tr := wireData(t, h.ssoHarness)
	if _, err := tr.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: rider, Name: "Goal", Sport: model.SportCycling}); err != nil {
		t.Fatal(err)
	}
}

// I3c: the lockout recovery. An owner who landed on a suffixed rider can bind
// their own account to the rider that has their data, but only while nothing
// else is bound to it.
func TestAnAdminMayBindTheirOwnAccountToARiderNobodyIsBoundTo(t *testing.T) {
	h := newBindingHarness(t)
	goalFor(t, h, "wilant")

	resp := h.admin.do(http.MethodPut, peoplePath("auth0|boss", "/binding"), `{"rider":"wilant"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT own binding to a free rider = %d: %v", resp.StatusCode, jsonBody(t, resp))
	}
	if rider, _ := h.binding("auth0|boss"); rider != "wilant" {
		t.Errorf("binding = %q, want wilant", rider)
	}
	out := h.logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "admin moved their own identity binding") || !strings.Contains(out, "rider=wilant") {
		t.Errorf("no Warn for the self-move naming the rider:\n%s", out)
	}
	if strings.Contains(out, "auth0|boss") || strings.Contains(out, "@example.com") {
		t.Errorf("the log names a sub or an address:\n%s", out)
	}
	// Their session held the old rider; it ended with the move.
	if got := h.admin.rider(); got != "" {
		t.Errorf("the old session answers as %q", got)
	}
	h.admin.signIn(inGroups(google("auth0|boss", "Boss", "boss@example.com", true), "admins"))
	if got := h.admin.rider(); got != "wilant" {
		t.Errorf("after signing in again the admin is %q, want wilant", got)
	}
}

// The recovery is only for a rider nobody is bound to: onto one that has any
// binding it is still refused, even with an admin's own say-so.
func TestAnAdminStillCannotBindThemselvesOntoABoundRider(t *testing.T) {
	h := newBindingHarness(t)
	bindTo(t, h.ssoHarness, "google-oauth2|other", "target", "target@example.com")
	resp := h.admin.do(http.MethodPut, peoplePath("auth0|boss", "/binding"), `{"rider":"target"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if rider, _ := h.binding("auth0|boss"); rider != "boss" {
		t.Errorf("binding = %q, want it unchanged", rider)
	}
}

func TestRiderInfoSaysWhetherARiderHasDataAndHowManyAccountsAreBoundToIt(t *testing.T) {
	h := newBindingHarness(t)
	goalFor(t, h, "wilant")
	bindTo(t, h.ssoHarness, "google-oauth2|a", "wilant", "a@example.com")
	bindTo(t, h.ssoHarness, "auth0|b", "wilant", "a@example.com")

	var info struct {
		Rider    string `json:"rider"`
		HasData  bool   `json:"hasData"`
		Bindings int    `json:"bindings"`
	}
	if code := h.admin.json("/api/people/riders/Wilant", &info); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if info.Rider != "wilant" || !info.HasData || info.Bindings != 2 {
		t.Errorf("info = %+v, want wilant with data and 2 bindings", info)
	}
	info.HasData, info.Bindings = true, 9
	if code := h.admin.json("/api/people/riders/nobody-here", &info); code != http.StatusOK || info.HasData || info.Bindings != 0 {
		t.Errorf("info for an unknown rider = %+v (status %d), want no data, no bindings", info, code)
	}
	if code := h.admin.json("/api/people/riders/has%20spaces", nil); code != http.StatusBadRequest {
		t.Errorf("an illegal rider = %d, want 400", code)
	}
	rider := h.newActor()
	rider.signIn(inGroups(google("google-oauth2|r", "Rider", "r@example.com", true), "cyclists"))
	if code := rider.json("/api/people/riders/wilant", nil); code != http.StatusForbidden {
		t.Errorf("a rider asking = %d, want 403", code)
	}
}

// I4: data with nobody to sign in as it.
func TestPeopleListsRidersWithDataButNoAccount(t *testing.T) {
	h := newBindingHarness(t, person("google-oauth2|sam", "Sam", "sam@example.com", true))
	goalFor(t, h, "ghost")
	goalFor(t, h, "sam")
	goalFor(t, h, "bound-elsewhere")
	bindTo(t, h.ssoHarness, "auth0|elsewhere", "bound-elsewhere", "e@example.com")

	var out struct {
		Riders []string `json:"riders"`
	}
	if code := h.admin.json("/api/people/orphans", &out); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(out.Riders) != 1 || out.Riders[0] != "ghost" {
		t.Errorf("riders = %v, want only [ghost]: sam has an account, bound-elsewhere a binding, boss no data", out.Riders)
	}
}

// I3d: the page marks a rider whose data somebody cannot currently sign in as.
func TestPeopleMarksUnverifiedOrUnknownClaimantsOfRidersWithDataAsNeedingAttention(t *testing.T) {
	unknown := person("auth0|ghost", "Ghosty", "g@example.com", true)
	unknown.ProfileKnown = false
	h := newBindingHarness(t,
		person("auth0|r1", "Robin", "robin@example.com", true),
		person("google-oauth2|r2", "Robin", "robin@example.com", false),
		unknown,
		person("auth0|fine", "Fine", "fine@example.com", false), // sole claimant, no data: simply unbound
	)
	goalFor(t, h, "robin")
	goalFor(t, h, "ghosty")

	var out []struct {
		ID            string `json:"id"`
		BindingStatus string `json:"bindingStatus"`
	}
	if code := h.admin.json("/api/people", &out); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	got := map[string]string{}
	for _, p := range out {
		got[p.ID] = p.BindingStatus
	}
	want := map[string]string{
		"auth0|r1": "needs-attention", "google-oauth2|r2": "needs-attention", "auth0|ghost": "needs-attention",
		"auth0|fine": "unbound",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: status = %q, want %q", id, got[id], w)
		}
	}
}
