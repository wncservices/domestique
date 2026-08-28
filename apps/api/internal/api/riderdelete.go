package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// purgeSummary reports what purgeRiderData actually removed — logged by
// every caller (handleDeleteMe, handleDeletePerson) so a rider's departure
// leaves a trail of what happened to their data, not just that it did.
type purgeSummary struct {
	Routes           int
	AccountsUnlinked int
	SyncStateRows    int
	ProviderLinks    int
	CrewMemberships  int
	RidesOrphaned    int
}

// purgeRiderData removes every trace of rider from this app's own database —
// routes, linked accounts and their sync state, provider sign-ins, and crew
// membership. It never touches Auth0 — callers purge local data first and
// only then call PeopleConnector.DeleteUser, so a failure here never leaves
// an Auth0 identity gone with local data still attached to it.
//
// Composed from each store's own public methods rather than one SQL
// transaction: Source is the source.Library interface, and a
// directory-backed deployment satisfies it with no SQL table at all, so no
// single cross-table transaction could span every step here regardless (see
// s.riderIdentityInUse for the same reasoning applied to a read). Every step
// below tolerates "already gone" so a retry after a partial failure is safe.
func (s *Server) purgeRiderData(ctx context.Context, rider string) (purgeSummary, error) {
	var sum purgeSummary
	rider = strings.ToLower(strings.TrimSpace(rider))
	if rider == "" {
		return sum, errors.New("purge: no rider")
	}

	if s.Source != nil {
		routes, _, err := s.Source.List(ctx)
		if err != nil {
			return sum, fmt.Errorf("listing routes: %w", err)
		}
		for _, rt := range routes {
			if !strings.EqualFold(rt.Owner, rider) {
				continue
			}
			if err := s.Source.Delete(ctx, rt.Slug); err != nil && !errors.Is(err, source.ErrNotFound) {
				return sum, fmt.Errorf("deleting route %s: %w", rt.Slug, err)
			}
			sum.Routes++
		}
	}

	if s.Accounts != nil {
		accountList, err := s.Accounts.List(ctx)
		if err != nil {
			return sum, fmt.Errorf("listing accounts: %w", err)
		}
		for _, a := range accountList {
			if !strings.EqualFold(a.Rider, rider) {
				continue
			}
			// Sync state for this account is purged first — Accounts.Unlink
			// deliberately leaves it (a re-link should resume where it left
			// off, per Unlink's own doc comment), but a rider who is gone
			// for good is never re-linking.
			if s.Store != nil {
				entries, err := s.Store.ForAccount(ctx, a.ID)
				if err != nil {
					return sum, fmt.Errorf("listing sync state for %s: %w", a.ID, err)
				}
				for slug := range entries {
					if err := s.Store.Forget(ctx, a.ID, slug); err != nil {
						return sum, fmt.Errorf("clearing sync state %s/%s: %w", a.ID, slug, err)
					}
					sum.SyncStateRows++
				}
			}
			if err := s.Accounts.Unlink(ctx, a.ID); err != nil && !errors.Is(err, accounts.ErrNotFound) {
				return sum, fmt.Errorf("unlinking %s: %w", a.ID, err)
			}
			sum.AccountsUnlinked++
		}
	}

	if s.Links != nil {
		for _, provider := range []string{string(model.ProviderGarmin), string(model.ProviderWahoo), komootProvider} {
			if err := s.Links.Delete(provider, rider); err != nil {
				return sum, fmt.Errorf("removing %s sign-in: %w", provider, err)
			}
			sum.ProviderLinks++
		}
	}

	if s.Crew != nil {
		n, err := s.Crew.RemoveRiderEverywhere(ctx, rider)
		if err != nil {
			return sum, fmt.Errorf("removing crew membership: %w", err)
		}
		sum.CrewMemberships = n
	}

	if s.Schedule != nil {
		n, err := s.Schedule.ClearCreatedBy(ctx, rider)
		if err != nil {
			return sum, fmt.Errorf("clearing ride authorship: %w", err)
		}
		sum.RidesOrphaned = n
	}

	return sum, nil
}

// handleDeleteMe lets a signed-in rider delete their own account: every
// trace of their data in this app's own database, then their Auth0
// identity itself, then the session they made this request with. Full
// removal, not a deactivation — a rider who wants back in needs a fresh
// invite/signup, starting with a clean slate, the same as an admin's own
// handleDeletePerson.
func (s *Server) handleDeleteMe(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.meAuth0Sub(w, r)
	if !ok {
		return
	}
	rider := auth.FromContext(r.Context()).User

	sum, err := s.purgeRiderData(r.Context(), rider)
	if err != nil {
		s.logger().Warn("purging own data before self-delete failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "could not remove your data — nothing was deleted: " + err.Error(),
		})
		return
	}

	if err := s.People.DeleteUser(r.Context(), sub); err != nil {
		s.logger().Warn("deleting own Auth0 identity failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "your data was removed, but the account itself could not be deleted: " + err.Error(),
		})
		return
	}

	// End the session the same way handleSSOLogout does — the account is
	// gone, staying "logged in" to either this app or the IdP would only
	// confuse whoever looks at this browser next.
	if s.Sessions != nil {
		if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
			if err := s.Sessions.Delete(cookie.Value); err != nil {
				s.logger().Warn("deleting session after self-delete failed", "err", err)
			}
		}
	}
	// #nosec G124 -- Secure, HttpOnly and SameSite are all set; gosec wants
	// Secure to be the literal `true` and cannot see through requestIsHTTPS,
	// conditional on purpose — see handleSSOLogout's identical note.
	http.SetCookie(w, &http.Cookie{
		Name: auth.SessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})

	postLogout := requestOrigin(r) + "/"
	if s.LandingHost != "" {
		postLogout = "https://" + s.LandingHost + "/"
	}
	redirectTo := postLogout
	if s.OIDC != nil {
		redirectTo = s.OIDC.EndSessionURL(postLogout, "")
	}

	s.logger().Info("rider deleted own account", "rider", rider, "routes", sum.Routes,
		"accountsUnlinked", sum.AccountsUnlinked, "providerLinks", sum.ProviderLinks, "crewMemberships", sum.CrewMemberships)
	writeJSON(w, http.StatusOK, map[string]string{"redirectTo": redirectTo})
}
