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
	"github.com/wncservices/domestique/apps/api/internal/identities"
)

// BootstrapResult says what one run of the bootstrap did.
type BootstrapResult struct {
	// Bound is the accounts that were given a binding.
	Bound int
	// Conflicts is the riders left unbound because their claimants disagree, or
	// because a sole claimant joined the tenant after the rider's data existed.
	Conflicts int
	// Unverified is the riders left unbound because one of several claimants has
	// no verified email.
	Unverified int
	// Unknown is the identities the Users Search had no profile for; nothing is
	// decided about them.
	Unknown int
	// NeedsAttention is the riders with data among the unverified and unknown
	// ones: data that somebody cannot currently sign in as.
	NeedsAttention int
	// Orphans is the riders that have data but no account in the gate role.
	Orphans int
}

// bootstrapBind is one binding the plan wants made.
type bootstrapBind struct {
	Sub, Rider, Email string
	// Verified is whether Email is vouched for. An unverified address is never
	// kept: the binding is made without it.
	Verified bool
}

// bootstrapPlan is what to do about the people the Management API lists. The
// maps count identities per rider; no email or sub is ever in them.
type bootstrapPlan struct {
	Binds      []bootstrapBind
	Conflicts  map[string]int
	Unverified map[string]int
	// Unknown counts, per rider, claimants the Users Search had no profile for.
	Unknown map[string]int
	// NeedsAttention is the riders with data among Unverified and Unknown.
	NeedsAttention map[string]int
	// Orphans is riders with data that no listed person claims and no binding
	// covers, sorted.
	Orphans []string
}

// planBootstrap decides, for every rider the people would sign in as under
// the old name-based rule (likelyRider), whether their accounts can be bound
// safely. It is pure: the caller says which riders already have a binding and
// which have data (and since when), and does the writing.
//
// The rules, in the order they apply to a rider with no binding yet:
//
//   - A claimant the Users Search had no profile for is unknown, and nothing is
//     bound for that rider: missing data is not evidence of anything.
//   - Exactly one claimant is bound whether or not the issuer has verified its
//     address. Nobody else claims the name, so there is nobody to confuse them
//     with, and refusing here is what locks an owner out on a password account
//     whose address was never confirmed. The address is kept (as a MAC) only if
//     it is verified. The exception: a rider with data whose only claimant
//     joined the tenant after that data existed is likely someone who renamed
//     themselves, so it is flagged as a conflict and left for an admin.
//   - Several claimants are bound only when every one has a verified email and
//     they are all the same address (one person, two ways to sign in). A
//     disagreement is a conflict; an unverified claimant leaves the rider
//     unbound.
//
// data maps a rider to the earliest time found in its data (zero when it has
// data but no usable time); see identities.Store.RidersWithData for how rough
// that is. A rider that already has a binding is skipped, so an admin's
// resolution is not reported again at every start.
func planBootstrap(people []auth0mgmt.Person, alreadyBound func(rider string) bool, data map[string]time.Time) bootstrapPlan {
	plan := bootstrapPlan{
		Conflicts: map[string]int{}, Unverified: map[string]int{},
		Unknown: map[string]int{}, NeedsAttention: map[string]int{},
	}

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
		since, hasData := data[rider]

		unknown := 0
		for _, p := range group {
			if !p.ProfileKnown {
				unknown++
			}
		}
		if unknown > 0 {
			plan.Unknown[rider] = unknown
			if hasData {
				plan.NeedsAttention[rider] = len(group)
			}
			continue
		}

		if len(group) == 1 {
			p := group[0]
			if hasData && !since.IsZero() && !p.CreatedAt.IsZero() && p.CreatedAt.After(since) {
				plan.Conflicts[rider] = 1
				continue
			}
			email := strings.ToLower(strings.TrimSpace(p.Email))
			plan.Binds = append(plan.Binds, bootstrapBind{
				Sub: p.UserID, Rider: rider, Email: email, Verified: p.EmailVerified && email != "",
			})
			continue
		}

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
			if hasData {
				plan.NeedsAttention[rider] = len(group)
			}
		case len(emails) > 1:
			plan.Conflicts[rider] = len(group)
		default:
			for _, p := range group {
				plan.Binds = append(plan.Binds, bootstrapBind{
					Sub: p.UserID, Rider: rider, Email: strings.ToLower(strings.TrimSpace(p.Email)), Verified: true,
				})
			}
		}
	}

	for rider := range data {
		if _, claimed := claimants[rider]; !claimed && !alreadyBound(rider) {
			plan.Orphans = append(plan.Orphans, rider)
		}
	}
	sort.Strings(plan.Orphans)
	return plan
}

// riderDataColumns is the registry's rider columns without the tables that
// are not a rider's data: the bindings themselves, and the deployment-wide
// settings whose updated_by is attribution only.
func riderDataColumns() identities.TakenColumns {
	cols := riderNameColumns()
	for _, t := range []string{"rider_identities", "settings", "flags"} {
		delete(cols, t)
	}
	return cols
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

	data, err := s.Identities.RidersWithData(ctx, riderDataColumns())
	if err != nil {
		return res, fmt.Errorf("reading which riders have data: %w", err)
	}

	plan := planBootstrap(people, func(rider string) bool { return boundRiders[rider] }, data)
	for _, b := range plan.Binds {
		mac := ""
		if b.Verified {
			mac = s.Identities.EmailMAC(b.Email)
		}
		added, err := s.Identities.Bind(ctx, issuer, b.Sub, b.Rider, mac)
		if err != nil {
			return res, fmt.Errorf("binding a rider: %w", err)
		}
		if added {
			res.Bound++
		}
	}

	for rider, n := range plan.Conflicts {
		res.Conflicts++
		s.logger().Error("identity bootstrap conflict: accounts claiming this rider disagree, or the only one joined after its data existed, so none was bound",
			"rider", rider, "identities", n)
	}
	for rider, n := range plan.Unverified {
		res.Unverified++
		if _, urgent := plan.NeedsAttention[rider]; urgent {
			continue // reported below, as an Error
		}
		s.logger().Warn("identity bootstrap skipped a rider: an account claiming it has no verified email, so none was bound",
			"rider", rider, "identities", n)
	}
	unknownIdentities := 0
	for _, n := range plan.Unknown {
		unknownIdentities += n
	}
	res.Unknown = unknownIdentities
	if unknownIdentities > 0 {
		// Never bound, and never guessed at. Nothing identifying: a count.
		s.logger().Error("identity bootstrap missing profile data: the Management API search returned nothing for some people, so none of their riders was bound",
			"identities", unknownIdentities, "riders", len(plan.Unknown))
	}
	for rider, n := range plan.NeedsAttention {
		res.NeedsAttention++
		s.logger().Error("identity bootstrap needs attention: a rider with data has an unverified or unknown claimant, so none was bound",
			"rider", rider, "identities", n)
	}
	res.Orphans = len(plan.Orphans)
	if res.Orphans > 0 {
		s.logger().Warn("identity bootstrap found riders with data but no account in the gate role", "riders", res.Orphans)
	}
	s.logger().Info("identity bootstrap complete", "bound", res.Bound, "conflicts", res.Conflicts,
		"unverified", res.Unverified, "unknown", res.Unknown, "orphans", res.Orphans)

	s.MarkBindingsReady()
	return res, nil
}

// bootstrapAttemptTimeout bounds one attempt, so a slow Management API cannot
// hold the server's start.
const bootstrapAttemptTimeout = 15 * time.Second

// StartIdentityBootstrap runs the bootstrap in the background, retrying until
// it succeeds, and returns at once. wait gives the pause before retry n
// (1-based). Does nothing outside mode oidc, where there are no bindings.
//
// It does not run the first attempt before returning, although that would
// usually have sign-in open by the time the server listens. A slow Management
// API (the attempt is allowed 15 s, and the token exchange, the role listing
// and the search are each a request) would then delay listening past whatever
// liveness window the deployment has, and a restart loop would follow: the
// probe settings of a chart this repo does not own would decide whether an Auth0
// slowdown takes the app down. Running it alongside costs nothing the gate does
// not already cost: until it finishes an account with no binding is refused
// (503, domestique_identity_bootstrap_ready is 0), bound accounts sign in as
// usual, and routes and health checks are up from the first second.
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
	go func() {
		for n := 0; ; n++ {
			if n > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait(n)):
				}
			}
			if err := attempt(); err != nil {
				s.logger().Error("identity bootstrap failed; unbound accounts are refused until it succeeds", "attempt", n+1, "err", err)
				continue
			}
			if n > 0 {
				s.logger().Info("identity bootstrap succeeded on a retry", "attempt", n+1)
			}
			return
		}
	}()
}
