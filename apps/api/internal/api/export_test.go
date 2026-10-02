package api

import (
	"context"
	"database/sql"
)

// HoldSchedulingLockForTest runs fn while holding the scheduling advisory lock
// the tick and Replan share, so an external test can make a request meet it
// held. It reports whether the lock was taken.
func HoldSchedulingLockForTest(ctx context.Context, db *sql.DB, fn func()) bool {
	return withDBLock(ctx, db, autoScheduleLockKey, fn)
}
