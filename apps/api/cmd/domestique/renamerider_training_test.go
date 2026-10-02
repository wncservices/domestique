package main

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func countWhere(t *testing.T, dsn, table, column, value string) int {
	t.Helper()
	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := workout.UseDB(db.Conn(), db.DSN()); err != nil {
		t.Fatal(err)
	}
	var n int
	// #nosec G701 -- table and column are constants from this file.
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ?`, value).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The bug: rename-rider moved routes, accounts, sync state and sign-ins but
// left every training table under the old name, so the rider's goals, plan,
// HRV and sleep were stranded.
func TestRenameRiderMovesTrainingDataToo(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workout.UseDB(db.Conn(), db.DSN()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO goals (id, rider, name, created_at, updated_at) VALUES ('g1', 'wilant', 'Goal', 't', 't')`,
		`INSERT INTO rider_profiles (rider, updated_at) VALUES ('wilant', 't')`,
		`INSERT INTO daily_wellness (rider, date, hrv_last_night, updated_at) VALUES ('wilant', '2026-01-02', 61, 't')`,
	} {
		if _, err := db.Conn().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	out := mustRun(t, "rename-rider", "wilant", "auth0|64f2a1b2c3d4e5f6")
	if !strings.Contains(out, "daily_wellness") {
		t.Errorf("output does not report the training tables it moved:\n%s", out)
	}

	for _, table := range []string{"goals", "rider_profiles", "daily_wellness"} {
		if n := countWhere(t, dsn, table, "rider", "wilant"); n != 0 {
			t.Errorf("%s: %d rows left under the old rider", table, n)
		}
		if n := countWhere(t, dsn, table, "rider", "auth0|64f2a1b2c3d4e5f6"); n != 1 {
			t.Errorf("%s: %d rows under the new rider, want 1", table, n)
		}
	}
}

// A collision in a training table is refused, with the old rider's data left
// exactly where it was.
func TestRenameRiderRefusesATrainingCollision(t *testing.T) {
	dir := workspace(t)
	dsn := dir + "/data/domestique.db"
	seedRider(t, dsn, "wilant")

	db, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workout.UseDB(db.Conn(), db.DSN()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO rider_profiles (rider, updated_at) VALUES ('wilant', 't')`,
		`INSERT INTO rider_profiles (rider, updated_at) VALUES ('newname', 't')`,
	} {
		if _, err := db.Conn().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	if out, err := capture(t, "rename-rider", "wilant", "newname"); err == nil {
		t.Fatalf("rename onto a rider with a profile succeeded:\n%s", out)
	} else if !strings.Contains(err.Error(), "rider_profiles") {
		t.Errorf("error = %v, want it to name rider_profiles", err)
	}
	if n := countWhere(t, dsn, "rider_profiles", "rider", "wilant"); n != 1 {
		t.Errorf("the old rider's profile is gone after a refused rename (%d)", n)
	}
	// The account was not moved either: one transaction, nothing half-done.
	if n := countWhere(t, dsn, "accounts", "rider", "wilant"); n != 1 {
		t.Errorf("accounts moved although the rename was refused (%d)", n)
	}
}
