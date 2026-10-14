package api

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ride_imports has a partial unique index (one running import per rider) that
// a merge could collide on: both riders mid-import must not abort the merge,
// and the target keeps its own row.
func TestMergeRiderWithARunningImportOnEachSide(t *testing.T) {
	eachRiderDataEngine(t, func(t *testing.T, env *riderDataEnv) {
		tok := fmt.Sprintf("%d", time.Now().UnixNano())
		a, b := "a"+tok, "b"+tok
		ts := "2026-10-04T09:00:00Z"
		for _, row := range []struct{ id, rider string }{{"ia" + tok, a}, {"ib" + tok, b}} {
			if _, err := env.db.Exec(env.d.Rebind(`INSERT INTO ride_imports (id, rider, state, started_at, updated_at) VALUES (?, ?, 'running', ?, ?)`),
				row.id, row.rider, ts, ts); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := MergeRider(context.Background(), env.db, env.d, a, b, MergeOptions{}); err != nil {
			t.Fatalf("merge aborted on the running-import index: %v", err)
		}
		if n := count(t, env, "ride_imports", "rider", b); n != 1 {
			t.Errorf("%d import rows under the target, want its own single row", n)
		}
		if n := count(t, env, "ride_imports", "rider", a); n != 0 {
			t.Errorf("%d import rows left under the merged-away rider", n)
		}
	})
}
