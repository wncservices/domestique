package basemap

import (
	"github.com/wncservices/domestique/apps/api/internal/pgtest"
	"path/filepath"
	"testing"
)

// eachEngine runs fn on SQLite and, when DOMESTIQUE_TEST_POSTGRES is set, on
// PostgreSQL. The store has per-engine DDL (schema takes the dialect), a partial
// expression index that only one in-progress update may satisfy, and error-text
// handling for pgx; passing on one engine says nothing about the other.
func eachEngine(t *testing.T, fn func(t *testing.T, dsn string)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, filepath.Join(t.TempDir(), "test.db")) })
	t.Run("postgres", func(t *testing.T) {
		dsn := pgtest.DSN(t)
		fn(t, dsn)
	})
}
