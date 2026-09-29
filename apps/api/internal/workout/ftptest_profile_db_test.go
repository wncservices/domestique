package workout

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/source"
)

func utcToday() string { return time.Now().UTC().Format("2006-01-02") }

func TestFTPVerificationColumnsOnEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()

			p, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", HoursPerAvailableDay: 1})
			if err != nil {
				t.Fatal(err)
			}
			if p.FTPVerifiedAt != "" || p.FTPTestSnoozedUntil != "" {
				t.Errorf("new profile carries %q/%q, want empty", p.FTPVerifiedAt, p.FTPTestSnoozedUntil)
			}

			// Setting an FTP through SaveProfile is a verification: the rider
			// has just looked at the number.
			p, err = db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 250})
			if err != nil {
				t.Fatal(err)
			}
			if p.FTPVerifiedAt != utcToday() {
				t.Errorf("verified = %q after setting FTP, want today %s", p.FTPVerifiedAt, utcToday())
			}

			// A save that leaves FTP alone does not move it.
			if err := db.MarkFTPVerified(ctx, "wilant", "2030-01-01"); err != nil {
				t.Fatal(err)
			}
			p, _ = db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 250, MaxHR: 190})
			if p.FTPVerifiedAt != "2030-01-01" {
				t.Errorf("verified = %q after an unrelated save, want it untouched", p.FTPVerifiedAt)
			}

			// A save that changes FTP dates it to today, but never backwards.
			p, _ = db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 260, MaxHR: 190})
			if p.FTPVerifiedAt != "2030-01-01" {
				t.Errorf("verified moved backward to %q", p.FTPVerifiedAt)
			}
		})
	}
}

func TestMarkFTPVerifiedIsMonotonicAndSaveProfileNeverClobbersTheSnooze(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant"}); err != nil {
				t.Fatal(err)
			}

			for _, c := range []struct{ mark, want string }{
				{"2026-03-10", "2026-03-10"},
				{"2026-03-01", "2026-03-10"}, // earlier: ignored
				{"2026-03-10", "2026-03-10"}, // same: no change
				{"2026-03-15", "2026-03-15"},
			} {
				if err := db.MarkFTPVerified(ctx, "wilant", c.mark); err != nil {
					t.Fatal(err)
				}
				p, _, _ := db.GetProfile(ctx, "wilant")
				if p.FTPVerifiedAt != c.want {
					t.Errorf("after marking %s: verified = %q, want %s", c.mark, p.FTPVerifiedAt, c.want)
				}
			}

			if err := db.SnoozeFTPTest(ctx, "wilant", "2026-04-12"); err != nil {
				t.Fatal(err)
			}
			// A whole-profile save built from an older read must not revert
			// either marker: they have their own writers.
			if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", MaxHR: 185}); err != nil {
				t.Fatal(err)
			}
			p, _, _ := db.GetProfile(ctx, "wilant")
			if p.FTPTestSnoozedUntil != "2026-04-12" || p.FTPVerifiedAt != "2026-03-15" {
				t.Errorf("SaveProfile clobbered a marker: %+v", p)
			}

			// A rider with no profile row is a clean no-op, not an error.
			if err := db.MarkFTPVerified(ctx, "nobody", "2026-03-15"); err != nil {
				t.Errorf("marking a rider with no profile: %v", err)
			}
		})
	}
}

func TestSetTestResultWritesOnce(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *DB{"sqlite": openTestDB, "postgres": openTestPostgres} {
		t.Run(engine, func(t *testing.T) {
			db := open(t)
			ctx := t.Context()
			w, err := db.CreateWorkout(ctx, CreateWorkoutRequest{Rider: "wilant", Name: "FTP Test", TestProtocol: "ramp"})
			if err != nil {
				t.Fatal(err)
			}
			first, err := db.SetTestResult(ctx, w.ID, 312)
			if err != nil || !first {
				t.Fatalf("first write = %v, %v, want true", first, err)
			}
			second, err := db.SetTestResult(ctx, w.ID, 280)
			if err != nil || second {
				t.Fatalf("second write = %v, %v, want false", second, err)
			}
			got, _ := db.GetWorkout(ctx, w.ID)
			if got.TestResultWatts != 312 {
				t.Errorf("result = %v, want the first write's 312", got.TestResultWatts)
			}
		})
	}
}

func TestRiderProfilesTableGainsFTPVerificationColumnsAndBackfillsOnlyWhereEmpty(t *testing.T) {
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	if _, err := src.Conn().Exec(`
CREATE TABLE rider_profiles (
    rider TEXT PRIMARY KEY, ftp_watts DOUBLE PRECISION NOT NULL DEFAULT 0,
    ftp_estimated BOOLEAN NOT NULL DEFAULT FALSE, estimated_fields TEXT NOT NULL DEFAULT '',
    auto_push_workouts BOOLEAN NOT NULL DEFAULT FALSE,
    ftp_levels_calibrated_watts DOUBLE PRECISION NOT NULL DEFAULT 0,
    threshold_pace_sec_per_km DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_hr INTEGER NOT NULL DEFAULT 0, threshold_hr INTEGER NOT NULL DEFAULT 0,
    resting_hr INTEGER NOT NULL DEFAULT 0, available_days TEXT NOT NULL DEFAULT '',
    hours_per_available_day DOUBLE PRECISION NOT NULL DEFAULT 0,
    experience_level TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{
		`INSERT INTO rider_profiles (rider, ftp_watts, updated_at) VALUES ('with-ftp', 255, '2026-01-01T00:00:00Z')`,
		`INSERT INTO rider_profiles (rider, ftp_watts, updated_at) VALUES ('no-ftp', 0, '2026-01-01T00:00:00Z')`,
	} {
		if _, err := src.Conn().Exec(row); err != nil {
			t.Fatal(err)
		}
	}

	db, err := UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatalf("UseDB (migrate): %v", err)
	}
	withFTP, _, _ := db.GetProfile(t.Context(), "with-ftp")
	noFTP, _, _ := db.GetProfile(t.Context(), "no-ftp")
	if withFTP.FTPVerifiedAt != utcToday() {
		t.Errorf("existing FTP was backfilled to %q, want the deploy day %s so nobody is nagged at once", withFTP.FTPVerifiedAt, utcToday())
	}
	if noFTP.FTPVerifiedAt != "" {
		t.Errorf("a rider with no FTP was backfilled to %q", noFTP.FTPVerifiedAt)
	}

	// A later startup must not re-date a verification that has since moved.
	if err := db.MarkFTPVerified(t.Context(), "with-ftp", "2099-01-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE rider_profiles SET ftp_verified_at = '2026-02-02' WHERE rider = 'with-ftp'`); err != nil {
		t.Fatal(err)
	}
	if _, err := UseDB(src.Conn(), src.DSN()); err != nil {
		t.Fatalf("second UseDB: %v", err)
	}
	again, _, _ := db.GetProfile(t.Context(), "with-ftp")
	if again.FTPVerifiedAt != "2026-02-02" {
		t.Errorf("second startup rewrote a set date to %q", again.FTPVerifiedAt)
	}
}
