package pgtest

import (
	"database/sql"
	"testing"
)

func TestEachCallGetsAnIsolatedSchema(t *testing.T) {
	a, b := DSN(t), DSN(t)
	if a == b {
		t.Fatal("two calls returned the same DSN")
	}
	open := func(dsn string) *sql.DB {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		db.SetMaxOpenConns(1)
		return db
	}
	da, db := open(a), open(b)
	if _, err := da.Exec(`CREATE TABLE x (n int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT * FROM x`); err == nil {
		t.Fatal("schema b can see schema a's table")
	}
	if _, err := db.Exec(`CREATE TABLE x (n int)`); err != nil {
		t.Fatalf("same table name collides across schemas: %v", err)
	}
}
