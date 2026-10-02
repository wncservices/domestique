package sessions

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/dbx"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// eachEngine runs a test against SQLite and, when DOMESTIQUE_TEST_POSTGRES is
// set, PostgreSQL in a schema of its own. conn is a fresh database each time.
func eachEngine(t *testing.T, run func(t *testing.T, conn *sql.DB, dsn string)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		db, err := source.OpenDB(filepath.Join(t.TempDir(), "s.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		run(t, db.Conn(), db.DSN())
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv(postgresEnv)
		if dsn == "" {
			t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
		}
		name := fmt.Sprintf("sessions_%d", time.Now().UnixNano())
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
		db, err := source.OpenDB(dsn + "&search_path=" + name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		run(t, db.Conn(), db.DSN())
	})
}

func open(t *testing.T, conn *sql.DB, dsn string, box *secrets.Box) *Store {
	t.Helper()
	s, err := UseDB(conn, dsn, box)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeleteRiderEndsEveryOfThatRidersSessionsOnly(t *testing.T) {
	eachEngine(t, func(t *testing.T, conn *sql.DB, dsn string) {
		s := open(t, conn, dsn, newBox(t))
		phone, _, _ := s.Create(auth.Identity{User: "gone", Sub: "auth0|gone"}, time.Hour)
		laptop, _, _ := s.Create(auth.Identity{User: "gone", Sub: "auth0|gone"}, time.Hour)
		friend, _, _ := s.Create(auth.Identity{User: "friend", Sub: "auth0|friend"}, time.Hour)

		// The caller's spelling must not matter: a rider is lower-cased and
		// trimmed everywhere else, so an admin typing "Gone " must still hit.
		n, err := s.DeleteRider(t.Context(), " Gone ")
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("deleted %d sessions, want 2", n)
		}
		for name, tok := range map[string]string{"phone": phone, "laptop": laptop} {
			if _, ok := s.Lookup(tok); ok {
				t.Errorf("%s session still valid after the rider was removed", name)
			}
		}
		if _, ok := s.Lookup(friend); !ok {
			t.Error("another rider's session was ended")
		}

		// Repeatable, and an unknown rider is not an error.
		if n, err := s.DeleteRider(t.Context(), "gone"); err != nil || n != 0 {
			t.Errorf("second DeleteRider = %d, %v", n, err)
		}
		if err := s.DeleteSub(t.Context(), "auth0|nobody"); err != nil {
			t.Error(err)
		}
	})
}

func TestDeleteSubEndsThatIdentitysSessions(t *testing.T) {
	eachEngine(t, func(t *testing.T, conn *sql.DB, dsn string) {
		s := open(t, conn, dsn, newBox(t))
		mine, _, _ := s.Create(auth.Identity{User: "gone", Sub: "auth0|gone"}, time.Hour)
		other, _, _ := s.Create(auth.Identity{User: "friend", Sub: "auth0|friend"}, time.Hour)
		noSub, _, _ := s.Create(auth.Identity{User: "friend"}, time.Hour)

		if err := s.DeleteSub(t.Context(), "auth0|gone"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Lookup(mine); ok {
			t.Error("session of the removed identity still valid")
		}
		for _, tok := range []string{other, noSub} {
			if _, ok := s.Lookup(tok); !ok {
				t.Error("an unrelated session was ended")
			}
		}
		// An empty sub must never match the sessions that have none.
		if err := s.DeleteSub(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Lookup(noSub); !ok {
			t.Error("DeleteSub(\"\") ended sessions that carry no sub")
		}
	})
}

// The keys are what the table holds instead of the name; neither may be the
// name, and they must not be shared between the two columns.
func TestRiderKeyIsNotTheRiderName(t *testing.T) {
	eachEngine(t, func(t *testing.T, conn *sql.DB, dsn string) {
		s := open(t, conn, dsn, newBox(t))
		if _, _, err := s.Create(auth.Identity{User: "wilant", Sub: "auth0|abc"}, time.Hour); err != nil {
			t.Fatal(err)
		}
		var rk, sk string
		if err := conn.QueryRow(`SELECT rider_key, sub_key FROM sessions`).Scan(&rk, &sk); err != nil {
			t.Fatal(err)
		}
		if rk == "" || rk == "wilant" || sk == "" || sk == "auth0|abc" || rk == sk {
			t.Errorf("rider_key = %q, sub_key = %q", rk, sk)
		}
	})
}

// legacySchema is the table as it was before rider_key existed.
func legacySchema(d dbx.Dialect) string {
	blob := "BLOB"
	if d.Name == dbx.Postgres.Name {
		blob = "BYTEA"
	}
	return `CREATE TABLE sessions (token TEXT PRIMARY KEY, identity ` + blob + ` NOT NULL, created_at TEXT NOT NULL, expires_at TEXT NOT NULL)`
}

// Rows written before the keys existed carry no key, so DeleteRider could not
// find them: a removed rider's old login would outlive the removal. UseDB
// backfills them from the sealed identity, once, and rows it cannot read
// (wrong key, tampered) are dropped since nobody could use them anyway.
func TestLegacyRowsAreBackfilledAndMigrationIsIdempotent(t *testing.T) {
	eachEngine(t, func(t *testing.T, conn *sql.DB, dsn string) {
		d, err := dbx.For(dsn)
		if err != nil {
			t.Fatal(err)
		}
		box := newBox(t)
		if _, err := conn.Exec(legacySchema(d)); err != nil {
			t.Fatal(err)
		}
		insert := func(tok string, sealed []byte) {
			t.Helper()
			future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
			if _, err := conn.Exec(d.Rebind(`INSERT INTO sessions (token, identity, created_at, expires_at) VALUES (?, ?, ?, ?)`),
				hashToken(tok), sealed, future, future); err != nil {
				t.Fatal(err)
			}
		}
		seal := func(user, sub string) []byte {
			b, err := box.Seal(`{"user":"` + user + `","sub":"` + sub + `"}`)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		insert("old-gone", seal("gone", "auth0|gone"))
		insert("old-friend", seal("friend", "auth0|friend"))
		insert("old-junk", []byte("not sealed data"))

		for range 3 { // idempotent: every start runs it again
			s := open(t, conn, dsn, box)
			var n int
			if err := conn.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 2 {
				t.Fatalf("%d rows after migration, want 2 (the undecryptable one dropped)", n)
			}
			if _, ok := s.Lookup("old-gone"); !ok {
				t.Fatal("a legacy session stopped working after the migration")
			}
		}

		s := open(t, conn, dsn, box)
		if n, err := s.DeleteRider(t.Context(), "gone"); err != nil || n != 1 {
			t.Fatalf("DeleteRider on a legacy row = %d, %v, want 1", n, err)
		}
		if _, ok := s.Lookup("old-gone"); ok {
			t.Error("legacy session of the removed rider is still valid")
		}
		if _, ok := s.Lookup("old-friend"); !ok {
			t.Error("legacy session of another rider was ended")
		}
	})
}

// Without a key nothing can be decrypted, so nothing can be backfilled, and
// the migration must not delete rows it merely could not read.
func TestMigrationWithoutAKeyLeavesLegacyRowsAlone(t *testing.T) {
	eachEngine(t, func(t *testing.T, conn *sql.DB, dsn string) {
		d, _ := dbx.For(dsn)
		if _, err := conn.Exec(legacySchema(d)); err != nil {
			t.Fatal(err)
		}
		future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		if _, err := conn.Exec(d.Rebind(`INSERT INTO sessions (token, identity, created_at, expires_at) VALUES ('t', ?, ?, ?)`),
			[]byte("sealed"), future, future); err != nil {
			t.Fatal(err)
		}
		open(t, conn, dsn, nil)
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 1 {
			t.Errorf("rows = %d, %v, want the row kept", n, err)
		}
	})
}
