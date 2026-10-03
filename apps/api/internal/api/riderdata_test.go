package api

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/garminmfa"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/routeshare"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/weather"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// riderDataEnv is one database with every store that owns a rider-keyed
// table opened on it, which is what the deployment looks like.
type riderDataEnv struct {
	srv *Server
	db  *sql.DB
	d   dbx.Dialect
}

func openRiderDataEnv(t *testing.T, dsn string) *riderDataEnv {
	t.Helper()
	src, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { src.Close() })
	conn, dsnUsed := src.Conn(), src.DSN()
	d, err := dbx.For(dsnUsed)
	if err != nil {
		t.Fatal(err)
	}

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{Source: src}
	if srv.Accounts, err = accounts.UseDB(conn, dsnUsed); err != nil {
		t.Fatal(err)
	}
	if srv.Store, err = state.UseDB(conn, dsnUsed); err != nil {
		t.Fatal(err)
	}
	// A deployment that predates provider_links still has komoot_links, which
	// the migration leaves behind on purpose; it holds a sealed Komoot token.
	blob := "BLOB"
	if d.Name == dbx.Postgres.Name {
		blob = "BYTEA"
	}
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS komoot_links (
    rider TEXT PRIMARY KEY, email TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL, token ` + blob + ` NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if srv.Links, err = providerlink.UseDB(conn, dsnUsed, box); err != nil {
		t.Fatal(err)
	}
	if srv.GarminMFA, err = garminmfa.UseDB(conn, dsnUsed, box); err != nil {
		t.Fatal(err)
	}
	if srv.Crew, err = crew.UseDB(conn, dsnUsed); err != nil {
		t.Fatal(err)
	}
	if srv.Schedule, err = schedule.UseDB(conn, dsnUsed); err != nil {
		t.Fatal(err)
	}
	if srv.Shares, err = routeshare.UseDB(conn, dsnUsed); err != nil {
		t.Fatal(err)
	}
	if srv.WeatherPrefs, err = weather.UseDB(conn, dsnUsed); err != nil {
		t.Fatal(err)
	}
	if srv.Training, err = workout.UseDB(conn, dsnUsed); err != nil {
		t.Fatal(err)
	}
	if srv.Sessions, err = sessions.UseDB(conn, dsnUsed, box); err != nil {
		t.Fatal(err)
	}
	if _, err = settings.UseDB(conn, dsnUsed, box); err != nil {
		t.Fatal(err)
	}
	return &riderDataEnv{srv: srv, db: conn, d: d}
}

func eachRiderDataEngine(t *testing.T, run func(t *testing.T, env *riderDataEnv)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		run(t, openRiderDataEnv(t, filepath.Join(t.TempDir(), "rider.db")))
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("DOMESTIQUE_TEST_POSTGRES")
		if dsn == "" {
			t.Skip("set DOMESTIQUE_TEST_POSTGRES to a PostgreSQL DSN to run this")
		}
		// A schema of its own: the shared test database also holds tables
		// other branches' runs left behind, which the guard would report.
		name := fmt.Sprintf("purge_%d", time.Now().UnixNano())
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(`CREATE SCHEMA ` + name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(`DROP SCHEMA ` + name + ` CASCADE`)
			_ = admin.Close()
		})
		run(t, openRiderDataEnv(t, dsn+"&search_path="+name))
	})
}

// riderColumns are the columns that tie a row to a rider. idColumns tie it to
// something a rider owns, so the row is theirs by one step removed.
var (
	riderColumns = []string{"rider", "created_by", "owner", "uploaded_by", "decided_by", "updated_by", "added_by"}
	idColumns    = []string{"workout_id", "session_id", "goal_id", "account_id", "rider_key"}
)

// TestEveryRiderKeyedTableIsRegistered is the guard: it reads the real schema
// after every store has migrated, and fails for a table with a rider column
// (or one keyed through a rider's id) that riderTables does not list. Adding a
// table that holds rider data means adding it to riderTables, and then
// teaching Training.DeleteRider (or the store's own purge) to delete from it.
func TestEveryRiderKeyedTableIsRegistered(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		found := riderKeyedTables(t, env)
		var missing []string
		for table := range found {
			if _, ok := riderTables[table]; !ok {
				missing = append(missing, table)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("tables with a rider column not registered in riderTables (see riderdata.go): %v", missing)
		}
		// And the other way round: a registry entry for a table that no longer
		// exists is a rename the purge did not follow.
		for table := range riderTables {
			if !found[table] {
				t.Errorf("riderTables lists %q, which has no rider-keyed column in the schema", table)
			}
		}
	})
}

func riderKeyedTables(t *testing.T, env *riderDataEnv) map[string]bool {
	t.Helper()
	var q string
	if env.d.Name == dbx.Postgres.Name {
		q = `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema()`
	} else {
		q = `SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table'`
	}
	rows, err := env.db.Query(q)
	if err != nil {
		t.Fatalf("reading schema: %v", err)
	}
	defer func() { _ = rows.Close() }()
	want := map[string]bool{}
	for _, c := range riderColumns {
		want[c] = true
	}
	for _, c := range idColumns {
		want[c] = true
	}
	out := map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		if want[column] {
			out[table] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seed is one table's rows for one rider. probe is how the test finds them
// again afterwards: a column and the value this rider's row carries there.
type seed struct {
	table string
	// insert runs with the rider and an id unique to them.
	insert func(env *riderDataEnv, rider, id string) error
	// probeColumn is where the rider (or their id) shows up.
	probeColumn string
	// probe is the value to look for in probeColumn.
	probe func(rider, id string) string
}

// envProbes is probe for the tables whose value only the environment can
// compute (a keyed stand-in for the rider, say); an entry wins over probe.
var envProbes = map[string]func(e *riderDataEnv, rider string) string{
	"sessions": func(e *riderDataEnv, rider string) string { return e.srv.Sessions.RiderKey(rider) },
}

func (s seed) probeValue(e *riderDataEnv, rider, id string) string {
	if f := envProbes[s.table]; f != nil {
		return f(e, rider)
	}
	return s.probe(rider, id)
}

func byRider(rider, _ string) string { return rider }

func exec(env *riderDataEnv, q string, args ...any) error {
	_, err := env.db.Exec(env.d.Rebind(q), args...)
	return err
}

const ts = "2026-01-02T03:04:05Z"

func riderSeeds() []seed {
	return []seed{
		{"routes", func(e *riderDataEnv, rider, id string) error {
			_, err := e.srv.Source.Create(context.Background(), source.CreateRequest{Name: "Loop " + id, GPX: minimalGPX, UploadedBy: rider})
			return err
		}, "uploaded_by", byRider},
		{"accounts", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO accounts (id, provider, rider, created_at, updated_at) VALUES (?, 'garmin', ?, ?, ?)`, "garmin:"+rider, rider, ts, ts)
		}, "rider", byRider},
		{"sync_state", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO sync_state (account_id, slug, remote_id, content_hash, updated_at) VALUES (?, 'a-route', 'r', 'h', ?)`, "garmin:"+rider, ts)
		}, "account_id", func(rider, _ string) string { return "garmin:" + rider }},
		{"provider_links", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO provider_links (provider, rider, email, secret, updated_at) VALUES ('garmin', ?, 'x@example.com', ?, ?)`, rider, []byte("sealed"), ts)
		}, "rider", byRider},
		{"komoot_links", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO komoot_links (rider, email, user_id, token, updated_at) VALUES (?, 'x@example.com', 'u', ?, ?)`, rider, []byte("sealed"), ts)
		}, "rider", byRider},
		{"garmin_mfa_challenges", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO garmin_mfa_challenges (token, rider, state, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`, "tok-"+id, rider, []byte("state"), ts, ts)
		}, "rider", byRider},
		{"crew_members", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO crew_members (crew_id, rider, status, requested_at) VALUES (?, ?, 'approved', ?)`, "crew-"+id, rider, ts)
		}, "rider", byRider},
		{"route_shares", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO route_shares (id, route_slug, created_by, created_at, expires_at) VALUES (?, 'a-route', ?, ?, ?)`, "share-"+id, rider, ts, ts)
		}, "created_by", byRider},
		{"route_share_redemptions", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO route_share_redemptions (share_id, rider, redeemed_at) VALUES (?, ?, ?)`, "someone-elses-share-"+id, rider, ts)
		}, "rider", byRider},
		{"weather_locations", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO weather_locations (rider, place, lat, lon, updated_at) VALUES (?, 'Ghent', 51.05, 3.72, ?)`, rider, ts)
		}, "rider", byRider},
		{"goals", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO goals (id, rider, name, created_at, updated_at) VALUES (?, ?, 'Goal', ?, ?)`, "goal-"+id, rider, ts, ts)
		}, "rider", byRider},
		{"rider_profiles", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO rider_profiles (rider, updated_at) VALUES (?, ?)`, rider, ts)
		}, "rider", byRider},
		{"workouts", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO workouts (id, rider, name, goal_id, steps, created_at, updated_at) VALUES (?, ?, 'W', ?, ?, ?, ?)`, "workout-"+id, rider, "goal-"+id, []byte("[]"), ts, ts)
		}, "rider", byRider},
		{"workout_pushes", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO workout_pushes (workout_id, provider, remote_id, content_hash, pushed_at) VALUES (?, 'garmin', 'r', 'h', ?)`, "workout-"+id, ts)
		}, "workout_id", func(_, id string) string { return "workout-" + id }},
		{"scheduled_weeks", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO scheduled_weeks (goal_id, week_start, created_at) VALUES (?, '2026-01-05', ?)`, "goal-"+id, ts)
		}, "goal_id", func(_, id string) string { return "goal-" + id }},
		{"completed_sessions", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO completed_sessions (id, rider, provider, external_id, date, created_at) VALUES (?, ?, 'garmin', ?, '2026-01-02', ?)`, "session-"+id, rider, "ext-"+id, ts)
		}, "rider", byRider},
		{"session_analyses", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO session_analyses (session_id, rider, analysed_at) VALUES (?, ?, ?)`, "session-"+id, rider, ts)
		}, "rider", byRider},
		{"session_links", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO session_links (session_id, rider, created_at) VALUES (?, ?, ?)`, "session-"+id, rider, ts)
		}, "rider", byRider},
		{"fitness_snapshots", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO fitness_snapshots (rider, date) VALUES (?, '2026-01-02')`, rider)
		}, "rider", byRider},
		{"daily_wellness", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO daily_wellness (rider, date, hrv_last_night, sleep_seconds, resting_hr, updated_at) VALUES (?, '2026-01-02', 61, 27000, 48, ?)`, rider, ts)
		}, "rider", byRider},
		{"progression_levels", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO progression_levels (rider, sport, zone, level, updated_at) VALUES (?, 'cycling', 'threshold', 4, ?)`, rider, ts)
		}, "rider", byRider},
		{"progression_history", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO progression_history (rider, sport, zone, level, at) VALUES (?, 'cycling', 'threshold', 4, ?)`, rider, ts)
		}, "rider", byRider},
		{"threshold_suggestions", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO threshold_suggestions (id, rider, field, created_at, updated_at) VALUES (?, ?, 'ftp', ?, ?)`, "sugg-"+id, rider, ts, ts)
		}, "rider", byRider},
		{"life_events", func(e *riderDataEnv, rider, id string) error {
			return exec(e, `INSERT INTO life_events (id, rider, kind, start_date, end_date, created_at, updated_at) VALUES (?, ?, 'illness', '2026-01-02', '2026-01-04', ?, ?)`, "event-"+id, rider, ts, ts)
		}, "rider", byRider},
		{"adjustments", func(e *riderDataEnv, rider, id string) error {
			rec := why.NewRecord(why.MissedMoved, "moved", why.MissedMovedInputs{})
			return e.srv.Training.RecordAdjustment(context.Background(), rider, workout.SubjectWorkout, "workout-"+id, rec, "2026-01-02")
		}, "rider", byRider},
		{"sessions", func(e *riderDataEnv, rider, id string) error {
			// Two sessions, as a rider on a phone and a laptop has.
			for range 2 {
				if _, _, err := e.srv.Sessions.Create(auth.Identity{User: rider, Sub: "auth0|" + rider}, time.Hour); err != nil {
					return err
				}
			}
			return nil
		}, "rider_key", nil},
	}
}

func count(t *testing.T, env *riderDataEnv, table, column, value string) int {
	t.Helper()
	var n int
	// #nosec G701 -- table and column are constants from this file.
	if err := env.db.QueryRow(env.d.Rebind(`SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ?`), value).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

// TestPurgeRemovesEveryRidersData seeds a row in every table registered as
// purged for the rider who is leaving and for one who stays, purges the first,
// and checks the first is gone from every table and the second untouched.
func TestPurgeRemovesEveryRidersData(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		gone, stays := "gone"+tok, "stays"+tok
		goneID, staysID := "g"+tok, "s"+tok

		seeds := riderSeeds()
		seeded := map[string]bool{}
		for _, s := range seeds {
			seeded[s.table] = true
			if err := s.insert(env, gone, goneID); err != nil {
				t.Fatalf("seeding %s for %s: %v", s.table, gone, err)
			}
			if err := s.insert(env, stays, staysID); err != nil {
				t.Fatalf("seeding %s for %s: %v", s.table, stays, err)
			}
		}
		for table, rt := range riderTables {
			if rt.Purged && !seeded[table] {
				t.Errorf("%s is registered as purged but this test has no seed for it", table)
			}
		}

		staysBefore := map[string]int{}
		for _, s := range seeds {
			staysBefore[s.table] = count(t, env, s.table, s.probeColumn, s.probeValue(env, stays, staysID))
			if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, gone, goneID)); n == 0 {
				t.Fatalf("%s: seed for the leaving rider did not land", s.table)
			}
		}

		if _, err := env.srv.purgeRiderData(context.Background(), gone); err != nil {
			t.Fatalf("purge: %v", err)
		}

		for _, s := range seeds {
			if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, gone, goneID)); n != 0 {
				t.Errorf("%s: %d rows left for the removed rider", s.table, n)
			}
			if n := count(t, env, s.table, s.probeColumn, s.probeValue(env, stays, staysID)); n == 0 || n != staysBefore[s.table] {
				t.Errorf("%s: %d rows for the rider who stays, want %d", s.table, n, staysBefore[s.table])
			}
		}
	})
}

// A crew's rides and recurring series are the crew's plan: a purge keeps the
// rows and blanks the name of the rider who scheduled them.
func TestPurgeKeepsCrewPlansButBlanksTheAuthor(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		gone := "author" + tok
		if err := exec(env, `INSERT INTO crew_rides (id, crew_id, route_slug, date, created_by, created_at) VALUES (?, 'c', 'r', '2026-02-01', ?, ?)`, "ride-"+tok, gone, ts); err != nil {
			t.Fatal(err)
		}
		if err := exec(env, `INSERT INTO ride_series (id, crew_id, route_slug, interval_weeks, created_by, created_at) VALUES (?, 'c', 'r', 1, ?, ?)`, "series-"+tok, gone, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := env.srv.purgeRiderData(context.Background(), gone); err != nil {
			t.Fatal(err)
		}
		for table, id := range map[string]string{"crew_rides": "ride-" + tok, "ride_series": "series-" + tok} {
			if n := count(t, env, table, "created_by", gone); n != 0 {
				t.Errorf("%s: author still named on %d rows", table, n)
			}
			if n := count(t, env, table, "id", id); n != 1 {
				t.Errorf("%s: row for the crew's plan is gone (%d)", table, n)
			}
		}
	})
}

// A purge run twice is safe, which is what lets a retry after a partial
// failure finish the job.
func TestPurgeIsRepeatable(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		rider := "again" + tok
		for _, s := range riderSeeds() {
			if err := s.insert(env, rider, "x"+tok); err != nil {
				t.Fatalf("seeding %s: %v", s.table, err)
			}
		}
		for range 2 {
			if _, err := env.srv.purgeRiderData(context.Background(), rider); err != nil {
				t.Fatalf("purge: %v", err)
			}
		}
	})
}
