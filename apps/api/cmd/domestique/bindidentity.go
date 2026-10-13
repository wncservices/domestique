package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/identities"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// bindingStore opens the bindings for a command. A dry run must not change
// anything, and opening the store the normal way creates the table and ends
// every session (identities.UseDB), so a dry run only attaches to a table
// that is already there: store is nil when there is none, which reads as "no
// bindings".
func bindingStore(src *source.DB, dryRun bool, box *secrets.Box) (*identities.Store, error) {
	if !dryRun {
		return identities.UseDB(src.Conn(), src.DSN(), box)
	}
	exists, err := identities.Exists(src.Conn(), src.DSN())
	if err != nil || !exists {
		return nil, err
	}
	return identities.Attach(src.Conn(), src.DSN(), box)
}

// sessionKey is the deployment's encryption key if there is one. A session
// cannot be found without it (its owner is stored as a keyed hash), so with
// none the sessions of an account whose binding changed cannot be ended here;
// the command still changes the binding and says so.
func sessionKey() (*secrets.Box, error) {
	box, err := secrets.FromEnv()
	switch {
	case errors.Is(err, secrets.ErrNoKey):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return box, nil
}

// endSessions ends every login of one issuer account, because each holds the
// rider it had when it was made.
func endSessions(ctx context.Context, src *source.DB, box *secrets.Box, sub string) error {
	if box == nil {
		fmt.Printf("sessions:   not ended — %s is not set, so they cannot be found; they expire on their own\n", secrets.EnvKey)
		return nil
	}
	ss, err := sessions.UseDB(src.Conn(), src.DSN(), box)
	if err != nil {
		return err
	}
	if err := ss.DeleteSub(ctx, sub); err != nil {
		return fmt.Errorf("ending the sessions of %s: %w", sub, err)
	}
	fmt.Println("sessions:   ended; the account signs in again as its new rider")
	return nil
}

// runBindIdentity points one issuer account at a rider: the same change the
// People page's "move" makes, for a deployment with no Management API and for
// an admin who needs their own account moved (which the page refuses).
//
// It never reads or writes an email. A binding made here has none recorded,
// and gets one the next time the account signs in.
func runBindIdentity(src *source.DB, args []string, dryRun bool) error {
	if len(args) != 3 {
		return errors.New("bind-identity needs exactly three arguments: <issuer> <sub> <rider>\n" +
			"       the issuer is auth.oidc.issuer exactly as configured; the sub is the account's id (Auth0: auth0|… or google-oauth2|…)")
	}
	issuer, sub, rider := strings.TrimSpace(args[0]), strings.TrimSpace(args[1]), normalizeRider(args[2])
	if issuer == "" || sub == "" {
		return errors.New("bind-identity: neither the issuer nor the sub may be empty")
	}
	if rider == "" || !accounts.RiderPattern.MatchString(rider) {
		return fmt.Errorf("bind-identity: %q is not a rider name (letters, digits and . _ @ | - only)", args[2])
	}

	box, err := sessionKey()
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, err := bindingStore(src, dryRun, box)
	if err != nil {
		return err
	}

	from, bound := "", false
	if store != nil {
		b, ok, err := store.Get(ctx, issuer, sub)
		if err != nil {
			return err
		}
		from, bound = b.Rider, ok
	}

	switch {
	case bound && from == rider:
		fmt.Printf("%s is already bound to %q — nothing to do\n", sub, rider)
		return nil
	case bound && dryRun:
		fmt.Printf("would move %s from %q to %q\n", sub, from, rider)
	case bound:
		if err := store.Move(ctx, issuer, sub, rider); err != nil {
			return err
		}
		fmt.Printf("moved %s from %q to %q\n", sub, from, rider)
	case dryRun:
		fmt.Printf("would bind %s to %q\n", sub, rider)
	default:
		if _, err := store.Bind(ctx, issuer, sub, rider, ""); err != nil {
			return err
		}
		fmt.Printf("bound %s to %q\n", sub, rider)
	}

	if dryRun {
		fmt.Println("\ndry run — nothing written")
		return nil
	}
	return endSessions(ctx, src, box, sub)
}

// runUnbindIdentity removes the binding of one issuer account. The account
// then signs in as a new rider of its own the next time (see
// handleSSOCallback), so this is for undoing a wrong binding, not for locking
// someone out: block them in Auth0 for that.
func runUnbindIdentity(src *source.DB, args []string, dryRun bool) error {
	if len(args) != 2 {
		return errors.New("unbind-identity needs exactly two arguments: <issuer> <sub>")
	}
	issuer, sub := strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
	if issuer == "" || sub == "" {
		return errors.New("unbind-identity: neither the issuer nor the sub may be empty")
	}

	box, err := sessionKey()
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, err := bindingStore(src, dryRun, box)
	if err != nil {
		return err
	}

	var b identities.Binding
	var bound bool
	if store != nil {
		if b, bound, err = store.Get(ctx, issuer, sub); err != nil {
			return err
		}
	}
	if !bound {
		fmt.Printf("%s has no binding — nothing to do\n", sub)
		return nil
	}
	if dryRun {
		fmt.Printf("would unbind %s from %q\n", sub, b.Rider)
		fmt.Println("\ndry run — nothing written")
		return nil
	}
	if _, err := store.Unbind(ctx, issuer, sub); err != nil {
		return err
	}
	fmt.Printf("unbound %s from %q\n", sub, b.Rider)
	return endSessions(ctx, src, box, sub)
}
