package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/auth"
)

// An admin can already bind their own account to any unbound rider from the
// People page, so holding it back at startup protects nothing and locks the
// owner out: they sign in as an empty "wilant-2" and nothing says why.
func TestBootstrapBindsAnAdminSoleClaimantNewerThanItsData(t *testing.T) {
	owner := at(person("auth0|owner", "Wilant", "owner@example.com", true), afterData)
	owner.Roles = []string{"domestique-admins"}
	e := newBootstrapEnv(t, auth.ModeOIDC, owner)
	e.dataAt(t, "wilant", dataTime)

	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.bound(t); got["auth0|owner"] != "wilant" {
		t.Fatalf("bindings = %v, want the admin bound to wilant", got)
	}
	if res.Bound != 1 || res.Conflicts != 0 {
		t.Errorf("result = %+v, want 1 bound and no conflict", res)
	}
	out := e.log.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "identity bootstrap bound an admin") || !strings.Contains(out, "rider=wilant") {
		t.Errorf("no Info naming the rider:\n%s", out)
	}
	if strings.Contains(out, "auth0|") || strings.Contains(out, "@example.com") {
		t.Errorf("the log names the account:\n%s", out)
	}
}

// The exemption is for the admin role only: anyone else who joined after the
// data existed may be somebody who renamed themselves, and stays flagged.
func TestBootstrapStillFlagsARiderRoleSoleClaimantNewerThanItsData(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC, at(person("auth0|rider", "Wilant", "rider@example.com", true), afterData))
	e.dataAt(t, "wilant", dataTime)

	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.bound(t); len(got) != 0 {
		t.Errorf("bindings = %v, want none", got)
	}
	if res.Conflicts != 1 {
		t.Errorf("conflicts = %d, want 1", res.Conflicts)
	}
}
