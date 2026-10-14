package workout

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func eachFTPEngine(t *testing.T, f func(t *testing.T, db *DB)) {
	t.Helper()
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			// Postgres tests share a database; start clean.
			if _, err := db.db.Exec(`DELETE FROM ftp_changes`); err != nil {
				t.Fatal(err)
			}
			f(t, db)
		})
	}
}

func clockAt(db *DB, ts time.Time) { db.Clock = func() time.Time { return ts } }

func TestFTPChangesAreAppendedOnlyWhenTheValueDiffers(t *testing.T) {
	eachFTPEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		clockAt(db, time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC))
		save := func(watts float64) {
			t.Helper()
			if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: watts}); err != nil {
				t.Fatal(err)
			}
		}
		save(250)
		save(250) // an edit that leaves FTP alone
		clockAt(db, time.Date(2026, time.April, 20, 9, 0, 0, 0, time.UTC))
		save(260)
		save(0) // clearing FTP is not an FTP value

		got, err := db.ListFTPChanges(ctx, "wilant", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("changes = %+v, want two (0 to 250, 250 to 260)", got)
		}
		first, second := got[0], got[1]
		if first.From != 0 || first.To != 250 || first.Date != "2026-03-02" || first.Source != FTPSourceManual {
			t.Errorf("first = %+v", first)
		}
		if second.From != 250 || second.To != 260 || second.Date != "2026-04-20" || second.Source != FTPSourceManual {
			t.Errorf("second = %+v", second)
		}
		if first.ChangedAt != "2026-03-02T09:00:00Z" {
			t.Errorf("changed_at = %q, want the store clock", first.ChangedAt)
		}
	})
}

func TestFTPChangesCarryTheCallersSource(t *testing.T) {
	eachFTPEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		clockAt(db, time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC))
		for i, source := range []string{FTPSourceDetection, FTPSourceTest, FTPSourceManual} {
			clockAt(db, time.Date(2026, time.March, 2, 9, 0, i, 0, time.UTC)) // changes in one day keep their order
			if _, err := db.SaveProfileFrom(ctx, RiderProfile{Rider: "wilant", FTPWatts: float64(250 + 10*i)}, source); err != nil {
				t.Fatal(err)
			}
		}
		got, _ := db.ListFTPChanges(ctx, "wilant", "", "")
		var sources []string
		for _, c := range got {
			sources = append(sources, c.Source)
		}
		if len(sources) != 3 || sources[0] != "detection" || sources[1] != "test" || sources[2] != "manual" {
			t.Errorf("sources = %v", sources)
		}
		if _, err := db.SaveProfileFrom(ctx, RiderProfile{Rider: "wilant", FTPWatts: 999}, "made-up"); err == nil {
			t.Error("an unknown source was accepted")
		}
	})
}

func TestListFTPChangesIsDateOrderedBoundedAndPerRider(t *testing.T) {
	eachFTPEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		for _, c := range []struct {
			rider, date string
			to          float64
		}{
			{"wilant", "2026-06-01", 270}, {"wilant", "2025-12-31", 250}, {"wilant", "2026-01-01", 255}, {"jonas", "2026-02-01", 300},
		} {
			clockAt(db, mustDate(t, c.date))
			if _, err := db.SaveProfile(ctx, RiderProfile{Rider: c.rider, FTPWatts: c.to}); err != nil {
				t.Fatal(err)
			}
		}
		all, _ := db.ListFTPChanges(ctx, "wilant", "", "")
		if len(all) != 3 || all[0].Date != "2025-12-31" || all[1].Date != "2026-01-01" || all[2].Date != "2026-06-01" {
			t.Errorf("all = %+v, want date order", all)
		}
		year, _ := db.ListFTPChanges(ctx, "wilant", "2026-01-01", "2026-12-31")
		if len(year) != 2 || year[0].Date != "2026-01-01" {
			t.Errorf("2026 = %+v, want both ends inclusive and 31 December in the year before", year)
		}
	})
}

func mustDate(t *testing.T, d string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02", d)
	if err != nil {
		t.Fatal(err)
	}
	return ts.Add(12 * time.Hour)
}

func TestAcceptedSuggestionsAreCopiedInOnceAndATestIsMarkedTest(t *testing.T) {
	eachFTPEngine(t, func(t *testing.T, db *DB) {
		ctx := t.Context()
		insert := func(id, field, status, reason string, value, previous float64, updated string) {
			t.Helper()
			if _, err := db.db.Exec(db.query(`INSERT INTO threshold_suggestions
                (id, rider, field, value, previous, direction, reason, status, created_at, updated_at)
                VALUES (?, 'wilant', ?, ?, ?, 'up', ?, ?, ?, ?)`), id, field, value, previous, reason, status, updated, updated); err != nil {
				t.Fatal(err)
			}
		}
		insert("s1", "ftp", ThresholdAccepted, "from Saturday's 20-minute effort (270 W best 20 minutes)", 268, 250, "2026-02-10T08:00:00.5Z")
		insert("s2", "ftp", ThresholdAccepted, "from Tuesday's ramp test (400 W best minute)", 300, 268, "2026-04-02T08:00:00Z")
		insert("s3", "ftp", ThresholdDismissed, "ignored", 320, 300, "2026-04-03T08:00:00Z")
		insert("s4", "max_hr", ThresholdAccepted, "not an FTP", 191, 188, "2026-04-04T08:00:00Z")
		// A change the accept path already recorded for this suggestion's day and value.
		if _, err := db.db.Exec(db.query(`INSERT INTO ftp_changes (id, rider, changed_at, date, from_watts, to_watts, source)
            VALUES ('live', 'wilant', '2026-04-02T08:00:01Z', '2026-04-02', 268, 300, 'test')`)); err != nil {
			t.Fatal(err)
		}

		for range 2 { // each start runs the copy; a second one changes nothing
			if err := db.copyAcceptedFTPSuggestions(); err != nil {
				t.Fatal(err)
			}
		}
		got, _ := db.ListFTPChanges(ctx, "wilant", "", "")
		if len(got) != 2 {
			t.Fatalf("changes = %+v, want the old detection copied and the recorded test left alone", got)
		}
		if got[0].Date != "2026-02-10" || got[0].From != 250 || got[0].To != 268 || got[0].Source != FTPSourceDetection {
			t.Errorf("copied = %+v", got[0])
		}
		if got[1].ID != "live" || got[1].Source != FTPSourceTest {
			t.Errorf("second = %+v, want the already-recorded test change, not a duplicate", got[1])
		}

		// A copied test-ride suggestion is marked test.
		if _, err := db.db.Exec(db.query(`DELETE FROM ftp_changes WHERE id = 'live'`)); err != nil {
			t.Fatal(err)
		}
		if err := db.copyAcceptedFTPSuggestions(); err != nil {
			t.Fatal(err)
		}
		got, _ = db.ListFTPChanges(ctx, "wilant", "", "")
		if len(got) != 2 || got[1].To != 300 || got[1].Source != FTPSourceTest || got[1].Date != "2026-04-02" {
			t.Errorf("after the live row is gone = %+v, want the copy of the ramp test marked test", got)
		}
	})
}

func TestFTPChangesMigrationRunsTwice(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "twice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	for range 2 {
		if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
			t.Fatalf("migration: %v", err)
		}
	}
}
