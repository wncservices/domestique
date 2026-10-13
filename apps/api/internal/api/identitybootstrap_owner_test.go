package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// dataAt gives rider a row of data (a training goal) stamped at the given time,
// the way an earlier life of the deployment left it.
func (e *bootstrapEnv) dataAt(t *testing.T, rider string, at time.Time) {
	t.Helper()
	tr, err := workout.UseDB(e.db.Conn(), e.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	tr.Clock = func() time.Time { return at }
	if _, err := tr.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: rider, Name: "Goal", Sport: model.SportCycling}); err != nil {
		t.Fatal(err)
	}
}

var (
	dataTime  = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	before    = dataTime.Add(-30 * 24 * time.Hour)
	afterData = dataTime.Add(30 * 24 * time.Hour)
)

func at(p auth0mgmt.Person, created time.Time) auth0mgmt.Person {
	p.CreatedAt = created
	return p
}

// I3a: a rider claimed by exactly one gate-role account is bound even when the
// issuer has never verified the address. This is the owner on a password
// account, and not binding them is the lockout.
func TestBootstrapBindsASoleClaimantWhoseEmailIsUnverified(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC, at(person("auth0|owner", "Wilant", "wilant@example.com", false), before))
	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.bound(t); got["auth0|owner"] != "wilant" {
		t.Fatalf("bindings = %v, want the sole claimant bound to wilant", got)
	}
	if res.Bound != 1 {
		t.Errorf("bound = %d", res.Bound)
	}
	// Only a verified address is ever kept: this one must not be able to join anyone.
	if riders, _ := e.ids.RidersByEmail(context.Background(), bootstrapIssuer, e.ids.EmailMAC("wilant@example.com")); len(riders) != 0 {
		t.Errorf("an unverified address was stored and matches %v", riders)
	}
}

// I3b: missing data is not "unverified". Nothing is bound from a person the
// Users Search had nothing on, and it is an Error with a count.
func TestBootstrapNeverBindsOnMissingData(t *testing.T) {
	missing := person("auth0|ghost", "Ghost", "ghost@example.com", true)
	missing.ProfileKnown = false
	missing.EmailVerified = false
	e := newBootstrapEnv(t, auth.ModeOIDC, missing, at(person("auth0|fine", "Fine", "fine@example.com", true), before))
	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := e.bound(t)
	if _, bound := got["auth0|ghost"]; bound {
		t.Error("an unknown person was bound")
	}
	if got["auth0|fine"] != "fine" {
		t.Errorf("a known person was not bound: %v", got)
	}
	if res.Unknown != 1 {
		t.Errorf("unknown = %d, want 1", res.Unknown)
	}
	out := e.log.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "identity bootstrap missing profile data") || !strings.Contains(out, "identities=1") {
		t.Errorf("no Error with a count for the unknown person:\n%s", out)
	}
	if strings.Contains(out, "ghost") || strings.Contains(out, "@example.com") || strings.Contains(out, "auth0|") {
		t.Errorf("the log names the person:\n%s", out)
	}
}

// I3d: several claimants, one unverified, and the rider has data: an Error.
func TestBootstrapReportsAnUnverifiedClaimantOfARiderWithDataAsAnError(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC,
		at(person("auth0|1", "Wilant", "wilant@example.com", true), before),
		at(person("google-oauth2|2", "Wilant", "wilant@example.com", false), before),
	)
	e.dataAt(t, "wilant", dataTime)
	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.bound(t); len(got) != 0 {
		t.Errorf("bindings = %v, want none", got)
	}
	if res.NeedsAttention != 1 {
		t.Errorf("needs attention = %d, want 1", res.NeedsAttention)
	}
	out := e.log.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "identity bootstrap needs attention") || !strings.Contains(out, "rider=wilant") || !strings.Contains(out, "identities=2") {
		t.Errorf("no Error naming the rider and a count:\n%s", out)
	}
}

// I4: a rider with data and a single claimant who joined the tenant after that
// data is likely someone who renamed themselves. Not bound; flagged.
func TestBootstrapDoesNotBindANewerSoleClaimantOfARiderWithData(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC, at(person("google-oauth2|renamer", "Wilant", "renamer@example.com", true), afterData))
	e.dataAt(t, "wilant", dataTime)
	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.bound(t); len(got) != 0 {
		t.Errorf("bindings = %v, want none for a claimant newer than the rider's data", got)
	}
	if res.Conflicts != 1 {
		t.Errorf("conflicts = %d, want the rider flagged", res.Conflicts)
	}
	if !strings.Contains(e.log.String(), "identity bootstrap conflict") {
		t.Errorf("no conflict logged:\n%s", e.log.String())
	}
}

func TestBootstrapBindsAnOlderSoleClaimantOfARiderWithData(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC, at(person("auth0|owner", "Wilant", "wilant@example.com", true), before))
	e.dataAt(t, "wilant", dataTime)
	if _, err := e.srv.BootstrapIdentities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.bound(t); got["auth0|owner"] != "wilant" {
		t.Errorf("bindings = %v, want the older claimant bound", got)
	}
}

// I4: riders that have data but no account in the gate role are reported, by
// count, so an admin knows data is sitting with nobody to sign in as it.
func TestBootstrapReportsRidersWithDataButNoAccount(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC, at(person("auth0|sam", "Sam", "sam@example.com", true), before))
	e.dataAt(t, "orphan", dataTime)
	e.dataAt(t, "sam", dataTime)
	res, err := e.srv.BootstrapIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Orphans != 1 {
		t.Errorf("orphans = %d, want 1", res.Orphans)
	}
	out := e.log.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "identity bootstrap found riders with data but no account") || !strings.Contains(out, "riders=1") {
		t.Errorf("no Warn with a count:\n%s", out)
	}
	if strings.Contains(out, "=orphan") {
		t.Errorf("the log names the orphan rider:\n%s", out)
	}
}
