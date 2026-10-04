package sessions

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
)

// Whether the issuer vouched for the address travels with the session, or the
// morning summary could not tell a verified address from a typed one.
func TestEmailVerifiedSurvivesTheSession(t *testing.T) {
	s := newStore(t, newBox(t))
	for _, verified := range []bool{true, false} {
		tok, _, err := s.Create(auth.Identity{User: "wilant", Email: "w@example.com", EmailVerified: verified, Sub: "auth0|w"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		id, ok := s.Lookup(tok)
		if !ok || id.Email != "w@example.com" || id.EmailVerified != verified {
			t.Fatalf("verified=%v: got %+v %v", verified, id, ok)
		}
		// Renaming rewrites the sealed identity; it must not drop the flag.
		if err := s.UpdateName(tok, "New Name"); err != nil {
			t.Fatal(err)
		}
		id, _ = s.Lookup(tok)
		if id.Name != "New Name" || id.EmailVerified != verified {
			t.Fatalf("after UpdateName, verified=%v: got %+v", verified, id)
		}
	}
}

// A session made before the claim was recorded has none, and reads as
// unverified: failing closed means that rider signs in again to opt in.
func TestASessionWithoutTheFlagIsUnverified(t *testing.T) {
	s := newStore(t, newBox(t))
	tok, _, err := s.Create(auth.Identity{User: "wilant", Email: "w@example.com"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := s.Lookup(tok); id.EmailVerified {
		t.Error("an address nobody vouched for read as verified")
	}
}

func TestRidersOfSub(t *testing.T) {
	s := newStore(t, newBox(t))
	for _, id := range []auth.Identity{
		{User: "wilant", Sub: "auth0|w"},
		{User: "wilant", Sub: "auth0|w"}, // a second login, same rider
		{User: "other", Sub: "auth0|o"},
	} {
		if _, _, err := s.Create(id, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RidersOfSub(t.Context(), "auth0|w")
	if err != nil || len(got) != 1 || got[0] != "wilant" {
		t.Fatalf("RidersOfSub = %v %v, want [wilant] once", got, err)
	}
	for _, none := range []string{"", "auth0|nobody"} {
		if got, err := s.RidersOfSub(t.Context(), none); err != nil || len(got) != 0 {
			t.Errorf("RidersOfSub(%q) = %v %v, want none", none, got, err)
		}
	}
}
