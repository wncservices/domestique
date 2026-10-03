package api

import (
	"context"
	"sort"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
)

// The calendar feed and the morning email both work with no signed-in rider: a
// calendar app fetches by secret URL, and the morning pass runs at 06:30.
// That is why neither can lean on "the next sign-in fails" the way everything
// else does when an admin blocks someone or takes their role away. These are
// the hooks that make the admin action reach them.

// ridersOfPerson is every rider name an admin action on one person may mean.
// The People page names a person by their issuer id (and, for blocking, an
// email), while what a rider owns here is keyed by the rider name, so it is
// gathered from every place that links the two:
//
//   - the rider names in that identity's live sessions (the exact name they
//     signed in as);
//   - the riders opted in to the morning summary under that email;
//   - the name the People page itself guesses for them (likelyRider), found by
//     looking the person up in the gate-role listing.
//
// Call it before the change: ending a session, or removing a role, takes the
// person out of the places it reads. A lookup that fails is logged and
// skipped, never fatal: what is found is revoked, and the morning pass checks
// the blocklist itself as a backstop.
func (s *Server) ridersOfPerson(ctx context.Context, id, email string) []string {
	set := map[string]bool{}
	add := func(name string) {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			set[name] = true
		}
	}
	email = strings.ToLower(strings.TrimSpace(email))

	if s.Sessions != nil {
		names, err := s.Sessions.RidersOfSub(ctx, id)
		if err != nil {
			s.logger().Warn("people access: could not read the riders of a person's sessions", "id", id, "err", err)
		}
		for _, n := range names {
			add(n)
		}
	}

	if s.People != nil && s.Auth != nil {
		if gate := s.Auth.RequiredGroup(); gate != "" {
			adminRole, riderRole := s.permissionRoleNames()
			var roles []string
			if adminRole != "" {
				roles = append(roles, adminRole)
			}
			if riderRole != "" {
				roles = append(roles, riderRole)
			}
			people, err := s.People.ListPeople(ctx, gate, roles...)
			if err != nil {
				s.logger().Warn("people access: could not look the person up", "id", id, "err", err)
			}
			for _, p := range people {
				if p.UserID != id {
					continue
				}
				add(likelyRider(p.Name, p.Nickname, p.UserID))
				if email == "" {
					email = strings.ToLower(strings.TrimSpace(p.Email))
				}
			}
		}
	}

	if s.MorningSummaries != nil && email != "" {
		names, err := s.MorningSummaries.RidersByEmail(ctx, email)
		if err != nil {
			s.logger().Warn("people access: could not look riders up by email", "err", err)
		}
		for _, n := range names {
			add(n)
		}
	}

	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// revokeBackgroundAccess takes away the two things that work without a
// session: the rider's calendar link (revoked, so the old URL is a 404 for
// good) and their morning summary (switched off, stored address cleared). It
// never re-enables anything on its own: a rider who is let back in makes a new
// link and opts in again. Failures are logged and the rest carried on, because
// the admin action that triggered this has already been made.
func (s *Server) revokeBackgroundAccess(ctx context.Context, riders []string, why string) {
	for _, rider := range riders {
		if s.CalendarFeeds != nil {
			if err := s.CalendarFeeds.Revoke(ctx, rider); err != nil {
				s.logger().Error("could not revoke a calendar feed", "rider", rider, "reason", why, "err", err)
			}
		}
		if s.MorningSummaries != nil {
			if err := s.MorningSummaries.Disable(ctx, rider, s.now()); err != nil {
				s.logger().Error("could not switch off a morning summary", "rider", rider, "reason", why, "err", err)
			}
		}
		s.logger().Info("calendar feed revoked and morning summary switched off", "rider", rider, "reason", why)
	}
}

// keepsTraining is whether the roles still let a person use the training
// features, which is what a feed and a summary belong to.
func (s *Server) keepsTraining(roleNames []string) bool {
	return s.Auth.ResolveRole(roleNames).Can(auth.PermManageTraining)
}
