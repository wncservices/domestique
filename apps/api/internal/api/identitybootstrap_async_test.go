package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
)

type blockingPeople struct {
	*fakePeople
	release chan struct{}
}

func (b *blockingPeople) ListPeople(ctx context.Context, gate string, roles ...string) ([]auth0mgmt.Person, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.fakePeople.ListPeople(ctx, gate, roles...)
}

// Starting the bootstrap must not hold the server's start: a slow Management
// API would otherwise delay listening past a liveness probe and have the pod
// killed. The gate stays closed (unbound sign-ins get 503) until it finishes.
func TestStartIdentityBootstrapDoesNotBlockStartup(t *testing.T) {
	e := newBootstrapEnv(t, auth.ModeOIDC, person("auth0|1", "Pat", "pat@example.com", true))
	slow := &blockingPeople{fakePeople: e.people, release: make(chan struct{})}
	e.srv.People = slow

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	returned := make(chan struct{})
	go func() {
		e.srv.StartIdentityBootstrap(ctx, func(int) time.Duration { return time.Millisecond })
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("StartIdentityBootstrap blocked while the Management API was slow")
	}
	if got := e.bound(t); len(got) != 0 {
		t.Fatalf("bound before the API answered: %v", got)
	}

	close(slow.release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(e.bound(t)) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := e.bound(t); got["auth0|1"] != "pat" {
		t.Errorf("bindings = %v once the API answered, want pat bound", got)
	}
}
