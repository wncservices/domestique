package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
)

// BootstrapResult says what one run of the bootstrap did.
type BootstrapResult struct {
	// Bound is the accounts that were given a binding.
	Bound int
	// Conflicts is the riders whose claimants disagree, so nobody was bound.
	Conflicts int
	// Unverified is the riders left unbound because a claimant has no verified
	// email.
	Unverified int
}

// bootstrapBind is one binding the plan wants made.
type bootstrapBind struct {
	Sub, Rider, Email string
}

// bootstrapPlan is what to do about the people the Management API lists.
type bootstrapPlan struct {
	Binds []bootstrapBind
	// Conflicts and Unverified count the identities claiming each rider that
	// was left alone: no email is kept, only how many.
	Conflicts  map[string]int
	Unverified map[string]int
}

// planBootstrap decides, for every rider the people would sign in as under
// the old name-based rule (likelyRider), whether their accounts can be bound
// safely. It is pure: the caller says which riders already have a binding and
// does the writing.
//
// A rider is bound only when every account claiming it has a verified email
// and they are all the same address, which is one person reached more than one
// way (Google and a password). Any disagreement binds nobody: two different
// people claim the name, and picking one is exactly the mistake this whole
// change exists to stop. A rider that already has a binding is skipped, so an
// admin's resolution is respected and not reported again at every start.
func planBootstrap(people []auth0mgmt.Person, alreadyBound func(rider string) bool) bootstrapPlan {
	plan := bootstrapPlan{Conflicts: map[string]int{}, Unverified: map[string]int{}}

	claimants := map[string][]auth0mgmt.Person{}
	for _, p := range people {
		if rider := likelyRider(p.Name, p.Nickname, p.UserID); rider != "" {
			claimants[rider] = append(claimants[rider], p)
		}
	}
	riders := make([]string, 0, len(claimants))
	for rider := range claimants {
		riders = append(riders, rider)
	}
	sort.Strings(riders)

	for _, rider := range riders {
		if alreadyBound(rider) {
			continue
		}
		group := claimants[rider]

		emails := map[string]bool{}
		unverified := 0
		for _, p := range group {
			email := strings.ToLower(strings.TrimSpace(p.Email))
			if !p.EmailVerified || email == "" {
				unverified++
				continue
			}
			emails[email] = true
		}
		switch {
		case unverified > 0:
			plan.Unverified[rider] = unverified
		case len(emails) > 1:
			plan.Conflicts[rider] = len(group)
		default:
			for _, p := range group {
				plan.Binds = append(plan.Binds, bootstrapBind{
					Sub: p.UserID, Rider: rider, Email: strings.ToLower(strings.TrimSpace(p.Email)),
				})
			}
		}
	}
	return plan
}

// BootstrapIdentities binds the riders that existed before bindings, from the
// people the Management API lists, and then opens sign-in to unbound accounts.
//
// Why this is needed at all: once a sign-in is resolved through a binding,
// an existing rider with none would look like a new person whose name happens
// to match, and (correctly) get a suffixed rider instead of their own data.
// So the existing riders are bound from what the issuer knows about them, with
// the same safety rule the live sign-in uses: only a verified email, only when
// everybody claiming the rider agrees.
//
// Idempotent, and only ever adds rows. A conflict is not a failure: the
// affected riders get no binding (logged at Error, with the rider and a count
// and nothing else), the rest are bound, and sign-in opens. A failure to
// read the people is returned and leaves sign-in closed.
func (s *Server) BootstrapIdentities(ctx context.Context) (BootstrapResult, error) {
	var res BootstrapResult
	if s.Identities == nil || s.People == nil {
		return res, errors.New("identity bootstrap needs the identity store and the Management API")
	}
	gate := s.authenticator().RequiredGroup()
	issuer := s.authenticator().OIDC().Issuer
	if gate == "" {
		// Without a gate role there is no list of who has access to read, so
		// there is nothing to bootstrap from. The riders then have to be bound
		// by hand, which is the same situation as no Management API at all.
		s.logger().Warn("identity bootstrap has no gate role to list people from; riders that existed before binding are not bound automatically")
		s.MarkBindingsReady()
		return res, nil
	}

	adminRole, riderRole := s.permissionRoleNames()
	var permissionRoles []string
	if adminRole != "" {
		permissionRoles = append(permissionRoles, adminRole)
	}
	if riderRole != "" {
		permissionRoles = append(permissionRoles, riderRole)
	}
	people, err := s.People.ListPeople(ctx, gate, permissionRoles...)
	if err != nil {
		return res, fmt.Errorf("listing people for the identity bootstrap: %w", err)
	}

	existing, err := s.Identities.List(ctx)
	if err != nil {
		return res, fmt.Errorf("reading the existing bindings: %w", err)
	}
	boundRiders := map[string]bool{}
	for _, b := range existing {
		boundRiders[b.Rider] = true
	}

	plan := planBootstrap(people, func(rider string) bool { return boundRiders[rider] })
	for _, b := range plan.Binds {
		added, err := s.Identities.Bind(ctx, issuer, b.Sub, b.Rider, s.Identities.EmailMAC(b.Email))
		if err != nil {
			return res, fmt.Errorf("binding a rider: %w", err)
		}
		if added {
			res.Bound++
		}
	}

	for rider, n := range plan.Conflicts {
		res.Conflicts++
		s.logger().Error("identity bootstrap conflict: accounts with different verified emails claim this rider, so none was bound",
			"rider", rider, "identities", n)
	}
	for rider, n := range plan.Unverified {
		res.Unverified++
		s.logger().Warn("identity bootstrap skipped a rider: an account claiming it has no verified email, so none was bound",
			"rider", rider, "identities", n)
	}
	s.logger().Info("identity bootstrap complete", "bound", res.Bound, "conflicts", res.Conflicts, "unverified", res.Unverified)

	s.MarkBindingsReady()
	return res, nil
}

// bootstrapAttemptTimeout bounds one attempt, so a slow Management API cannot
// hold the server's start.
const bootstrapAttemptTimeout = 15 * time.Second

// StartIdentityBootstrap runs the bootstrap once before returning, so that in
// the usual case sign-in is open by the time the server starts listening, and
// retries in the background if that first attempt fails. wait gives the pause
// before retry n (1-based).
//
// The server does not wait for it to succeed. A Management API outage must
// not take routes, health checks or already-bound riders down with it; what it
// costs is that an account with no binding is refused (503) until a retry
// works, which is the safe direction to fail in. Does nothing outside mode
// oidc, where there are no bindings.
func (s *Server) StartIdentityBootstrap(ctx context.Context, wait func(attempt int) time.Duration) {
	if s.authenticator().Mode() != auth.ModeOIDC {
		return
	}
	attempt := func() error {
		actx, cancel := context.WithTimeout(ctx, bootstrapAttemptTimeout)
		defer cancel()
		_, err := s.BootstrapIdentities(actx)
		return err
	}
	err := attempt()
	if err == nil {
		return
	}
	s.logger().Error("identity bootstrap failed; unbound accounts are refused until it succeeds", "err", err)

	go func() {
		for n := 1; ; n++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait(n)):
			}
			if err := attempt(); err != nil {
				s.logger().Error("identity bootstrap failed; unbound accounts are refused until it succeeds", "attempt", n+1, "err", err)
				continue
			}
			s.logger().Info("identity bootstrap succeeded on a retry", "attempt", n+1)
			return
		}
	}()
}
