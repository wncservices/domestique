package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/crewplan"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
)

// Ride together: members of a crew who say which weekdays they are open to a
// shared ride, and a proposal of one day and one route for a week when two of
// them each have a long or endurance ride to move. See
// docs/superpowers/specs/2026-09-29-crew-planning-design.md.
//
// What crosses between riders is exactly: the weekdays a member is open to
// (shown to the crew's approved members), and a proposal's week, day, route, the
// riders in it and each one's answer. The server reads each opted-in rider's own
// plan to compute a proposal, on behalf of the group that consented, and the
// response never carries any of it. No endpoint here writes another rider's
// workouts: a rider's own row, and their own plan on their own confirmation, are
// the only things written about them.

// ---------- DTOs ----------
//
// Mirrored by hand in apps/web/src/api/types.ts — change them together.

type togetherDaysDTO struct {
	Rider string   `json:"rider"`
	Days  []string `json:"days"`
}

type rideTogetherMemberDTO struct {
	Rider  string `json:"rider"`
	Status string `json:"status"`
}

type rideTogetherDTO struct {
	ID        string `json:"id"`
	CrewID    string `json:"crewId"`
	CrewName  string `json:"crewName"`
	WeekStart string `json:"weekStart"`
	Day       string `json:"day"`
	RouteSlug string `json:"routeSlug"`
	RouteName string `json:"routeName"`
	// Status is open or agreed; an ended or stale proposal is never listed.
	Status string `json:"status"`
	// YourStatus is the caller's own answer: pending, accepted or declined.
	YourStatus string                  `json:"yourStatus"`
	Members    []rideTogetherMemberDTO `json:"members"`
}

type rideTogetherListDTO struct {
	Proposals []rideTogetherDTO `json:"proposals"`
}

// validWeekdays are the days a rider may offer.
var validWeekdays = map[string]bool{"mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true, "sun": true}

// ---------- the flag ----------

// handleSetTogether is PUT /api/crews/{id}/together {days}: the caller says
// which weekdays they are open to riding together with this crew. It writes the
// session rider's own row and nobody's else; an empty list deletes it. Only an
// approved member may.
func (s *Server) handleSetTogether(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) || !s.scheduleAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User
	id := r.PathValue("id")

	c, err := s.Crew.Get(ctx, id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	members, err := s.Crew.Members(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	approved := false
	for _, m := range members {
		if m.Status == crew.StatusApproved && strings.EqualFold(m.Rider, rider) {
			approved = true
		}
	}
	if !approved {
		s.logger().Info("together days refused: not a member", "crew", c.ID, "rider", rider)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a member of this crew may say they are open to riding together"})
		return
	}

	var body struct {
		Days []string `json:"days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	days := make([]string, 0, len(body.Days))
	for _, d := range body.Days {
		d = strings.ToLower(strings.TrimSpace(d))
		if !validWeekdays[d] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "days must be mon, tue, wed, thu, fri, sat or sun"})
			return
		}
		days = append(days, d)
	}
	if err := s.Schedule.SetTogether(ctx, id, rider, days); err != nil {
		s.fail(w, err)
		return
	}
	stored, err := s.Schedule.TogetherFor(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("ride together days set", "crew", c.ID, "rider", rider, "open", len(stored[strings.ToLower(rider)]) > 0)
	writeJSON(w, http.StatusOK, togetherDaysDTO{Rider: strings.ToLower(rider), Days: orEmpty(stored[strings.ToLower(rider)])})
}

// attachTogether adds who is open to riding together to a crew's DTO, for an
// approved member of that crew. A non-member and a pending member see none: the
// flag is the offer itself, shown to the members it is offered to.
func (s *Server) attachTogether(ctx context.Context, dto *crewDTO) {
	if s.Schedule == nil || dto.MembershipStatus != string(crew.StatusApproved) {
		return
	}
	flags, err := s.Schedule.TogetherFor(ctx, dto.ID)
	if err != nil {
		s.logger().Warn("could not read together days for a crew", "crew", dto.ID, "err", err)
		return
	}
	inCrew := map[string]bool{}
	for _, name := range dto.Roster {
		inCrew[strings.ToLower(name)] = true
	}
	var riders []string
	for rider := range flags {
		if inCrew[rider] {
			riders = append(riders, rider)
		}
	}
	sort.Strings(riders)
	for _, rider := range riders {
		dto.Together = append(dto.Together, togetherDaysDTO{Rider: rider, Days: flags[rider]})
	}
}

// ---------- proposals ----------

// rideTogetherWeeks are the weeks proposals are made for: this one and next.
func (s *Server) rideTogetherWeeks() []string {
	monday := periodization.MondayOf(s.now())
	return []string{monday.Format(dateLayout), monday.AddDate(0, 0, 7).Format(dateLayout)}
}

// handleRideTogether is GET /api/training/ride-together: the caller's open and
// agreed proposals for this week and next. A proposal is computed here, when
// either rider reads, and stored once per crew and week so every rider sees the
// same one; there is no background loop. A stored one whose premise no longer
// holds is marked stale and hidden, and the week proposed again if it still can be.
func (s *Server) handleRideTogether(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	if !s.crewAvailable(w) || !s.scheduleAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User

	out, err := s.rideTogetherFor(ctx, rider)
	if err != nil {
		s.logger().Error("ride together failed", "rider", rider, "err", err)
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rideTogetherListDTO{Proposals: out})
}

// rideTogetherFor refreshes the proposals of every crew rider is in and returns
// the caller's own.
func (s *Server) rideTogetherFor(ctx context.Context, rider string) ([]rideTogetherDTO, error) {
	snap, err := s.Crew.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	weeks := s.rideTogetherWeeks()
	for _, c := range snap.Crews {
		if !snap.ApprovedRiders.Has(c.ID, rider) {
			continue
		}
		for _, week := range weeks {
			if err := s.refreshProposal(ctx, snap, c, week); err != nil {
				return nil, err
			}
		}
	}

	mine, err := s.Schedule.ProposalsForRider(ctx, rider, weeks)
	if err != nil {
		return nil, err
	}
	crewNames := make(map[string]string, len(snap.Crews))
	for _, c := range snap.Crews {
		crewNames[c.ID] = c.Name
	}
	routes, err := s.routesBySlug(ctx)
	if err != nil {
		return nil, err
	}
	out := []rideTogetherDTO{}
	for _, p := range mine {
		if p.Status != schedule.ProposalOpen && p.Status != schedule.ProposalAgreed {
			continue
		}
		dto := rideTogetherDTO{
			ID: p.ID, CrewID: p.CrewID, CrewName: crewNames[p.CrewID], WeekStart: p.WeekStart, Day: p.Day,
			RouteSlug: p.RouteSlug, RouteName: p.RouteSlug, Status: string(p.Status), Members: []rideTogetherMemberDTO{},
		}
		if rt, ok := routes[p.RouteSlug]; ok {
			dto.RouteName = rt.Name
		}
		for _, m := range p.Members {
			dto.Members = append(dto.Members, rideTogetherMemberDTO{Rider: m.Rider, Status: string(m.Status)})
			if strings.EqualFold(m.Rider, rider) {
				dto.YourStatus = string(m.Status)
			}
		}
		out = append(out, dto)
	}
	return out, nil
}

// refreshProposal makes the stored proposal of one crew and week current: a
// stored open one that still holds stays; one that does not is marked stale; and
// when there is none to keep, a fresh one is computed and stored if the group
// qualifies. An ended or agreed proposal is final for its week.
func (s *Server) refreshProposal(ctx context.Context, snap crew.Snapshot, c crew.Crew, week string) error {
	flags, err := s.Schedule.TogetherFor(ctx, c.ID)
	if err != nil {
		return err
	}
	stored, have, err := s.Schedule.ProposalFor(ctx, c.ID, week)
	if err != nil {
		return err
	}
	if have && (stored.Status == schedule.ProposalEnded || stored.Status == schedule.ProposalAgreed) {
		return nil
	}

	// Everyone who could be in a proposal: the opted-in approved members, and
	// anyone already in a stored one (to check what they have done since).
	riders := map[string]bool{}
	for rider := range flags {
		if snap.ApprovedRiders.Has(c.ID, rider) {
			riders[rider] = true
		}
	}
	if have {
		for _, m := range stored.Members {
			riders[m.Rider] = true
		}
	}
	if len(riders) < 2 && !have {
		return nil
	}
	cands, err := s.togetherCandidates(ctx, riders, flags)
	if err != nil {
		return err
	}
	routes, err := s.togetherRoutes(ctx, snap, riders)
	if err != nil {
		return err
	}
	now := s.now()
	group := crewplan.Group{CrewID: c.ID, WeekStart: week, Candidates: cands, Routes: routes}

	if have && stored.Status == schedule.ProposalOpen {
		states := make([]crewplan.MemberState, 0, len(stored.Members))
		for _, m := range stored.Members {
			states = append(states, crewplan.MemberState{Rider: m.Rider, Accepted: m.Status == schedule.MemberAccepted, WorkoutID: m.WorkoutID})
		}
		if crewplan.Premise(crewplan.PremiseInput{Group: group, Day: stored.Day, RouteSlug: stored.RouteSlug, Members: states, Now: now}) {
			return nil
		}
		if err := s.Schedule.SetProposalStatus(ctx, stored.ID, schedule.ProposalStale); err != nil {
			return err
		}
		s.logger().Info("ride together proposal went stale", "crew", c.ID, "proposal", stored.ID)
	}

	for _, p := range crewplan.Align(crewplan.AlignInput{Groups: []crewplan.Group{group}, Now: now}) {
		created, isNew, err := s.Schedule.CreateProposal(ctx, schedule.Proposal{
			CrewID: p.CrewID, WeekStart: p.WeekStart, Day: p.Day, RouteSlug: p.RouteSlug, Riders: p.Riders,
		})
		if err != nil {
			return err
		}
		if isNew {
			s.logger().Info("ride together proposed", "crew", c.ID, "proposal", created.ID, "riders", len(p.Riders))
		}
	}
	return nil
}

// togetherCandidates reads each rider's own plan for the group that opted in. It
// reads and never writes; what it reads never leaves crewplan's answer.
func (s *Server) togetherCandidates(ctx context.Context, riders map[string]bool, flags map[string][]string) ([]crewplan.Candidate, error) {
	names := make([]string, 0, len(riders))
	for r := range riders {
		names = append(names, r)
	}
	sort.Strings(names)
	today := s.now().Format(dateLayout)
	var out []crewplan.Candidate
	for _, rider := range names {
		workouts, err := s.Training.ListWorkouts(ctx, rider)
		if err != nil {
			return nil, err
		}
		ridden, err := s.riddenToday(ctx, rider, today, workouts)
		if err != nil {
			return nil, err
		}
		blackout, err := s.blackoutFor(ctx, rider)
		if err != nil {
			return nil, err
		}
		out = append(out, crewplan.Candidate{
			Rider: rider, Days: flags[rider], Workouts: workouts, Ridden: ridden, Blackout: blackout,
		})
	}
	return out, nil
}

// togetherRoutes are the library's routes with how long each would take and which
// of the riders may see it (config.VisibleTo against the crew snapshot).
func (s *Server) togetherRoutes(ctx context.Context, snap crew.Snapshot, riders map[string]bool) ([]crewplan.RouteOption, error) {
	routes, err := s.routesBySlug(ctx)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, 0, len(routes))
	for slug := range routes {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	var out []crewplan.RouteOption
	for _, slug := range slugs {
		rt := routes[slug]
		visible := map[string]bool{}
		for rider := range riders {
			if config.VisibleTo(rt, rider, snap) {
				visible[rider] = true
			}
		}
		out = append(out, crewplan.RouteOption{Slug: slug, Seconds: crewplan.EstimateRoute(rt.Stats).Seconds(), VisibleTo: visible})
	}
	return out, nil
}
