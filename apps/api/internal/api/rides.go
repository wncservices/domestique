package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

type rideDTO struct {
	ID        string `json:"id"`
	CrewID    string `json:"crewId"`
	Slug      string `json:"slug"`
	RouteName string `json:"routeName"`
	Date      string `json:"date"`
	Time      string `json:"time,omitempty"`
	CreatedBy string `json:"createdBy"`
}

// upcomingRideDTO is a rideDTO plus the crew it belongs to — the shape
// GET /api/rides/upcoming needs and a plain rideDTO doesn't: that endpoint
// spans every crew the caller is in, so each row has to say whose ride it
// is, unlike /api/crews/{id}/rides where the crew is already the URL.
type upcomingRideDTO struct {
	rideDTO
	CrewName string `json:"crewName"`
}

// scheduleAvailable mirrors crewAvailable — see its own doc comment for why
// every handler checks this rather than assuming.
func (s *Server) scheduleAvailable(w http.ResponseWriter) bool {
	if s.Schedule != nil {
		return true
	}
	// Error, not warn — see crewAvailable's identical reasoning: this is a
	// wiring defect if it ever fires outside a test, not a deployment
	// choice.
	s.logger().Error("schedule store unavailable — this should never happen outside tests")
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
		RouteName: name, Date: ride.Date, Time: ride.Time, CreatedBy: ride.CreatedBy,
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
		Time string `json:"time"`
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

	ride, err := s.Schedule.Create(r.Context(), id, route.Slug, body.Date, body.Time, identity.User)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("crew ride scheduled", "crew", id, "slug", route.Slug, "date", body.Date, "by", identity.User)
	writeJSON(w, http.StatusCreated, rideDTOFor(ride, map[string]string{route.Slug: route.Name}))
}

// handleUpcomingRides answers "is anything scheduled?" across every crew
// the caller belongs to — the one query behind both the Library page's
// upcoming-ride banner and each crew row's own "next ride" line, so
// neither surface needs its own fetch path. schedule.Store.ListUpcoming is
// unfiltered by crew (see its own doc comment); this handler does the
// filtering, the same division of labour crewAuthorityFor already draws
// everywhere else in this file — the store answers what exists, the API
// layer answers what this caller may see.
//
// from is an optional ?from=YYYY-MM-DD query param, the caller's own idea
// of "today." The server does not default this to its own idea of today:
// this deployment has riders in one timezone and no guarantee the server
// process runs in it (nothing here sets TZ), so time.Now() server-side can
// disagree with a rider's actual local day by up to a couple of hours
// around midnight — the same class of bug CrewsPage.vue's todayISO()
// already works around for the date picker. The browser always knows its
// own local day; the server never reliably knows the rider's, so the
// browser sends it. Omitting the param (any caller besides this app's own
// frontend) falls back to the server's own UTC today, which is still a
// reasonable default — just not one this handler should ever compute for a
// request that already supplied a better answer.
func (s *Server) handleUpcomingRides(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) || !s.scheduleAvailable(w) {
		return
	}

	from := r.URL.Query().Get("from")
	if from == "" {
		from = time.Now().UTC().Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", from); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from must be a date in YYYY-MM-DD form"})
		return
	}

	crews, ok := s.crewSnapshot(w, r)
	if !ok {
		return
	}
	identity := auth.FromContext(r.Context())
	rides, err := s.Schedule.ListUpcoming(r.Context(), from)
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
	crewNames := make(map[string]string, len(crews.Crews))
	for _, c := range crews.Crews {
		crewNames[c.ID] = c.Name
	}

	out := make([]upcomingRideDTO, 0)
	for _, ride := range rides {
		if !crews.ApprovedRiders.Has(ride.CrewID, identity.User) {
			continue
		}
		out = append(out, upcomingRideDTO{
			rideDTO:  rideDTOFor(ride, names),
			CrewName: crewNames[ride.CrewID],
		})
	}
	writeJSON(w, http.StatusOK, out)
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

// handleSyncRide is the one deliberate exception to the rule the rest of
// this app's push machinery now follows (see config.PushTargetsFor's own
// doc comment): a rider looking at a specific scheduled ride, in a crew
// they are themselves an approved member of, explicitly asking for that
// one route to reach that one crew's devices right now. Any approved
// member may trigger it — the same audience handleListRides already
// shows the ride to — not just whoever holds CanSchedule, since sending
// what is already shared and already visible to your own crew fellows'
// devices is a different, lesser thing than deciding what gets shared or
// scheduled in the first place.
//
// Scoped twice over, deliberately more than TargetsFor alone would give:
// routes is just this one ride's route, and linked is narrowed to this
// crew's own currently-approved members before BuildPlan ever runs — not
// the full account list TargetsFor would otherwise be free to resolve
// across, which would also reach any *other* crew this same route
// happens to be independently shared to. A rider syncing a ride from one
// crew's popup must never spill into a different crew the route's owner
// also happens to belong to.
func (s *Server) handleSyncRide(w http.ResponseWriter, r *http.Request) {
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

	ride, err := s.Schedule.Get(r.Context(), rideID)
	if err != nil {
		s.failScheduleLookup(w, err)
		return
	}
	if ride.CrewID != id {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such scheduled ride"})
		return
	}

	routes, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	idx := -1
	for i, route := range routes {
		if route.Slug == ride.Slug {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this ride's route no longer exists"})
		return
	}

	linked, ok := s.linkedAccounts(w)
	if !ok {
		return
	}
	crews, ok := s.crewSnapshot(w, r)
	if !ok {
		return
	}
	scoped := make([]model.Account, 0, len(linked))
	for _, a := range linked {
		if crews.ApprovedRiders.Has(id, a.Rider) {
			scoped = append(scoped, a)
		}
	}

	resp, err := s.applyPush(r.Context(), []model.Route{routes[idx]}, scoped, crews, true, nil)
	if err != nil {
		s.fail(w, err)
		return
	}

	s.logger().Info("crew ride synced", "crew", id, "ride", rideID, "slug", ride.Slug, "by", identity.User)
	writeJSON(w, http.StatusOK, resp)
}
