package crew

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// Two owners leaving at the same moment must not both succeed. On PostgreSQL
// (READ COMMITTED) a single DELETE with a count subquery lets both see the
// other's row; the check has to lock the owner rows.
func TestConcurrentOwnersLeavingNeverStrandTheCrew(t *testing.T) {
	store := postgresStore(t)
	ctx := t.Context()

	for round := 0; round < 40; round++ {
		c, err := store.Create(ctx, fmt.Sprintf("Race %d", round), "alice")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.RequestJoin(ctx, c.ID, "bob"); err != nil {
			t.Fatal(err)
		}
		if err := store.Approve(ctx, c.ID, "bob", "alice"); err != nil {
			t.Fatal(err)
		}
		if err := store.SetOwner(ctx, c.ID, "bob", true); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, who := range []string{"alice", "bob"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = store.Remove(ctx, c.ID, who)
			}()
		}
		close(start)
		wg.Wait()

		left := 0
		for _, who := range []string{"alice", "bob"} {
			if ok, _ := store.IsOwner(ctx, c.ID, who); ok {
				left++
			}
		}
		if left == 0 {
			t.Fatalf("round %d: both owners left (errors %v, %v), the crew has no owner", round, errs[0], errs[1])
		}
		if !errors.Is(errs[0], ErrLastOwner) && !errors.Is(errs[1], ErrLastOwner) {
			t.Fatalf("round %d: neither removal was refused: %v, %v", round, errs[0], errs[1])
		}
	}
}
