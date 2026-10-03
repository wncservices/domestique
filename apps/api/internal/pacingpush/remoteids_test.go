package pacingpush

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteIDsEachEngine(t *testing.T) {
	for engine, open := range map[string]func(*testing.T) *Store{
		"sqlite": func(t *testing.T) *Store { return openStore(t, filepath.Join(t.TempDir(), "p.db")) },
		"postgres": func(t *testing.T) *Store {
			dsn := os.Getenv(postgresEnv)
			if dsn == "" {
				t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
			}
			return openStore(t, dsn)
		},
	} {
		t.Run(engine, func(t *testing.T) {
			s := open(t)
			ctx := t.Context()
			_ = s.Put(ctx, "wilant", "garmin", "pacing:a", "c1")
			_ = s.Put(ctx, "wilant", "garmin", "pacing:b", "c2")
			_ = s.Put(ctx, "wilant", "wahoo", "pacing:a", "w1")
			_ = s.Put(ctx, "other", "garmin", "pacing:a", "o1")

			got, err := s.RemoteIDs(ctx, "Wilant", "garmin")
			if err != nil || len(got) != 2 || !got["c1"] || !got["c2"] {
				t.Errorf("garmin ids = %v (err %v), want c1 and c2 only", got, err)
			}
			if none, err := s.RemoteIDs(ctx, "nobody", "garmin"); err != nil || len(none) != 0 {
				t.Errorf("a rider with none: %v, %v", none, err)
			}
		})
	}
}
