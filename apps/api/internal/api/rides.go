package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

type rideDTO struct {
	ID        string `json:"id"`
	CrewID    string `json:"crewId"`
	Slug      string `json:"slug"`
	RouteName string `json:"routeName"`
	Date      string `json:"date"`
	CreatedBy string `json:"createdBy"`
}

// scheduleAvailable mirrors crewAvailable — see its own doc comment for why
// every handler checks this rather than assuming.
func (s *Server) scheduleAvailable(w http.ResponseWriter) bool {
	if s.Schedule != nil {
		return true
	}
	writeJSON(w, http.StatusPreconditionFailed, map[string]string{
		"error": "this deployment has no schedule store configured",
	})
	return false
}

// crewAuthority is what a caller may do with one crew, resolved once per
// request from its membership list.
type crewAuthority struct {
	// mine is owner or admin — may manage the roster and always may
	// schedule, the same authority handleSetCrewAutoShare already gates on.
	mine bool
	// approved is any current member, including mine — enough to read
	// rides (see handleListRides), though not enough on its own to create
	// or manage them.
	approved bool
	// canSchedule is mine, or an approved member whose own
	// crew.Member.CanSchedule grant is set.
	canSchedule bool
}

func crewAuthorityFor(identity auth.Identity, c crew.Crew, members []crew.Member) crewAuthority {
	a := crewAuthority{mine: identity.CanEditRoute(c.Owner)}
	if a.mine {
		a.approved = true
		a.canSchedule = true
		return a
	}
	for _, m := range members {
		if m.Status != crew.StatusApproved || !strings.EqualFold(m.Rider, identity.User) {
			continue
		}
		a.approved = true
		a.canSchedule = m.CanSchedule
		break
	}
	return a
}

// handleListRides returns every ride a crew has scheduled. Any approved
// member may see it — the same audience the roster's own read side
// (Members, revealed to Mine only) is more restrictive than on purpose:
// unlike who-else-asked-to-join, a scheduled ride is the point of telling
// the whole crew, not something to keep from anyone but the owner.
func (s *Server) handleListRides(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) || !s.scheduleAvailable(w) {
		return
	}

	id := r.PathValue("id")
	c, err := s.Crew.Get(r.Context(), id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	members, err := s.Crew.Members(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if authority := crewAuthorityFor(identity, c, members); !authority.approved {
		s.forbidCrew(w, r)
		return
	}

	rides, err := s.Schedule.ListForCrew(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}

	routes, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	names := make(map[string]string, len(routes))
	for _, route := range routes {
		names[route.Slug] = route.Name
	}

	out := make([]rideDTO, 0, len(rides))
	for _, ride := range rides {
		out = append(out, rideDTOFor(ride, names))
	}
	writeJSON(w, http.StatusOK, out)
}

func rideDTOFor(ride schedule.Ride, routeNames map[string]string) rideDTO {
	// A route deleted after being scheduled leaves routeNames with nothing
	// for this slug — falls back to the slug itself rather than shipping an
	// empty routeName the frontend would have to notice and paper over on
	// its own. The ride row itself is still a real, deletable thing even
	// once its route is gone (see handleDeleteRide's own doc comment: a
	// ride never reaches back to touch the route or its targets, so
	// nothing here needs the route to still exist).
	name := routeNames[ride.Slug]
	if name == "" {
		name = ride.Slug
	}
	return rideDTO{
		ID: ride.ID, CrewID: ride.CrewID, Slug: ride.Slug,
		RouteName: name, Date: ride.Date, CreatedBy: ride.CreatedBy,
	}
}

// handleCreateRide schedules a ride: an existing route, for a crew, on a
// day. Mine (owner/admin) or a member with their own CanSchedule grant.
//
// The named route must already be shared with this crew, or this call must
// itself have the authority to share it — the same authority
// validateCrewTargets already requires of any write that changes a route's
// targets (the route's owner, or an admin, via Identity.CanEditRoute). A
// member holding only CanSchedule, scheduling someone else's route, cannot
// retarget that route themselves — CanSchedule is not a route-edit
// permission — so for them the route has to already be shared here; the
// natural workflow is the route's owner shares it once (from the Library
// page, as today), and any granted member can schedule it afterward.
//
// A route the caller cannot otherwise see (config.VisibleTo — see the
// visibleRoutes filter below) is rejected the same way a genuinely
// nonexistent slug is: 404, "no such route," never a 400 that would
// distinguish "exists but not shared with you" from "doesn't exist."
// Anything more specific would let a rider probe slugs outside their own
// crews to learn whether they exist.
func (s *Server) handleCreateRide(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) || !s.scheduleAvailable(w) {
		return
	}

	id := r.PathValue("id")
	c, err := s.Crew.Get(r.Context(), id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	members, err := s.Crew.Members(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	authority := crewAuthorityFor(identity, c, members)
	if !authority.canSchedule {
		s.forbidCrew(w, r)
		return
	}

	var body struct {
		Slug string `json:"slug"`
		Date string `json:"date"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	routes, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	crews, ok := s.crewSnapshot(w, r)
	if !ok {
		return
	}
	// Filtered through the same visibility rule every other route-facing
	// handler uses (visibleRoutes/config.VisibleTo — see handleRoutes,
	// handleTrack's mayView) before ever searching by slug: without this, a
	// rider who may schedule for *some* crew of their own — trivially any
	// rider, since owning any crew grants it — could probe an arbitrary
	// slug across the whole deployment and learn from the response whether
	// it exists, is owned, or is shared, none of which they have any
	// relationship to. Route existence must not leak beyond what the
	// caller could already see.
	routes = visibleRoutes(routes, identity, crews)
	idx := -1
	for i, route := range routes {
		if route.Slug == body.Slug {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such route"})
		return
	}
	route := routes[idx]

	// Resolves the same way TargetsFor does: naming the crew in Targets is
	// not enough on its own if the route's owner has since left it (a
	// stale target TargetsFor already treats as reaching nobody) — that
	// case has to fall through to the re-share attempt below, which then
	// correctly fails the same way a fresh share attempt would.
	shared := false
	if route.Targets != nil && crews.ApprovedRiders.Has(id, route.Owner) {
		for _, t := range *route.Targets {
			if t == id {
				shared = true
				break
			}
		}
	}
	if !shared {
		if !identity.CanEditRoute(route.Owner) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "this route isn't shared with this crew yet — its owner needs to share it first",
			})
			return
		}
		existing := []string{}
		if route.Targets != nil {
			existing = *route.Targets
		}
		newTargets := append(append([]string{}, existing...), id)
		if err := validateCrewTargets(newTargets, route.Owner, crews); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if _, err := s.Source.Update(r.Context(), route.Slug, source.UpdateRequest{Targets: &newTargets}); err != nil {
			s.fail(w, err)
			return
		}
	}

	ride, err := s.Schedule.Create(r.Context(), id, route.Slug, body.Date, identity.User)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("crew ride scheduled", "crew", id, "slug", route.Slug, "date", body.Date, "by", identity.User)
	writeJSON(w, http.StatusCreated, rideDTOFor(ride, map[string]string{route.Slug: route.Name}))
}

// handleDeleteRide removes a scheduled ride — its creator, the crew's
// owner, or an admin. "Its creator" means *currently*: someone who has
// since been removed from the crew keeps no lingering right to touch it —
// authority is re-derived from the crew's live membership on every call
// (crewAuthorityFor), the same as every other crew endpoint, rather than
// trusting the identity on the ride row alone. Removing a ride never
// touches the route or the crew's own targets; see schedule.Store.Delete's
// doc comment.
func (s *Server) handleDeleteRide(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) || !s.scheduleAvailable(w) {
		return
	}

	id, rideID := r.PathValue("id"), r.PathValue("rideId")
	c, err := s.Crew.Get(r.Context(), id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	ride, err := s.Schedule.Get(r.Context(), rideID)
	if err != nil {
		s.failScheduleLookup(w, err)
		return
	}
	if ride.CrewID != id {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such scheduled ride"})
		return
	}
	members, err := s.Crew.Members(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}

	identity := auth.FromContext(r.Context())
	authority := crewAuthorityFor(identity, c, members)
	if !authority.mine && (!authority.approved || !strings.EqualFold(ride.CreatedBy, identity.User)) {
		s.forbidCrew(w, r)
		return
	}

	if err := s.Schedule.Delete(r.Context(), rideID); err != nil {
		s.failScheduleLookup(w, err)
		return
	}

	s.logger().Info("crew ride removed", "crew", id, "ride", rideID, "by", identity.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) failScheduleLookup(w http.ResponseWriter, err error) {
	if errors.Is(err, schedule.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.fail(w, err)
}
