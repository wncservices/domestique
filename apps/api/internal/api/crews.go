package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crew"
)

type crewMemberDTO struct {
	Rider  string `json:"rider"`
	Status string `json:"status"`
	// Origin distinguishes a pending row's two shapes — "self" (the rider
	// asked to join, waiting on the owner) or "invite" (the owner started
	// it, waiting on the rider) — since they need the owner's own roster
	// view to show a different action for each. Meaningless once approved.
	Origin string `json:"origin,omitempty"`
	// CanSchedule is whether this member may schedule a crew ride, beyond
	// the owner/admin who always may — see crew.Member.CanSchedule. Only
	// meaningful for an approved row; omitted (false) for a pending one.
	CanSchedule bool `json:"canSchedule,omitempty"`
}

type crewDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
	// Mine is whether this identity may manage the crew — its owner, or an
	// admin — the same CanEditRoute idiom accountDTO.Mine already uses for
	// a linked account. Members is revealed under the same condition.
	Mine bool `json:"mine"`
	// MembershipStatus is this viewer's own standing: "none", "pending" or
	// "approved". Always present, even for a crew that isn't theirs — a
	// rider needs it to know whether "Request to join" or "Pending" is the
	// right thing to show.
	MembershipStatus string `json:"membershipStatus"`
	// MembershipOrigin distinguishes the two ways a pending status can have
	// come about — "self" (this rider asked to join; the owner still has to
	// approve) or "invite" (the owner added this rider directly; they still
	// have to confirm) — only meaningful, and only set, when MembershipStatus
	// is "pending". The frontend needs it to offer "Accept/Decline" for an
	// invite instead of just a waiting badge.
	MembershipOrigin string `json:"membershipOrigin,omitempty"`
	// MemberCount is the approved roster size. Always visible — existence
	// and roster size are not sensitive, a rider needs them to discover a
	// crew worth requesting to join.
	MemberCount int `json:"memberCount"`
	// AutoShare is whether a member uploading with no explicit target
	// choice gets it shared here by default. Visible to everyone, not just
	// Mine — it changes what *any* member's own uploads default to, not
	// just the owner's, so any member deciding whether to trust this crew
	// needs to see it. Only the owner or an admin may change it.
	AutoShare bool `json:"autoShare"`
	// CanSchedule is whether this identity may schedule a crew ride for
	// this crew — Mine (owner/admin, always), or an approved member whose
	// own crewMemberDTO.CanSchedule is set. Computed here rather than left
	// for the frontend to derive from Members: Members is only present when
	// Mine, so a granted-but-not-owner member has no other way to learn
	// their own standing from this response.
	CanSchedule bool `json:"canSchedule"`
	// Roster is who is currently, approvedly, in the crew — just names, no
	// status/origin — visible to any approved member (or Mine), not only
	// the owner: unlike a pending request, who is currently in the crew is
	// not information the crew has a reason to keep from its own members.
	// A non-member (MembershipStatus "none" or "pending") gets nothing
	// here; MemberCount above is the only roster information they see.
	Roster []string `json:"roster,omitempty"`
	// Members is who is pending and who is approved, together with each
	// row's own status/origin/canSchedule, omitted unless Mine — nobody
	// else has a reason to see who else asked to join, or manage the
	// per-member schedule grant.
	Members []crewMemberDTO `json:"members,omitempty"`
}

// crewDTOFor builds the DTO for one crew from its full membership list —
// callers already have this (Members returns pending and approved
// together), so this never issues a query of its own.
func (s *Server) crewDTOFor(identity auth.Identity, c crew.Crew, members []crew.Member) crewDTO {
	dto := crewDTO{
		ID: c.ID, Name: c.Name, Owner: c.Owner,
		Mine:             identity.CanEditRoute(c.Owner),
		MembershipStatus: "none",
		AutoShare:        c.AutoShare,
	}
	for _, m := range members {
		if m.Status == crew.StatusApproved {
			dto.MemberCount++
		}
		if strings.EqualFold(m.Rider, identity.User) {
			dto.MembershipStatus = string(m.Status)
			if m.Status == crew.StatusPending {
				dto.MembershipOrigin = string(m.Origin)
			}
			if m.Status == crew.StatusApproved && m.CanSchedule {
				dto.CanSchedule = true
			}
		}
	}
	if dto.Mine {
		dto.CanSchedule = true
	}
	if dto.Mine || dto.MembershipStatus == string(crew.StatusApproved) {
		dto.Roster = make([]string, 0, dto.MemberCount)
		for _, m := range members {
			if m.Status == crew.StatusApproved {
				dto.Roster = append(dto.Roster, m.Rider)
			}
		}
	}
	if dto.Mine {
		dto.Members = make([]crewMemberDTO, 0, len(members))
		for _, m := range members {
			member := crewMemberDTO{Rider: m.Rider, Status: string(m.Status)}
			if m.Status == crew.StatusPending {
				member.Origin = string(m.Origin)
			}
			if m.Status == crew.StatusApproved {
				member.CanSchedule = m.CanSchedule
			}
			dto.Members = append(dto.Members, member)
		}
	}
	return dto
}

// crewAvailable reports whether this deployment has a crew store at all.
// Always true in practice — Server.Crew is wired unconditionally in
// runServe, the same as Source/Store — but every handler checks it rather
// than assuming, the same defensiveness Links.CanStore's own doc comment
// argues for: a nil Server is a valid configuration in tests.
func (s *Server) crewAvailable(w http.ResponseWriter) bool {
	if s.Crew != nil {
		return true
	}
	writeJSON(w, http.StatusPreconditionFailed, map[string]string{
		"error": "this deployment has no crew store configured",
	})
	return false
}

// handleCreateCrew makes a new crew. The caller becomes its owner and its
// first approved member, in one step (crew.Store.Create does both).
func (s *Server) handleCreateCrew(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) {
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no rider in the session"})
		return
	}

	c, err := s.Crew.Create(r.Context(), body.Name, rider)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	members, err := s.Crew.Members(r.Context(), c.ID)
	if err != nil {
		s.fail(w, err)
		return
	}

	s.logger().Info("crew created", "id", c.ID, "owner", rider)
	writeJSON(w, http.StatusCreated, s.crewDTOFor(auth.FromContext(r.Context()), c, members))
}

// handleListCrews returns every crew — existence and roster size are not
// sensitive, so this is unfiltered, unlike handleCreateCrew's write side.
func (s *Server) handleListCrews(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) {
		return
	}

	crews, err := s.Crew.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}

	identity := auth.FromContext(r.Context())
	out := make([]crewDTO, 0, len(crews))
	for _, c := range crews {
		members, err := s.Crew.Members(r.Context(), c.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		out = append(out, s.crewDTOFor(identity, c, members))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteCrew removes a crew and its membership. Owner or admin only.
func (s *Server) handleDeleteCrew(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) {
		return
	}

	id := r.PathValue("id")
	c, err := s.Crew.Get(r.Context(), id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	if !auth.FromContext(r.Context()).CanEditRoute(c.Owner) {
		s.forbidCrew(w, r)
		return
	}

	if err := s.Crew.Delete(r.Context(), id); err != nil {
		s.failCrewLookup(w, err)
		return
	}

	s.logger().Info("crew deleted", "id", id, "by", auth.FromContext(r.Context()).User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSetCrewAutoShare flips whether a member uploading with no explicit
// target choice gets it shared here by default. Owner or admin only — the
// same ownership rule as delete, since this changes what the crew *does*
// for every member, not just the caller's own membership.
func (s *Server) handleSetCrewAutoShare(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) {
		return
	}

	id := r.PathValue("id")
	c, err := s.Crew.Get(r.Context(), id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if !identity.CanEditRoute(c.Owner) {
		s.forbidCrew(w, r)
		return
	}

	var body struct {
		AutoShare bool `json:"autoShare"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if err := s.Crew.SetAutoShare(r.Context(), id, body.AutoShare); err != nil {
		s.failCrewLookup(w, err)
		return
	}

	c, err = s.Crew.Get(r.Context(), id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	members, err := s.Crew.Members(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}

	s.logger().Info("crew auto-share set", "crew", id, "autoShare", body.AutoShare, "by", identity.User)
	writeJSON(w, http.StatusOK, s.crewDTOFor(identity, c, members))
}

// handleSetCanScheduleCrewMember grants or revokes one member's permission
// to schedule a crew ride. Owner or admin only, the same rule as
// auto-share — this changes what someone *other* than the caller may do,
// not the caller's own membership.
func (s *Server) handleSetCanScheduleCrewMember(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) {
		return
	}

	id, rider := r.PathValue("id"), r.PathValue("rider")
	c, err := s.Crew.Get(r.Context(), id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if !identity.CanEditRoute(c.Owner) {
		s.forbidCrew(w, r)
		return
	}

	var body struct {
		CanSchedule bool `json:"canSchedule"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if err := s.Crew.SetCanSchedule(r.Context(), id, rider, body.CanSchedule); err != nil {
		s.failCrewLookup(w, err)
		return
	}

	members, err := s.Crew.Members(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}

	s.logger().Info("crew schedule permission set", "crew", id, "rider", rider, "canSchedule", body.CanSchedule, "by", identity.User)
	writeJSON(w, http.StatusOK, s.crewDTOFor(identity, c, members))
}

// handleJoinCrew records the caller's request to join. Self-service: no
// ownership check, an identity may only ever request on its own behalf
// (the rider comes from the session, never the body or a query param).
func (s *Server) handleJoinCrew(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) {
		return
	}

	id := r.PathValue("id")
	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no rider in the session"})
		return
	}

	if _, err := s.Crew.RequestJoin(r.Context(), id, rider); err != nil {
		switch {
		case errors.Is(err, crew.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such crew"})
		case errors.Is(err, crew.ErrAlreadyMember):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return
	}

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

	s.logger().Info("crew join requested", "crew", id, "rider", rider)
	writeJSON(w, http.StatusOK, s.crewDTOFor(auth.FromContext(r.Context()), c, members))
}

// handleAddCrewMember lets the crew's owner (or an admin) enroll a rider
// directly, without that rider ever requesting to join first — the
// owner-initiated counterpart to handleJoinCrew.
func (s *Server) handleAddCrewMember(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) {
		return
	}

	id := r.PathValue("id")
	c, err := s.Crew.Get(r.Context(), id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if !identity.CanEditRoute(c.Owner) {
		s.forbidCrew(w, r)
		return
	}

	var body struct {
		Rider string `json:"rider"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if _, err := s.Crew.AddMember(r.Context(), id, body.Rider, identity.User); err != nil {
		switch {
		case errors.Is(err, crew.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such crew"})
		case errors.Is(err, crew.ErrAlreadyMember):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return
	}

	members, err := s.Crew.Members(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}

	s.logger().Info("crew member added", "crew", id, "rider", body.Rider, "by", identity.User)
	writeJSON(w, http.StatusOK, s.crewDTOFor(identity, c, members))
}

// handleApproveCrewMember grants a pending member — the same URL for both
// directions of consent, distinguished by who is calling it: a rider
// granting their own row confirms an invite (Confirm; a self-request there
// is refused, since that consent is already the requester's own, waiting on
// the owner), an owner or admin granting someone else's approves a
// self-request (Approve; an invite there is refused for the same reason in
// reverse — the owner cannot supply the invited rider's half themselves).
func (s *Server) handleApproveCrewMember(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) {
		return
	}

	id, rider := r.PathValue("id"), r.PathValue("rider")
	c, err := s.Crew.Get(r.Context(), id)
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())

	if strings.EqualFold(identity.User, rider) {
		if err := s.Crew.Confirm(r.Context(), id, rider); err != nil {
			s.failCrewMemberDecision(w, err)
			return
		}
	} else {
		if !identity.CanEditRoute(c.Owner) {
			s.forbidCrew(w, r)
			return
		}
		if err := s.Crew.Approve(r.Context(), id, rider, identity.User); err != nil {
			s.failCrewMemberDecision(w, err)
			return
		}
	}

	members, err := s.Crew.Members(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}

	s.logger().Info("crew member approved", "crew", id, "rider", rider, "by", identity.User)
	writeJSON(w, http.StatusOK, s.crewDTOFor(identity, c, members))
}

// failCrewMemberDecision maps Approve/Confirm's own errors — including the
// two that say "wrong side of this is trying to grant it" — on top of
// failCrewLookup's crew.ErrNotFound handling.
func (s *Server) failCrewMemberDecision(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, crew.ErrConfirmationRequired):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, crew.ErrNoInvite):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	default:
		s.failCrewLookup(w, err)
	}
}

// handleRemoveCrewMember denies a pending request, removes an approved
// member, or lets a member leave on their own — the same store call
// either way, distinguished only by who may call it: a rider acting on
// their own membership needs no ownership check, anyone acting on someone
// else's needs to be the crew's owner or an admin.
func (s *Server) handleRemoveCrewMember(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageCrews) {
		return
	}
	if !s.crewAvailable(w) {
		return
	}

	id, rider := r.PathValue("id"), r.PathValue("rider")
	identity := auth.FromContext(r.Context())
	if !strings.EqualFold(rider, identity.User) {
		c, err := s.Crew.Get(r.Context(), id)
		if err != nil {
			s.failCrewLookup(w, err)
			return
		}
		if !identity.CanEditRoute(c.Owner) {
			s.forbidCrew(w, r)
			return
		}
	}

	// Deny (pending) and Remove (approved) are separate store calls because
	// the store draws that distinction; here, the caller just wants this
	// rider off the crew regardless of which one it was, so try both and
	// only fail if neither found anything to remove.
	err := s.Crew.Deny(r.Context(), id, rider)
	if errors.Is(err, crew.ErrNotFound) {
		err = s.Crew.Remove(r.Context(), id, rider)
	}
	if err != nil {
		s.failCrewLookup(w, err)
		return
	}

	s.logger().Info("crew member removed", "crew", id, "rider", rider, "by", identity.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// forbidCrew writes the same shape s.require does, for an ownership check
// that happens after the permission gate rather than as part of it.
func (s *Server) forbidCrew(w http.ResponseWriter, r *http.Request) {
	identity := auth.FromContext(r.Context())
	s.logger().Info("crew ownership denied", "user", identity.User, "role", identity.Role, "path", r.URL.Path)
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": "only this crew's owner or an admin may do that",
	})
}

func (s *Server) failCrewLookup(w http.ResponseWriter, err error) {
	if errors.Is(err, crew.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.fail(w, err)
}
