// Acceptance tests for the CLI: every command, driven through run() the way
// the shell drives it, against real files in a temp directory.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

const testConfig = `
source:
  dsn: ./data/domestique.db
`

const exampleGPX = `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" creator="test" xmlns="http://www.topografix.com/GPX/1/1">
  <trk><trkseg>
    <trkpt lat="50.7920" lon="2.8180"><ele>42.0</ele></trkpt>
    <trkpt lat="50.7982" lon="2.8344"><ele>128.0</ele></trkpt>
    <trkpt lat="50.8007" lon="2.8437"><ele>139.0</ele></trkpt>
  </trkseg></trk>
</gpx>`

// workspace builds a temp directory with a config and a folder of .gpx files
// to import, and makes it the working directory so the CLI's relative defaults
// apply. The library itself is the database the config points at.
func workspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	write(t, filepath.Join(dir, "domestique.yaml"), testConfig)

	incoming := filepath.Join(dir, "incoming")
	if err := os.MkdirAll(incoming, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(incoming, "kemmelberg-loop.gpx"), exampleGPX)

	t.Chdir(dir)
	return dir
}

// linkAccount links a head unit the way the UI would, so the CLI tests have
// something to push to. Accounts live in the database now; nothing in the
// config file names them.
func linkAccount(t *testing.T, dsn, provider, rider string) {
	t.Helper()

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, err := accounts.UseDB(db.Conn(), db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Link(t.Context(), model.Provider(provider), rider, ""); err != nil {
		t.Fatal(err)
	}
}

// seedOwnedRoute writes a route directly, owned by the given rider. The
// CLI's own import command has no owner concept — it is a bulk filesystem
// import, not an authenticated upload — and an unowned route now resolves
// to nobody, since a nil-target route reaches only its owner's own
// accounts (see config.TargetsFor). Tests that need a route which actually
// reaches somewhere seed one this way instead of going through import.
func seedOwnedRoute(t *testing.T, dsn, owner string) {
	t.Helper()

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Create(t.Context(), source.CreateRequest{
		Name: "Kemmelberg Loop", GPX: []byte(exampleGPX), UploadedBy: owner,
	}); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// capture runs the CLI and returns everything it printed to stdout.
func capture(t *testing.T, args ...string) (string, error) {
	t.Helper()

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer

	runErr := run(args)

	writer.Close()
	os.Stdout = original

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := reader.Read(buf)
		sb.Write(buf[:n])
		if readErr != nil {
			break
		}
	}
	return sb.String(), runErr
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := capture(t, args...)
	if err != nil {
		t.Fatalf("domestique %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func TestCLIHelp(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"--help"}} {
		out := mustRun(t, args...)
		for _, command := range []string{"validate", "plan", "push", "state", "import", "serve"} {
			if !strings.Contains(out, command) {
				t.Errorf("%v: help does not mention %q", args, command)
			}
		}
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	_, err := capture(t, "frobnicate")
	if err == nil {
		t.Fatal("unknown command exited 0")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error does not name the command: %v", err)
	}
}

func TestCLIValidate(t *testing.T) {
	workspace(t)
	mustRun(t, "import", "--from", "./incoming")

	out := mustRun(t, "validate")
	for _, want := range []string{"kemmelberg-loop", "Kemmelberg Loop", "1 route(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate output missing %q:\n%s", want, out)
		}
	}
}

// A broken file must be reported without stopping the rest of the import.
func TestCLIImportReportsBrokenFiles(t *testing.T) {
	dir := workspace(t)
	write(t, filepath.Join(dir, "incoming", "broken.gpx"), "this is not xml")

	out, err := capture(t, "import", "--from", "./incoming")
	if err == nil {
		t.Error("expected a non-zero exit when a file is broken")
	}
	if !strings.Contains(out, "kemmelberg-loop") {
		t.Errorf("the healthy file was dropped:\n%s", out)
	}
	if !strings.Contains(out, "1 of 2 file(s) imported") {
		t.Errorf("summary missing:\n%s", out)
	}
}

func TestCLIImportFromAMissingDirectory(t *testing.T) {
	workspace(t)
	if _, err := capture(t, "import", "--from", "./nope"); err == nil {
		t.Fatal("expected an error when the directory does not exist")
	}
}

// With no config at all the default is a database, which is created on
// demand — so a fresh install works rather than erroring about a missing
// routes directory.
func TestCLIValidateWithNoConfigCreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	out, err := capture(t, "validate")
	if err != nil {
		t.Fatalf("validate on a fresh directory failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "database") {
		t.Errorf("expected a database library, got:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "data", "domestique.db")); statErr != nil {
		t.Errorf("database not created: %v", statErr)
	}
}

// Nothing linked means nowhere to push, and plan says so by having nothing
// to do.
func TestCLIPlanWithNothingLinked(t *testing.T) {
	workspace(t)
	mustRun(t, "import", "--from", "./incoming")

	out := mustRun(t, "plan")
	if !strings.Contains(out, "up to date") && !strings.Contains(out, "0 change") {
		t.Errorf("expected an empty plan with nothing linked:\n%s", out)
	}
}

// With a linked head unit, the same route does produce a plan.
func TestCLIPlanWithALinkedAccount(t *testing.T) {
	dir := workspace(t)
	db := filepath.Join(dir, "data", "routes.db")

	linkAccount(t, db, "garmin", "one")
	seedOwnedRoute(t, db, "one")

	out := mustRun(t, "plan", "--db", db)
	if !strings.Contains(out, "create") || !strings.Contains(out, "garmin:one") {
		t.Errorf("plan does not target the linked account:\n%s", out)
	}
}

func TestCLIPushDryRunChangesNothing(t *testing.T) {
	dir := workspace(t)
	db := filepath.Join(dir, "data", "routes.db")
	linkAccount(t, db, "garmin", "one")
	seedOwnedRoute(t, db, "one")

	out := mustRun(t, "push", "--db", db, "--dry-run")
	if !strings.Contains(out, "dry run") {
		t.Errorf("dry run not announced:\n%s", out)
	}

	// And the plan is unchanged afterwards.
	if out := mustRun(t, "plan", "--db", db); !strings.Contains(out, "1 change(s)") {
		t.Errorf("dry run changed the plan:\n%s", out)
	}
}

// The adapters are stubs, so a real push must fail loudly rather than
// silently recording success.
func TestCLIPushFailsWhileAdaptersAreStubs(t *testing.T) {
	dir := workspace(t)
	db := filepath.Join(dir, "data", "routes.db")
	linkAccount(t, db, "garmin", "one")
	seedOwnedRoute(t, db, "one")

	_, err := capture(t, "push", "--db", db)
	if err == nil {
		t.Fatal("push reported success with stub adapters")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestCLIStateOnFreshWorkspace(t *testing.T) {
	workspace(t)

	out := mustRun(t, "state")
	if !strings.Contains(out, "nothing has been pushed yet") {
		t.Errorf("state output = %q", out)
	}
}

func TestCLIImportIntoDatabase(t *testing.T) {
	dir := workspace(t)
	db := filepath.Join(dir, "data", "routes.db")

	out := mustRun(t, "import", "--db", db, "--from", "./incoming")
	if !strings.Contains(out, "1 of 1 file(s) imported") {
		t.Errorf("import summary missing:\n%s", out)
	}

	// The database is now a working library.
	out = mustRun(t, "validate", "--db", db)
	if !strings.Contains(out, "Kemmelberg Loop") {
		t.Errorf("imported route missing from the database:\n%s", out)
	}
	if !strings.Contains(out, "database") {
		t.Errorf("validate did not report the database library:\n%s", out)
	}
}

func TestCLIImportRequiresFrom(t *testing.T) {
	workspace(t)
	if _, err := capture(t, "import"); err == nil {
		t.Fatal("import without --from succeeded")
	}
}
