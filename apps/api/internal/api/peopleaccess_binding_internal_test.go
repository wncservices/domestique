package api

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
	"github.com/wncservices/domestique/apps/api/internal/identities"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// listOnly is a PeopleConnector that can only list; anything else panics,
// which is the point: ridersOfPerson must not call it.
type listOnly struct {
	PeopleConnector
	people []auth0mgmt.Person
}

func (l listOnly) ListPeople(context.Context, string, ...string) ([]auth0mgmt.Person, error) {
	return l.people, nil
}

// Blocking or demoting a person revokes the calendar link and morning email of
// the riders they are. An account bound to a suffixed rider whose profile still
// reads like somebody else's name must revoke its own rider, not that person's.
func TestRidersOfPersonUsesTheBindingNotTheProfileName(t *testing.T) {
	db, err := source.OpenDB(filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, _ := secrets.GenerateKey()
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := identities.UseDB(db.Conn(), db.DSN(), box)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.New(auth.Config{
		Mode: auth.ModeOIDC, RequiredGroup: "domestique-users",
		Roles: auth.RoleMapping{Admin: []string{"admins"}, Rider: []string{"cyclists"}},
		OIDC:  auth.OIDCConfig{Issuer: "https://tenant.example/", ClientID: "c", RedirectURL: "https://app.example/cb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := ids.Bind(ctx, "https://tenant.example/", "google|impostor", "wilant-2", ""); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		Auth: authenticator, Identities: ids,
		People: listOnly{people: []auth0mgmt.Person{
			{UserID: "google|impostor", Name: "Wilant", Email: "impostor@example.com"},
			{UserID: "auth0|unbound", Name: "Pat", Email: "pat@example.com"},
		}},
	}

	if got := s.ridersOfPerson(ctx, "google|impostor", ""); !slices.Equal(got, []string{"wilant-2"}) {
		t.Errorf("riders of the bound impostor = %v, want only [wilant-2]; the profile name wilant belongs to somebody else", got)
	}
	// With no binding the profile's guess is still what there is to go on.
	if got := s.ridersOfPerson(ctx, "auth0|unbound", ""); !slices.Equal(got, []string{"pat"}) {
		t.Errorf("riders of an unbound person = %v, want the profile's [pat]", got)
	}
}
