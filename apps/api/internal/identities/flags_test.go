package identities

import (
	"context"
	"testing"
)

func TestFlagsAreReplacedPerIssuerAndFollowTheRider(t *testing.T) {
	ctx := context.Background()
	eachEngine(t, func(t *testing.T, e env) {
		if _, err := e.db.Conn().Exec(`DELETE FROM bootstrap_flags`); err != nil {
			t.Fatal(err)
		}
		first := []Flag{
			{Sub: "auth0|1", Rider: "wilant", Reason: FlagNewerThanData},
			{Sub: "auth0|2", Rider: "sam", Reason: FlagConflict},
		}
		if err := e.store.ReplaceFlags(ctx, issuer, first); err != nil {
			t.Fatal(err)
		}
		// Another issuer's flags are not this one's to replace.
		if err := e.store.ReplaceFlags(ctx, "https://other.test/", []Flag{{Sub: "x", Rider: "x", Reason: FlagConflict}}); err != nil {
			t.Fatal(err)
		}
		got, err := e.store.Flags(ctx, issuer)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got["auth0|1"].Rider != "wilant" || got["auth0|1"].Reason != FlagNewerThanData {
			t.Fatalf("flags = %+v", got)
		}

		// A later run replaces the set: what no longer holds is gone.
		if err := e.store.ReplaceFlags(ctx, issuer, first[:1]); err != nil {
			t.Fatal(err)
		}
		if got, _ = e.store.Flags(ctx, issuer); len(got) != 1 {
			t.Errorf("flags after replace = %+v, want one", got)
		}
		if other, _ := e.store.Flags(ctx, "https://other.test/"); len(other) != 1 {
			t.Errorf("another issuer's flags = %+v, want them untouched", other)
		}

		// Removing a rider removes the flags that name it.
		if _, err := e.store.DeleteRider(ctx, "wilant"); err != nil {
			t.Fatal(err)
		}
		if got, _ = e.store.Flags(ctx, issuer); len(got) != 0 {
			t.Errorf("flags after the rider was removed = %+v", got)
		}
	})
}
