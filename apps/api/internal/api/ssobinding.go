package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/identities"
)

// MarkBindingsReady opens sign-in to accounts that have no binding yet. The
// startup step that binds the riders that already exist calls it when it is
// done; a deployment with nothing to bind them from (no Management API) calls
// it straight away.
func (s *Server) MarkBindingsReady() { s.bindingsReady.Store(true) }

// riderNameColumns is every column, in every registered table, that holds a
// rider's name. A name found in any of them is taken: a rider who has only
// training data, or only a crew membership, is still somebody, and a sign-in
// that merely carries the same name must not land on them.
//
// Built from riderTables rather than listed again here, so a table registered
// for purge and rename is covered without anyone remembering this function.
func riderNameColumns() identities.TakenColumns {
	out := identities.TakenColumns{}
	for table, rt := range riderTables {
		cols := rt.Rename.Columns
		if len(cols) == 0 {
			cols = rt.Taken
		}
		if len(cols) > 0 {
			out[table] = cols
		}
	}
	return out
}

// riderForSignIn decides which rider a verified ID token signs in as, and
// reports false after writing the response when it cannot.
//
// The decision never starts from a name. The issuer's sub is assigned by the
// issuer and cannot be edited by its holder; a name claim can (a Google
// display name is whatever its owner types), and deriving the rider from it
// let anyone invited rename themselves into another rider. So:
//
//  1. a bound account is the rider it is bound to, whatever its claims say now;
//  2. an unbound account whose verified email matches the accounts of exactly
//     one rider joins that rider (the same person, a second way of signing in);
//  3. otherwise it gets its own rider, named from the claims if that name is
//     free and from the first free "name-2", "name-3"... if it is not;
//  4. an email that matches more than one rider joins none of them, and falls
//     to step 3 for an admin to sort out.
//
// identity.User, as built from the claims, is only the suggestion for step 3.
func (s *Server) riderForSignIn(ctx context.Context, w http.ResponseWriter, identity auth.Identity) (string, bool) {
	if s.Identities == nil {
		// Nothing to bind to means nothing safe to resolve by: falling back to
		// the name claim is the hole this exists to close.
		s.logger().Error("oidc sign-in refused: this deployment has no identity store")
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "this deployment cannot complete a sign-in",
		})
		return "", false
	}
	sub := strings.TrimSpace(identity.Sub)
	if sub == "" {
		s.logger().Warn("oidc token carried no sub to bind a rider to")
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "the identity provider did not identify who signed in",
		})
		return "", false
	}
	issuer := s.authenticator().OIDC().Issuer

	// Only a verified address may stand for the person, and only as a keyed
	// MAC: the address itself is never stored, matched in clear or logged.
	mac := ""
	if identity.EmailVerified {
		mac = s.Identities.EmailMAC(identity.Email)
	}

	fail := func(step string, err error) (string, bool) {
		s.logger().Error("resolving a rider for a sign-in failed", "step", step, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "sign-in could not be completed"})
		return "", false
	}

	// 1. Bound.
	b, bound, err := s.Identities.Get(ctx, issuer, sub)
	if err != nil {
		return fail("lookup", err)
	}
	if bound {
		// A failed write here must not lock a bound rider out: the binding
		// exists and is what matters.
		if err := s.Identities.Seen(ctx, issuer, sub, mac); err != nil {
			s.logger().Warn("recording a sign-in failed", "rider", b.Rider, "err", err)
		}
		return b.Rider, true
	}

	// Past here an account has no binding, and what happens next can hand it
	// a rider that already has data. Until the riders that existed before
	// bindings have theirs, that rider may simply not be bound yet, and the
	// only thing left to go on would be the name.
	if !s.bindingsReady.Load() {
		s.logger().Warn("oidc sign-in refused until the riders that already exist have been bound")
		w.Header().Set("Retry-After", "30")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "sign-in is still starting up — try again in a minute",
		})
		return "", false
	}

	// 2 and 4. A verified email shared with accounts already bound.
	if mac != "" {
		riders, err := s.Identities.RidersByEmail(ctx, mac)
		if err != nil {
			return fail("email match", err)
		}
		switch {
		case len(riders) == 1:
			if _, err := s.Identities.Bind(ctx, issuer, sub, riders[0], mac); err != nil {
				return fail("bind by email", err)
			}
			// Bind keeps an existing binding, so what the store now says is the
			// answer even if another request for this account won the race.
			b, _, err := s.Identities.Get(ctx, issuer, sub)
			if err != nil {
				return fail("lookup", err)
			}
			s.logger().Info("identity bound by verified email", "rider", b.Rider, "issuer", issuer)
			return b.Rider, true
		case len(riders) > 1:
			s.logger().Warn("verified email is ambiguous: it belongs to more than one rider, so it joins none",
				"issuer", issuer, "riders", len(riders))
		}
	}

	// 3. A rider of its own.
	claim, err := s.Identities.ClaimFree(ctx, issuer, sub, mac, identity.User, riderNameColumns())
	if err != nil {
		return fail("claim", err)
	}
	switch {
	case claim.Suffixed:
		s.logger().Warn("sign-in name already belongs to another rider",
			"wanted", strings.ToLower(identity.User), "rider", claim.Rider, "issuer", issuer)
	case !claim.Existing:
		s.logger().Info("identity bound to a new rider", "rider", claim.Rider, "issuer", issuer)
	}
	return claim.Rider, true
}
