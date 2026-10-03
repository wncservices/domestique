package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/crewplan"
	"github.com/wncservices/domestique/apps/api/internal/schedule"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Confirming a proposal is each rider's own write. Accepting moves the caller's
// own qualifying session and records the caller's own answer; declining records
// it and ends the proposal for the week. Neither names a rider (the caller comes
// from the session and no body is read), neither reads or writes anyone else's
// plan to act, and accepting does not wait for anyone else. A rider who accepted
// has an ordinary plan session afterwards: not fixed, not owned by the crew.

const (
	togetherEndedMessage = "That ride together is no longer on offer."
	togetherStaleMessage = "That ride together no longer works for everyone, so nothing was moved."
)

// handleAcceptRideTogether is POST /api/training/ride-together/{id}/accept.
func (s *Server) handleAcceptRideTogether(w http.ResponseWriter, r *http.Request) {
	s.answerRideTogether(w, r, true)
}

// handleDeclineRideTogether is POST /api/training/ride-together/{id}/decline.
func (s *Server) handleDeclineRideTogether(w http.ResponseWriter, r *http.Request) {
	s.answerRideTogether(w, r, false)
}

func (s *Server) answerRideTogether(w http.ResponseWriter, r *http.Request, accept bool) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	if !s.crewAvailable(w) || !s.scheduleAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User
	id := r.PathValue("id")

	// A proposal the caller is not in does not exist for them.
	p, err := s.Schedule.GetProposal(ctx, id)
	if err != nil && !errors.Is(err, schedule.ErrNotFound) {
		s.fail(w, err)
		return
	}
	mine, member := schedule.Member{}, false
	for _, m := range p.Members {
		if strings.EqualFold(m.Rider, rider) {
			mine, member = m, true
		}
	}
	if err != nil || !member {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such ride together"})
		return
	}

	var out answerOutcome
	ran := withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
		s.seasonMu.Lock()
		defer s.seasonMu.Unlock()
		if accept {
			out = s.acceptRideTogether(ctx, rider, id)
		} else {
			out = s.declineRideTogether(ctx, rider, id, mine)
		}
	})
	if !ran {
		s.writeReplanLocked(w, rider)
		return
	}
	switch {
	case out.err != nil:
		s.logger().Error("ride together answer failed", "rider", rider, "proposal", id, "accept", accept, "err", out.err)
		s.fail(w, out.err)
		return
	case out.status != 0:
		s.logger().Info("ride together answer refused", "rider", rider, "proposal", id, "accept", accept, "status", out.status)
		body := map[string]any{"error": out.message}
		if out.fresh {
			// The picture the rider should be looking at now.
			proposals, err := s.rideTogetherFor(ctx, rider)
			if err != nil {
				s.fail(w, err)
				return
			}
			body["proposals"] = proposals
		}
		writeJSON(w, out.status, body)
		return
	}
	s.logger().Info("ride together answered", "rider", rider, "proposal", id, "accept", accept, "moved", out.moved)
	proposals, err := s.rideTogetherFor(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rideTogetherListDTO{Proposals: proposals})
}

type answerOutcome struct {
	status  int
	message string
	fresh   bool // the response carries the caller's fresh proposals
	moved   bool
	err     error
}

func refuseAnswer(status int, msg string, fresh bool) answerOutcome {
	return answerOutcome{status: status, message: msg, fresh: fresh}
}

// declineRideTogether records the caller's decline and ends the proposal for
// the week. It writes no workout, anybody's.
func (s *Server) declineRideTogether(ctx context.Context, rider, id string, mine schedule.Member) answerOutcome {
	p, err := s.Schedule.GetProposal(ctx, id)
	if err != nil {
		return answerOutcome{err: err}
	}
	switch {
	case mine.Status == schedule.MemberDeclined:
		return answerOutcome{}
	case mine.Status == schedule.MemberAccepted:
		return refuseAnswer(http.StatusConflict, "You already accepted this, so your session has moved. Move it back yourself if you changed your mind.", false)
	case p.Status != schedule.ProposalOpen:
		return refuseAnswer(http.StatusConflict, togetherEndedMessage, false)
	}
	if err := s.Schedule.SetMemberStatus(ctx, id, rider, schedule.MemberDeclined, ""); err != nil {
		return answerOutcome{err: err}
	}
	if err := s.Schedule.SetProposalStatus(ctx, id, schedule.ProposalEnded); err != nil {
		return answerOutcome{err: err}
	}
	return answerOutcome{}
}

// acceptRideTogether moves the caller's qualifying session onto the shared day,
// revalidated server-side first: a premise that no longer holds is a 409 with
// the fresh proposals and nothing moved. The move notes the day it left
// (scheduler.MovedFrom), so scheduling keeps the vacated day taken, and carries
// the adjusted marker, so it is the rider's session from then on.
func (s *Server) acceptRideTogether(ctx context.Context, rider, id string) answerOutcome {
	p, err := s.Schedule.GetProposal(ctx, id)
	if err != nil {
		return answerOutcome{err: err}
	}
	var mine schedule.Member
	for _, m := range p.Members {
		if strings.EqualFold(m.Rider, rider) {
			mine = m
		}
	}
	switch {
	case mine.Status == schedule.MemberAccepted:
		return answerOutcome{} // idempotent
	case mine.Status == schedule.MemberDeclined || p.Status != schedule.ProposalOpen:
		return refuseAnswer(http.StatusConflict, togetherEndedMessage, true)
	}

	snap, err := s.Crew.Snapshot(ctx)
	if err != nil {
		return answerOutcome{err: err}
	}
	c, err := s.Crew.Get(ctx, p.CrewID)
	if err != nil {
		if errors.Is(err, crew.ErrNotFound) {
			return refuseAnswer(http.StatusConflict, togetherEndedMessage, true)
		}
		return answerOutcome{err: err}
	}
	group, err := s.proposalGroup(ctx, snap, p)
	if err != nil {
		return answerOutcome{err: err}
	}
	states := make([]crewplan.MemberState, 0, len(p.Members))
	for _, m := range p.Members {
		states = append(states, crewplan.MemberState{Rider: m.Rider, Accepted: m.Status == schedule.MemberAccepted, WorkoutID: m.WorkoutID})
	}
	now := s.now()
	if !snap.ApprovedRiders.Has(c.ID, rider) ||
		!crewplan.Premise(crewplan.PremiseInput{Group: group, Day: p.Day, RouteSlug: p.RouteSlug, Members: states, Now: now}) {
		if err := s.Schedule.SetProposalStatus(ctx, id, schedule.ProposalStale); err != nil {
			return answerOutcome{err: err}
		}
		return refuseAnswer(http.StatusConflict, togetherStaleMessage, true)
	}

	var own crewplan.Candidate
	for _, cand := range group.Candidates {
		if strings.EqualFold(cand.Rider, rider) {
			own = cand
		}
	}
	sess, ok := crewplan.QualifyingSession(own, p.WeekStart, now)
	if !ok {
		return refuseAnswer(http.StatusConflict, togetherStaleMessage, true)
	}

	moved := sess.Date != p.Day
	if moved {
		routes, err := s.routesBySlug(ctx)
		if err != nil {
			return answerOutcome{err: err}
		}
		routeName := p.RouteSlug
		if rt, ok := routes[p.RouteSlug]; ok {
			routeName = rt.Name
		}
		description := sess.Description + " " + scheduler.AdjustedMarker + " moved from " + sess.Date +
			" - Ride together: " + routeName + " with your crew."
		day := p.Day
		if _, err := s.Training.UpdateWorkout(ctx, sess.ID, workout.UpdateWorkoutRequest{Date: &day, Description: &description}); err != nil {
			return answerOutcome{err: err}
		}
	}
	if err := s.Schedule.SetMemberStatus(ctx, id, rider, schedule.MemberAccepted, sess.ID); err != nil {
		if moved {
			// Put the session back as it was: the answer did not land.
			date, desc := sess.Date, sess.Description
			if _, rerr := s.Training.UpdateWorkout(ctx, sess.ID, workout.UpdateWorkoutRequest{Date: &date, Description: &desc}); rerr != nil {
				s.logger().Error("could not put a session back after a failed ride together answer", "rider", rider, "proposal", id, "err", rerr)
			}
		}
		return answerOutcome{err: err}
	}
	if err := s.agreeIfEveryoneAccepted(ctx, id); err != nil {
		return answerOutcome{err: err}
	}
	return answerOutcome{moved: moved}
}

// agreeIfEveryoneAccepted marks the proposal agreed once every rider has accepted.
func (s *Server) agreeIfEveryoneAccepted(ctx context.Context, id string) error {
	p, err := s.Schedule.GetProposal(ctx, id)
	if err != nil {
		return err
	}
	for _, m := range p.Members {
		if m.Status != schedule.MemberAccepted {
			return nil
		}
	}
	return s.Schedule.SetProposalStatus(ctx, id, schedule.ProposalAgreed)
}

// proposalGroup is a stored proposal's crew and week as crewplan reads it: the
// riders in it (and whoever else is open to it, who cannot change the answer)
// with their own plans, and the routes visible to them.
func (s *Server) proposalGroup(ctx context.Context, snap crew.Snapshot, p schedule.Proposal) (crewplan.Group, error) {
	flags, err := s.Schedule.TogetherFor(ctx, p.CrewID)
	if err != nil {
		return crewplan.Group{}, err
	}
	riders := map[string]bool{}
	for _, m := range p.Members {
		riders[m.Rider] = true
	}
	cands, err := s.togetherCandidates(ctx, riders, flags)
	if err != nil {
		return crewplan.Group{}, err
	}
	routes, err := s.togetherRoutes(ctx, snap, riders)
	if err != nil {
		return crewplan.Group{}, err
	}
	return crewplan.Group{CrewID: p.CrewID, WeekStart: p.WeekStart, Candidates: cands, Routes: routes}, nil
}
