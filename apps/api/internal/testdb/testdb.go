// Package testdb hands tests a SQLite database whose schema is already
// migrated, so a harness does not pay for the migrations on every test.
//
// A full set of store migrations is a few hundred DDL statements, each its own
// write transaction. Under -race that was roughly a third of the api package's
// CPU time: the 1,300 tests were each spending more time creating tables than
// exercising anything. The migrations run once per test binary here; every test
// gets a copy of the resulting file.
//
// The copy is a normal database: each store's UseDB still runs against it and
// finds everything in place, which is the same path a restart of a deployed
// instance takes. Tests that exercise a migration itself (a table that predates
// a column) build their own database and do not use this package.
//
// This package is imported only from _test.go files.
package testdb

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/blocklist"
	"github.com/wncservices/domestique/apps/api/internal/calendarfeed"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/garminmfa"
	"github.com/wncservices/domestique/apps/api/internal/identities"
	"github.com/wncservices/domestique/apps/api/internal/morningsummary"
	"github.com/wncservices/domestique/apps/api/internal/pacingpush"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/ridestart"
	"github.com/wncservices/domestique/apps/api/internal/routeshare"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/weather"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var (
	once     sync.Once
	template []byte
	buildErr error
)

// Path returns the path of a fresh, already-migrated SQLite database inside
// the test's own temp dir. It is the drop-in replacement for
// filepath.Join(t.TempDir(), "x.db").
func Path(t testing.TB) string {
	t.Helper()
	once.Do(func() { template, buildErr = build() })
	if buildErr != nil {
		t.Fatalf("build migrated template database: %v", buildErr)
	}
	p := filepath.Join(t.TempDir(), "routes.db")
	if err := os.WriteFile(p, template, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func build() ([]byte, error) {
	dir, err := os.MkdirTemp("", "domestique-template-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	p := filepath.Join(dir, "template.db")

	db, err := source.OpenDB(p)
	if err != nil {
		return nil, err
	}
	conn, dsn := db.Conn(), db.DSN()
	steps := []func() error{
		func() error { _, err := crew.UseDB(conn, dsn); return err },
		func() error { _, err := accounts.UseDB(conn, dsn); return err },
		func() error { _, err := state.UseDB(conn, dsn); return err },
		func() error { _, err := schedule.UseDB(conn, dsn); return err },
		func() error { _, err := blocklist.UseDB(conn, dsn); return err },
		func() error { _, err := routeshare.UseDB(conn, dsn); return err },
		func() error { _, err := calendarfeed.UseDB(conn, dsn); return err },
		func() error { _, err := morningsummary.UseDB(conn, dsn); return err },
		func() error { _, err := workout.UseDB(conn, dsn); return err },
		func() error { _, err := pacingpush.UseDB(conn, dsn); return err },
		func() error { _, err := providerlink.UseDB(conn, dsn, nil); return err },
		func() error { _, err := garminmfa.UseDB(conn, dsn, nil); return err },
		func() error { _, err := settings.UseDB(conn, dsn, nil); return err },
		func() error { _, err := weather.UseDB(conn, dsn); return err },
		func() error { _, err := ridestart.UseDB(conn, dsn); return err },
		func() error { _, err := sessions.UseDB(conn, dsn, nil); return err },
		func() error { _, err := identities.UseDB(conn, dsn, nil); return err },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	// Fold the WAL back into the main file so copying that one file is a
	// complete copy.
	if _, err := conn.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(p) // #nosec G304 -- a path this function just made
}
