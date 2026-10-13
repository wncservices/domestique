package identities

import (
	"context"
	"testing"
)

// A sub or an address bound under one issuer says nothing about another: a
// changed tenant must not inherit riders by coincidence of sub or email.
func TestLookupsAreScopedToTheIssuer(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		mac := e.store.EmailMAC("a@example.com")
		if _, err := e.store.Bind(ctx, "https://other.test/", "g|1", "stranger", mac); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.store.RidersByEmail(ctx, issuer, mac); len(got) != 0 {
			t.Errorf("RidersByEmail under another issuer's binding = %v, want none", got)
		}
		if got, _ := e.store.RidersOfSub(ctx, issuer, "g|1"); len(got) != 0 {
			t.Errorf("RidersOfSub under another issuer's binding = %v, want none", got)
		}
		if got, _ := e.store.RidersByEmail(ctx, "https://other.test/", mac); len(got) != 1 {
			t.Errorf("RidersByEmail under its own issuer = %v, want one", got)
		}
	})
}
