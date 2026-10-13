package identities

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

const (
	postgresEnv = "DOMESTIQUE_TEST_POSTGRES"
	issuer      = "https://issuer.test/"
)

// fixed is the store clock: an explicit zone, so the test means the same in
// every TZ.
var fixed = time.Date(2026, 10, 13, 9, 30, 0, 0, time.FixedZone("CEST", 2*3600))

type env struct {
	store *Store
	db    *source.DB
	box   *secrets.Box
}

func newBox(t *testing.T) *secrets.Box {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func openDB(t *testing.T, dsn string) *source.DB {
	t.Helper()
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func openStore(t *testing.T, dsn string) env {
	t.Helper()
	db := openDB(t, dsn)
	box := newBox(t)
	store, err := UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	store.Clock = func() time.Time { return fixed }
	// Postgres tests share a database; start clean.
	for _, q := range []string{`DELETE FROM rider_identities`, `CREATE TABLE IF NOT EXISTS idtest_things (owner TEXT)`, `DELETE FROM idtest_things`} {
		if _, err := db.Conn().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return env{store: store, db: db, box: box}
}

func eachEngine(t *testing.T, run func(t *testing.T, e env)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		run(t, openStore(t, filepath.Join(t.TempDir(), "identities.db")))
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv(postgresEnv)
		if dsn == "" {
			t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
		}
		run(t, openStore(t, dsn))
	})
}

func countWhere(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	// #nosec G701 -- table is a constant in this file.
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBindAndGet(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		ok, err := e.store.Bind(ctx, issuer, "auth0|1", "Wilant ", e.store.EmailMAC("A@Example.com"))
		if err != nil || !ok {
			t.Fatalf("Bind = %v, %v; want true", ok, err)
		}
		b, found, err := e.store.Get(ctx, issuer, "auth0|1")
		if err != nil || !found {
			t.Fatalf("Get = %v, %v", found, err)
		}
		if b.Rider != "wilant" || !b.HasEmail {
			t.Errorf("binding = %+v, want rider wilant with an email recorded", b)
		}
		if !b.CreatedAt.Equal(fixed) || !b.LastSeenAt.Equal(fixed) {
			t.Errorf("times = %v / %v, want the store clock %v", b.CreatedAt, b.LastSeenAt, fixed)
		}

		// A second bind of the same account changes nothing: only an admin moves one.
		ok, err = e.store.Bind(ctx, issuer, "auth0|1", "someone-else", "")
		if err != nil || ok {
			t.Fatalf("second Bind = %v, %v; want false, nil", ok, err)
		}
		if b, _, _ = e.store.Get(ctx, issuer, "auth0|1"); b.Rider != "wilant" {
			t.Errorf("rider after a second bind = %q, want wilant", b.Rider)
		}

		// The same sub under another issuer is a different account.
		if _, found, _ := e.store.Get(ctx, "https://other.test/", "auth0|1"); found {
			t.Error("a binding for one issuer answered for another")
		}
		if _, found, _ := e.store.Get(ctx, issuer, "auth0|unknown"); found {
			t.Error("an unknown sub has a binding")
		}
	})
}

func TestSeenRefreshesTheClockAndTheEmail(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		if _, err := e.store.Bind(ctx, issuer, "g|1", "wilant", e.store.EmailMAC("old@example.com")); err != nil {
			t.Fatal(err)
		}
		later := fixed.Add(48 * time.Hour)
		e.store.Clock = func() time.Time { return later }

		if err := e.store.Seen(ctx, issuer, "g|1", e.store.EmailMAC("new@example.com")); err != nil {
			t.Fatal(err)
		}
		b, _, _ := e.store.Get(ctx, issuer, "g|1")
		if !b.LastSeenAt.Equal(later) || !b.CreatedAt.Equal(fixed) {
			t.Errorf("times = created %v seen %v, want created %v seen %v", b.CreatedAt, b.LastSeenAt, fixed, later)
		}
		if got, _ := e.store.RidersByEmail(ctx, issuer, e.store.EmailMAC("old@example.com")); len(got) != 0 {
			t.Errorf("an address the account no longer asserts still matches: %v", got)
		}
		if got, _ := e.store.RidersByEmail(ctx, issuer, e.store.EmailMAC("new@example.com")); len(got) != 1 {
			t.Errorf("the address the account asserts now matches %v, want one rider", got)
		}

		// Asserting no verified email clears it.
		if err := e.store.Seen(ctx, issuer, "g|1", ""); err != nil {
			t.Fatal(err)
		}
		if b, _, _ = e.store.Get(ctx, issuer, "g|1"); b.HasEmail {
			t.Error("an account that asserts no verified email still has one recorded")
		}
	})
}

func TestEmailMAC(t *testing.T) {
	box := newBox(t)
	s := &Store{box: box}
	if s.EmailMAC("A@Example.com ") != s.EmailMAC("a@example.com") {
		t.Error("the MAC depends on case or surrounding space")
	}
	if s.EmailMAC("a@example.com") == s.EmailMAC("b@example.com") {
		t.Error("two addresses share a MAC")
	}
	if s.EmailMAC("a@example.com") == box.MAC("something-else", "a@example.com") {
		t.Error("the MAC is not under its own purpose")
	}
	if got := s.EmailMAC("a@example.com"); strings.Contains(got, "example") {
		t.Errorf("the MAC leaks the address: %q", got)
	}
	if s.EmailMAC("  ") != "" {
		t.Error("an empty address has a MAC")
	}
	if (&Store{}).EmailMAC("a@example.com") != "" {
		t.Error("a store with no key produced a MAC")
	}
}

func TestTheAddressIsNotStored(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		if _, err := e.store.Bind(ctx, issuer, "g|1", "wilant", e.store.EmailMAC("secret.person@example.com")); err != nil {
			t.Fatal(err)
		}
		var issuerCol, sub, rider, mac, created, seen string
		if err := e.db.Conn().QueryRow(`SELECT issuer, sub, rider, email_mac, created_at, last_seen_at FROM rider_identities`).
			Scan(&issuerCol, &sub, &rider, &mac, &created, &seen); err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{issuerCol, sub, rider, mac, created, seen} {
			if strings.Contains(strings.ToLower(v), "secret.person") {
				t.Errorf("the address is in the table: %q", v)
			}
		}
		if len(mac) != 64 {
			t.Errorf("email_mac = %q, want a hex HMAC-SHA256", mac)
		}
	})
}

func TestRidersByEmailIsDistinctAndEmptyMatchesNothing(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		mac := e.store.EmailMAC("a@example.com")
		for _, b := range []struct{ sub, rider, mac string }{
			{"g|1", "wilant", mac}, {"a|1", "wilant", mac}, {"g|2", "sam", mac}, {"g|3", "robin", ""}, {"g|4", "pat", ""},
		} {
			if _, err := e.store.Bind(ctx, issuer, b.sub, b.rider, b.mac); err != nil {
				t.Fatal(err)
			}
		}
		got, err := e.store.RidersByEmail(ctx, issuer, mac)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != "sam,wilant" {
			t.Errorf("riders = %v, want [sam wilant] once each", got)
		}
		if got, _ := e.store.RidersByEmail(ctx, issuer, ""); len(got) != 0 {
			t.Errorf("an empty MAC matched %v", got)
		}
	})
}

func TestMoveUnbindListAndDelete(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		for _, b := range []struct{ sub, rider string }{{"g|1", "wilant"}, {"a|1", "wilant"}, {"g|2", "sam"}} {
			if _, err := e.store.Bind(ctx, issuer, b.sub, b.rider, ""); err != nil {
				t.Fatal(err)
			}
		}
		if bound, _ := e.store.RiderBound(ctx, "WILANT"); !bound {
			t.Error("RiderBound is case sensitive")
		}
		if err := e.store.Move(ctx, issuer, "g|2", "Robin"); err != nil {
			t.Fatal(err)
		}
		if b, _, _ := e.store.Get(ctx, issuer, "g|2"); b.Rider != "robin" {
			t.Errorf("after Move rider = %q, want robin", b.Rider)
		}
		if err := e.store.Move(ctx, issuer, "g|none", "robin"); err != ErrNotBound {
			t.Errorf("Move of an unbound account = %v, want ErrNotBound", err)
		}
		if subs, _ := e.store.RidersOfSub(ctx, issuer, "g|1"); strings.Join(subs, ",") != "wilant" {
			t.Errorf("RidersOfSub = %v", subs)
		}
		all, err := e.store.List(ctx)
		if err != nil || len(all) != 3 {
			t.Fatalf("List = %d bindings, %v; want 3", len(all), err)
		}

		if n, err := e.store.DeleteRider(ctx, "wilant"); err != nil || n != 2 {
			t.Fatalf("DeleteRider = %d, %v; want 2", n, err)
		}
		if bound, _ := e.store.RiderBound(ctx, "wilant"); bound {
			t.Error("a deleted rider is still bound")
		}
		if ok, err := e.store.Unbind(ctx, issuer, "g|2"); err != nil || !ok {
			t.Fatalf("Unbind = %v, %v", ok, err)
		}
		if ok, _ := e.store.Unbind(ctx, issuer, "g|2"); ok {
			t.Error("Unbind of nothing reported a removal")
		}
		if n := countWhere(t, e.db.Conn(), "rider_identities"); n != 0 {
			t.Errorf("%d bindings left", n)
		}
	})
}

func taken() TakenColumns {
	return TakenColumns{
		"idtest_things":    {"owner"},
		"no_such_table":    {"rider"}, // a feature this deployment never created
		"rider_identities": {"rider"}, // bound to somebody
		"idtest_missing":   {"no_such_column"},
	}
}

func TestClaimFree(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		claim := func(sub, candidate string) Claim {
			t.Helper()
			c, err := e.store.ClaimFree(ctx, issuer, sub, "", candidate, taken())
			if err != nil {
				t.Fatalf("ClaimFree(%s, %s): %v", sub, candidate, err)
			}
			return c
		}

		if c := claim("g|1", "Wilant"); c.Rider != "wilant" || c.Suffixed || c.Existing {
			t.Fatalf("first claim = %+v, want a plain wilant", c)
		}
		// Bound to another sub: -2, then -3.
		if c := claim("g|2", "wilant"); c.Rider != "wilant-2" || !c.Suffixed {
			t.Errorf("second claim = %+v, want wilant-2", c)
		}
		if c := claim("g|3", "wilant"); c.Rider != "wilant-3" || !c.Suffixed {
			t.Errorf("third claim = %+v, want wilant-3", c)
		}
		// A rider present in a registered table, with no binding at all, is taken.
		if _, err := e.db.Conn().Exec(`INSERT INTO idtest_things (owner) VALUES ('Sam')`); err != nil {
			t.Fatal(err)
		}
		if c := claim("g|4", "sam"); c.Rider != "sam-2" {
			t.Errorf("claim of a rider that only has data = %+v, want sam-2", c)
		}
		// The suffix is skipped over when it is taken too.
		if _, err := e.db.Conn().Exec(`INSERT INTO idtest_things (owner) VALUES ('robin'), ('robin-2')`); err != nil {
			t.Fatal(err)
		}
		if c := claim("g|5", "robin"); c.Rider != "robin-3" {
			t.Errorf("claim = %+v, want robin-3", c)
		}
		// An account that is already bound gets its binding back, whatever it asks for.
		if c := claim("g|1", "sam"); c.Rider != "wilant" || !c.Existing {
			t.Errorf("claim by a bound account = %+v, want its own wilant", c)
		}
		if n := countWhere(t, e.db.Conn(), "rider_identities"); n != 5 {
			t.Errorf("%d bindings, want 5", n)
		}
		if _, err := e.store.ClaimFree(ctx, issuer, "g|9", "", "  ", taken()); err == nil {
			t.Error("a blank candidate was accepted")
		}
	})
}

// Two sign-ins that want the same free name cannot both get it, and one
// account signing in twice at once is one binding.
func TestClaimFreeIsAtomic(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		got := map[string]string{}
		for _, sub := range []string{"g|a", "g|b", "g|c", "g|d"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, err := e.store.ClaimFree(ctx, issuer, sub, "", "newcomer", taken())
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				got[sub] = c.Rider
				mu.Unlock()
			}()
		}
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := e.store.ClaimFree(ctx, issuer, "g|twin", "", "twin", taken()); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		seen := map[string]bool{}
		for sub, rider := range got {
			if seen[rider] {
				t.Errorf("%s shares rider %s with another account", sub, rider)
			}
			seen[rider] = true
		}
		var twins int
		if err := e.db.Conn().QueryRow(`SELECT COUNT(*) FROM rider_identities WHERE sub = 'g|twin'`).Scan(&twins); err != nil || twins != 1 {
			t.Errorf("the account that signed in three times at once has %d bindings (%v), want 1", twins, err)
		}
	})
}

// The registry of rider-keyed tables is not this package's to know, so the
// migration is tested here with the real sessions store.
func TestTheMigrationEndsEverySessionOnce(t *testing.T) {
	eachEngine(t, func(t *testing.T, e env) {
		db := e.db
		ss, err := sessions.UseDB(db.Conn(), db.DSN(), e.box)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Conn().Exec(`DELETE FROM sessions`); err != nil {
			t.Fatal(err)
		}
		// An earlier version of the app: sessions exist and the marker does not.
		if _, err := db.Conn().Exec(`DELETE FROM identity_migrations`); err != nil {
			t.Fatal(err)
		}
		for _, who := range []string{"wilant", "sam"} {
			if _, _, err := ss.Create(auth.Identity{User: who, Sub: "auth0|" + who}, time.Hour); err != nil {
				t.Fatal(err)
			}
		}
		if n := countWhere(t, db.Conn(), "sessions"); n != 2 {
			t.Fatalf("seeded %d sessions, want 2", n)
		}

		if _, err := UseDB(db.Conn(), db.DSN(), e.box); err != nil {
			t.Fatal(err)
		}
		if n := countWhere(t, db.Conn(), "sessions"); n != 0 {
			t.Fatalf("%d sessions survived the migration, want 0", n)
		}

		// Someone signs in again through the binding; the next start leaves them alone.
		if _, _, err := ss.Create(auth.Identity{User: "wilant", Sub: "auth0|wilant"}, time.Hour); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := UseDB(db.Conn(), db.DSN(), e.box); err != nil {
				t.Fatal(err)
			}
		}
		if n := countWhere(t, db.Conn(), "sessions"); n != 1 {
			t.Errorf("%d sessions after later starts, want the 1 made since", n)
		}
		if n := countWhere(t, db.Conn(), "identity_migrations"); n != 1 {
			t.Errorf("%d markers, want 1", n)
		}
	})
}

// A database that has no sessions table (a mode that never made one) is not an
// error, and still gets the marker.
func TestTheMigrationToleratesNoSessionsTable(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "bare.db"))
	if _, err := db.Conn().Exec(`DROP TABLE IF EXISTS sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := UseDB(db.Conn(), db.DSN(), nil); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db.Conn(), "identity_migrations"); n != 1 {
		t.Errorf("%d markers, want 1", n)
	}
}
