package main

import (
	"os"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/identities"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

const oidcConfig = `
source:
  dsn: ./data/domestique.db
auth:
  mode: oidc
  oidc:
    issuer: https://tenant.example/
    client_id: c
    redirect_url: https://app.example/sso/callback
`

func useOIDCConfig(t *testing.T) string {
	t.Helper()
	dir := workspace(t)
	if err := os.WriteFile("domestique.yaml", []byte(oidcConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	withKey(t)
	return dir + "/data/domestique.db"
}

func bindingCount(t *testing.T, dsn string) int {
	t.Helper()
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if exists, _ := identities.Exists(db.Conn(), db.DSN()); !exists {
		return 0
	}
	return countRows(t, db, "rider_identities")
}

// I5: a typo in the issuer would write a binding that no sign-in ever matches,
// and report success. The configured issuer is the only one that can match.
func TestBindIdentityRefusesAnIssuerThatIsNotTheConfiguredOne(t *testing.T) {
	dsn := useOIDCConfig(t)

	for _, wrong := range []string{"https://tenant.example", "https://other.example/", "tenant.example/"} {
		out, err := capture(t, "bind-identity", wrong, "auth0|abc", "wilant")
		if err == nil {
			t.Fatalf("%q was accepted:\n%s", wrong, out)
		}
		if !strings.Contains(err.Error(), "https://tenant.example/") || !strings.Contains(err.Error(), "--force") {
			t.Errorf("%q: the refusal should print the configured issuer and mention --force: %v", wrong, err)
		}
	}
	if _, err := capture(t, "unbind-identity", "https://tenant.example", "auth0|abc"); err == nil {
		t.Error("unbind-identity accepted the wrong issuer")
	}
	if n := bindingCount(t, dsn); n != 0 {
		t.Errorf("%d bindings written by refused commands", n)
	}

	// --force is for a deployment that really is moving tenants.
	out := mustRun(t, "bind-identity", "--force", "https://old-tenant.example/", "auth0|abc", "wilant")
	if !strings.Contains(out, "bound") {
		t.Errorf("--force output:\n%s", out)
	}
	// The configured one needs no flag.
	mustRun(t, "bind-identity", "https://tenant.example/", "auth0|def", "pat")
	if n := bindingCount(t, dsn); n != 2 {
		t.Errorf("%d bindings, want 2", n)
	}
}

func TestBindIdentityDryRunAlsoChecksTheIssuer(t *testing.T) {
	useOIDCConfig(t)
	if _, err := capture(t, "bind-identity", "--dry-run", "https://tenant.example", "auth0|abc", "wilant"); err == nil {
		t.Error("a dry run accepted the wrong issuer: it would have promised something the real run cannot do")
	}
}

// The first command that is not a dry run creates the table, and creating it
// ends every session. An operator should hear that, not find out from riders.
func TestTheFirstBindingCommandSaysItEndsEverySession(t *testing.T) {
	useOIDCConfig(t)

	out := mustRun(t, "bind-identity", "https://tenant.example/", "auth0|abc", "wilant")
	if !strings.Contains(out, "created the bindings table") || !strings.Contains(out, "ended ALL sessions") {
		t.Errorf("the first command did not warn:\n%s", out)
	}
	out = mustRun(t, "bind-identity", "https://tenant.example/", "auth0|def", "pat")
	if strings.Contains(out, "ended ALL sessions") {
		t.Errorf("a later command warned again:\n%s", out)
	}
	for _, args := range [][]string{{"help"}} {
		help := mustRun(t, args...)
		if !strings.Contains(help, "creates the bindings table and ends ALL sessions") || !strings.Contains(help, "--force") {
			t.Errorf("help does not state the side effect and --force:\n%s", help)
		}
	}
}

func TestADryRunDoesNotWarnAboutSessionsItDoesNotEnd(t *testing.T) {
	useOIDCConfig(t)
	out := mustRun(t, "bind-identity", "--dry-run", "https://tenant.example/", "auth0|abc", "wilant")
	if !strings.Contains(out, "dry run") || !strings.Contains(out, "would create the bindings table and end ALL sessions") {
		t.Errorf("a dry run before the table exists should say what the real run would do:\n%s", out)
	}
}
