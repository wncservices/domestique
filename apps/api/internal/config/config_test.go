package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "domestique.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A missing config is normal: the defaults are enough to start uploading.
// The default is the database, so a fresh install has somewhere to put routes
// without anyone creating a directory first.
func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing config should not be an error: %v", err)
	}
	if cfg.Source.DSN != DefaultDSN {
		t.Errorf("dsn = %q, want %q", cfg.Source.DSN, DefaultDSN)
	}
	// Authentication is off by default: a fresh checkout runs on a laptop.
	if cfg.Auth.Mode != "" {
		t.Errorf("auth mode = %q, want unset (none)", cfg.Auth.Mode)
	}
}

func TestLoadParsesSource(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
source:
  dsn: ./data/routes.db
`))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Source.DSN != "./data/routes.db" {
		t.Errorf("source = %+v", cfg.Source)
	}
}

func TestLoadRejectsBadConfigs(t *testing.T) {
	for name, body := range map[string]string{
		"invalid yaml": "source: [oops",
		"required group with no auth": `
auth:
  mode: none
  required_group: riders
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Error("expected an error, got none")
			}
		})
	}
}

// Accounts are linked through the UI and live in the database. Anything in the
// config file naming them would be a second source of truth.
func TestConfigHasNoAccounts(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
accounts:
  - id: garmin:someone
    provider: garmin
    rider: someone
default_targets: [garmin:someone]
source:
  dsn: x.db
`))
	if err != nil {
		t.Fatalf("stray keys should be ignored, not fatal: %v", err)
	}
	// The point: nothing above reaches the app.
	if cfg.Source.DSN != "x.db" {
		t.Errorf("source = %+v", cfg.Source)
	}
}

// CLI flags rewrite the source after Load, so Validate has to be callable
// again.
func TestValidateRejectsAnEmptyDSN(t *testing.T) {
	cfg := &Config{Source: SourceConfig{DSN: "data/domestique.db"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cfg.Source.DSN = "   "
	if err := cfg.Validate(); err == nil {
		t.Fatal("an empty DSN was accepted; there would be nowhere to keep routes")
	}
}

// A malformed elevation.url would otherwise fail silently forever — the
// backfill it drives is deliberately best-effort per call, so nothing
// downstream of a bad URL ever surfaces an error anywhere. This has to be
// caught at startup instead, the same as a bad auth config already is.
func TestValidateRejectsAMalformedElevationURL(t *testing.T) {
	base := func() *Config {
		return &Config{
			Source:    SourceConfig{DSN: "data/domestique.db"},
			Elevation: ElevationConfig{Enabled: true, URL: "https://example.com/lookup"},
		}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("a valid elevation.url was rejected: %v", err)
	}

	for _, bad := range []string{
		"not a url at all",
		"example.com/lookup", // no scheme
		"https://",           // no host
	} {
		cfg := base()
		cfg.Elevation.URL = bad
		if err := cfg.Validate(); err == nil {
			t.Errorf("%q: expected an error, got none", bad)
		}
	}

	// Disabled: no validation at all, same as an empty URL never mattering
	// while the feature is off.
	cfg := base()
	cfg.Elevation.Enabled = false
	cfg.Elevation.URL = "not a url at all"
	if err := cfg.Validate(); err != nil {
		t.Errorf("a bad url while disabled should be ignored: %v", err)
	}
}

// A route with no targets used to reach every linked account, system-wide,
// with no consent from whoever owned the other accounts. That was the gap
// crews exist to close: the default now is the owner's own accounts only.
func TestTargetsForDefaultsToTheOwnersOwnAccounts(t *testing.T) {
	linked := []model.Account{
		{ID: "garmin:one", Provider: model.ProviderGarmin, Rider: "one"},
		{ID: "wahoo:two", Provider: model.ProviderWahoo, Rider: "two"},
	}
	noCrews := crew.Snapshot{}

	route := model.Route{Owner: "one"}
	if got := TargetsFor(route, linked, noCrews); len(got) != 1 || got[0] != "garmin:one" {
		t.Errorf("targets = %v, want [garmin:one]", got)
	}

	// An owner with no linked account of their own reaches nowhere.
	route = model.Route{Owner: "nobody-links-anything"}
	if got := TargetsFor(route, linked, noCrews); len(got) != 0 {
		t.Errorf("targets = %v, want none", got)
	}

	// An unowned route (CLI-imported, or from before ownership was tracked)
	// reaches nobody — the safe direction, not "everyone".
	if got := TargetsFor(model.Route{}, linked, noCrews); len(got) != 0 {
		t.Errorf("targets = %v, want none for an unowned route", got)
	}

	// An explicitly empty list still means nowhere, unchanged from before
	// crews existed.
	none := []string{}
	route = model.Route{Owner: "one", RouteMeta: model.RouteMeta{Targets: &none}}
	if got := TargetsFor(route, linked, noCrews); len(got) != 0 {
		t.Errorf("targets = %v, want none", got)
	}
}

// This is the property the whole feature exists for: a route shared to a
// crew reaches exactly that crew's current approved members, and nobody
// else — not a rider with a linked account who never joined.
func TestTargetsForResolvesACrewToItsCurrentApprovedMembers(t *testing.T) {
	linked := []model.Account{
		{ID: "garmin:one", Provider: model.ProviderGarmin, Rider: "one"},
		{ID: "wahoo:two", Provider: model.ProviderWahoo, Rider: "two"},
		{ID: "garmin:three", Provider: model.ProviderGarmin, Rider: "three"},
	}
	crews := crew.Snapshot{
		Crews:          []crew.Crew{{ID: "crew:sunday-club", Name: "Sunday Club", Owner: "one"}},
		ApprovedRiders: crew.MemberSet{"crew:sunday-club": {"one", "two"}},
	}

	shared := []string{"crew:sunday-club"}
	route := model.Route{Owner: "one", RouteMeta: model.RouteMeta{Targets: &shared}}

	got := TargetsFor(route, linked, crews)
	want := map[string]bool{"garmin:one": true, "wahoo:two": true}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want exactly %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected target %q — rider three is not a crew member", id)
		}
	}
}

// Membership is resolved fresh on every call, never stored on the route —
// removing a member must stop them appearing on the very next resolution,
// with nobody touching the route itself.
func TestTargetsForStopsReachingARemovedMember(t *testing.T) {
	linked := []model.Account{
		{ID: "garmin:one", Provider: model.ProviderGarmin, Rider: "one"},
		{ID: "wahoo:two", Provider: model.ProviderWahoo, Rider: "two"},
	}
	shared := []string{"crew:sunday-club"}
	route := model.Route{Owner: "one", RouteMeta: model.RouteMeta{Targets: &shared}}

	before := crew.Snapshot{
		Crews:          []crew.Crew{{ID: "crew:sunday-club", Owner: "one"}},
		ApprovedRiders: crew.MemberSet{"crew:sunday-club": {"one", "two"}},
	}
	if got := TargetsFor(route, linked, before); len(got) != 2 {
		t.Fatalf("targets = %v, want both members' accounts", got)
	}

	after := crew.Snapshot{
		Crews:          []crew.Crew{{ID: "crew:sunday-club", Owner: "one"}},
		ApprovedRiders: crew.MemberSet{"crew:sunday-club": {"one"}},
	}
	if got := TargetsFor(route, linked, after); len(got) != 1 || got[0] != "garmin:one" {
		t.Errorf("targets = %v, want only the owner's own account once the member is gone", got)
	}
}

// A route written before crews existed can hold a raw account id in
// Targets. It must never resolve to that account — the concrete proof that
// the migration story in the crew design holds: no script touches old
// rows, the resolver's own fallback is the fix.
func TestTargetsForIgnoresALegacyRawAccountID(t *testing.T) {
	linked := []model.Account{
		{ID: "garmin:one", Provider: model.ProviderGarmin, Rider: "one"},
		{ID: "wahoo:two", Provider: model.ProviderWahoo, Rider: "two"},
	}
	legacy := []string{"wahoo:two"}
	route := model.Route{Owner: "one", RouteMeta: model.RouteMeta{Targets: &legacy}}

	got := TargetsFor(route, linked, crew.Snapshot{})
	if len(got) != 1 || got[0] != "garmin:one" {
		t.Errorf("targets = %v, want only the owner's own account — the legacy id must never resolve", got)
	}
}

func TestUnknownTargetsAreReported(t *testing.T) {
	crews := crew.Snapshot{
		Crews:          []crew.Crew{{ID: "crew:sunday-club", Owner: "one"}},
		ApprovedRiders: crew.MemberSet{"crew:sunday-club": {"one"}},
	}

	unrelated := []string{"crew:does-not-exist"}
	route := model.Route{Owner: "one", RouteMeta: model.RouteMeta{Targets: &unrelated}}
	if unknown := UnknownTargets(route, crews); len(unknown) != 1 {
		t.Errorf("unknown = %v, want the unknown crew flagged", unknown)
	}

	good := []string{"crew:sunday-club"}
	route = model.Route{Owner: "one", RouteMeta: model.RouteMeta{Targets: &good}}
	if unknown := UnknownTargets(route, crews); len(unknown) != 0 {
		t.Errorf("unknown = %v, want none", unknown)
	}

	// A raw account id from before crews existed is unknown too.
	legacy := []string{"garmin:one"}
	route = model.Route{Owner: "one", RouteMeta: model.RouteMeta{Targets: &legacy}}
	if unknown := UnknownTargets(route, crews); len(unknown) != 1 {
		t.Errorf("unknown = %v, want the legacy account id flagged", unknown)
	}

	// A route with no targets has nothing to flag.
	if unknown := UnknownTargets(model.Route{}, crews); len(unknown) != 0 {
		t.Errorf("unknown = %v, want none", unknown)
	}
}
