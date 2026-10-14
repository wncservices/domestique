package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/auth0mgmt"
)

// PeopleConnector is the seam against Auth0's Management API — tests must
// not depend on a real tenant, the same reasoning KomootConnector and
// GarminConnector already carry. *auth0mgmt.Client satisfies this directly;
// no wrapper is needed the way LiveGarmin needs one, since none of these
// methods need translating into a different shape first.
type PeopleConnector interface {
	ListPeople(ctx context.Context, gateRole string, permissionRoles ...string) ([]auth0mgmt.Person, error)
	Invite(ctx context.Context, email, name string, roleNames []string) (auth0mgmt.Person, error)
	SetRoles(ctx context.Context, userID string, roleNames []string) error
	SendInviteEmail(ctx context.Context, email string) error
	FindByEmail(ctx context.Context, email string) ([]auth0mgmt.Person, error)
	UpdateName(ctx context.Context, userID, name string) (auth0mgmt.Person, error)
	ListEnrollments(ctx context.Context, userID string) ([]auth0mgmt.Enrollment, error)
	CreateGuardianEnrollmentTicket(ctx context.Context, userID string) (string, error)
	DeleteEnrollment(ctx context.Context, enrollmentID string) error
	SetBlocked(ctx context.Context, userID string, blocked bool) error
	DeleteUser(ctx context.Context, userID string) error
}

type personDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	// Role is the resolved app-level label ("admin", "rider", "viewer") —
	// the same computation Identify runs at sign-in (auth.ResolveRole), not
	// the raw Auth0 role names, so this page shows exactly what a person
	// can actually do rather than what happens to be assigned.
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt,omitempty"`
	LastLogin string `json:"lastLogin,omitempty"`
	// Blocked mirrors Auth0's own blocked flag — see auth0mgmt.Person.Blocked.
	// Lets the People page render a Block/Unblock toggle reflecting current
	// state instead of firing the action blind.
	Blocked bool `json:"blocked,omitempty"`
	// LikelyRider is a best-effort guess at what identityFromToken will
	// resolve this person's rider identity to once they actually sign in —
	// see likelyRider's own doc comment. Empty when nothing legal could be
	// derived; this is a hint for admin tooling (the crew add-member
	// picker), never a source of truth for who someone is.
	LikelyRider string `json:"likelyRider,omitempty"`
	// BoundRider is the rider this account actually signs in as, from the
	// binding (internal/identities), as opposed to LikelyRider, which is only
	// a guess from the profile's name. Empty when the account has no binding
	// or this deployment keeps none.
	BoundRider string `json:"boundRider,omitempty"`
	// BindingStatus is "bound", "unbound", "conflict" (unbound, and the accounts
	// claiming the same likely rider disagree, or the only one joined after the
	// rider's data existed) or "needs-attention" (unbound, the rider has data, and
	// a claimant is unverified or unknown). Empty when this deployment keeps no
	// bindings (anything but mode oidc).
	BindingStatus string `json:"bindingStatus,omitempty"`
}

// likelyRider guesses what identityFromToken (sso.go) would resolve this
// person's rider identity to, from data the Management API already exposes
// — name, then nickname, then the Auth0 user id itself, the same priority
// identityFromToken gives an OIDC token's name/nickname/sub claims (Auth0
// never sends this app a preferred_username, for either connection this
// tenant has, so that first-priority claim has nothing to mirror here).
//
// It exists to close one real gap: a rider who has been invited and signed
// in (so they show up here, and can use the app) but has never uploaded a
// route, linked a provider account, or created a crew leaves no trace
// anywhere else the crew add-member picker's suggestions are built from
// (CrewsPage.vue's knownRiders) — found live, an admin unable to find a
// just-invited rider there at all. This is deliberately duplicated logic
// rather than a shared helper with identityFromToken: that function reads
// live OIDC token claims and is part of the sign-in path itself, worth
// keeping minimal and independently verified; this reads Management API
// profile data for a person who may not have signed in yet at all, and
// getting it wrong only means a wrong suggestion in a picker that already
// lets an admin type an exact rider identity by hand regardless.
func likelyRider(name, nickname, userID string) string {
	for _, candidate := range []string{name, nickname, userID} {
		c := strings.ToLower(strings.TrimSpace(candidate))
		if c != "" && accounts.RiderPattern.MatchString(c) {
			return c
		}
	}
	return ""
}

// peopleAvailable reports whether the deployment has Management API
// credentials at all — same shape as Komoot/Garmin's own optional
// credentials: nil degrades the page, it does not 500 the request.
func (s *Server) peopleAvailable(w http.ResponseWriter) bool {
	if s.People != nil {
		return true
	}
	s.logger().Warn("people page requested but no Auth0 Management API access is configured")
	writeJSON(w, http.StatusPreconditionFailed, map[string]string{
		"error": "this deployment has no Auth0 Management API access configured",
	})
	return false
}

// permissionRoleNames names the two Auth0 roles this page offers a choice
// between, in the order role names below expects (admin first, then
// rider) — auth.Config's own Roles mapping, not hardcoded, so this never
// drifts from what Identify actually resolves at sign-in.
func (s *Server) permissionRoleNames() (admin, rider string) {
	roles := s.Auth.Roles()
	if len(roles.Admin) > 0 {
		admin = roles.Admin[0]
	}
	if len(roles.Rider) > 0 {
		rider = roles.Rider[0]
	}
	return admin, rider
}

// roleNamesFor turns an app-level role label into the Auth0 role names a
// person holding it should have: always the gate role (RequiredGroup —
// "allowed in at all"), plus the permission role for anything above
// viewer. "viewer" is gate-only on purpose: there is no Auth0 role for it
// in this deployment (see roles.tf), it is simply the absence of the other
// two, the same fallback default_role already means at sign-in.
func (s *Server) roleNamesFor(label string) ([]string, error) {
	gate := s.Auth.RequiredGroup()
	if gate == "" {
		return nil, fmt.Errorf("this deployment has no auth.required_group configured — nothing to grant")
	}
	adminRole, riderRole := s.permissionRoleNames()

	switch label {
	case "admin":
		if adminRole == "" {
			return nil, fmt.Errorf("no admin role configured (auth.roles.admin)")
		}
		return []string{gate, adminRole}, nil
	case "rider":
		if riderRole == "" {
			return nil, fmt.Errorf("no rider role configured (auth.roles.rider)")
		}
		return []string{gate, riderRole}, nil
	case "viewer":
		return []string{gate}, nil
	default:
		return nil, fmt.Errorf("unknown role %q (want admin, rider or viewer)", label)
	}
}

func (s *Server) personDTO(p auth0mgmt.Person) personDTO {
	role := s.Auth.ResolveRole(p.Roles)
	dto := personDTO{
		ID: p.UserID, Email: p.Email, Name: p.Name, Role: roleLabel(role),
		LikelyRider: likelyRider(p.Name, p.Nickname, p.UserID),
		Blocked:     p.Blocked,
	}
	if !p.CreatedAt.IsZero() {
		dto.CreatedAt = formatTime(p.CreatedAt)
	}
	if !p.LastLogin.IsZero() {
		dto.LastLogin = formatTime(p.LastLogin)
	}
	return dto
}

// handlePeopleList lists everyone with access to this deployment — every
// gate-role member, each with the role they actually resolve to.
func (s *Server) handlePeopleList(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManagePeople) {
		return
	}
	if !s.peopleAvailable(w) {
		return
	}

	gate := s.Auth.RequiredGroup()
	if gate == "" {
		writeJSON(w, http.StatusOK, []personDTO{})
		return
	}
	adminRole, riderRole := s.permissionRoleNames()
	var permissionRoles []string
	if adminRole != "" {
		permissionRoles = append(permissionRoles, adminRole)
	}
	if riderRole != "" {
		permissionRoles = append(permissionRoles, riderRole)
	}

	people, err := s.People.ListPeople(r.Context(), gate, permissionRoles...)
	if err != nil {
		s.logger().Warn("listing people failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Auth0 would not list who has access just now.",
		})
		return
	}

	views := s.bindingViews(r.Context(), people)
	out := make([]personDTO, 0, len(people))
	for _, p := range people {
		dto := s.personDTO(p)
		if v, ok := views[p.UserID]; ok {
			dto.BoundRider, dto.BindingStatus = v.rider, v.status
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// bindingView is what the People page shows about one account's binding.
type bindingView struct {
	rider, status string
}

// bindingViews reads the bindings once and says, for each listed person,
// which rider they are bound to or why they are not. Nil when this deployment
// keeps no bindings or they cannot be read (logged): the page then shows no
// binding column at all, which is the honest answer, rather than showing every
// account as unbound.
func (s *Server) bindingViews(ctx context.Context, people []auth0mgmt.Person) map[string]bindingView {
	if s.Identities == nil {
		return nil
	}
	all, err := s.Identities.List(ctx)
	if err != nil {
		s.logger().Warn("reading identity bindings for the people page failed", "err", err)
		return nil
	}
	issuer := s.authenticator().OIDC().Issuer
	bySub := map[string]string{}
	boundRiders := map[string]bool{}
	for _, b := range all {
		boundRiders[b.Rider] = true
		if b.Issuer == issuer {
			bySub[b.Sub] = b.Rider
		}
	}
	// The same rule the startup bootstrap used, so the page flags exactly the
	// riders it left alone for an admin. A failure to read which riders have
	// data only loses the "needs attention" distinction.
	data, err := s.Identities.RidersWithData(ctx, riderDataColumns())
	if err != nil {
		s.logger().Warn("reading which riders have data for the people page failed", "err", err)
	}
	plan := planBootstrap(people, func(rider string) bool { return boundRiders[rider] }, data)

	out := make(map[string]bindingView, len(people))
	for _, p := range people {
		if rider, ok := bySub[p.UserID]; ok {
			out[p.UserID] = bindingView{rider: rider, status: "bound"}
			continue
		}
		status := "unbound"
		guess := likelyRider(p.Name, p.Nickname, p.UserID)
		switch {
		case plan.Conflicts[guess] > 0:
			status = "conflict"
		case plan.NeedsAttention[guess] > 0:
			// Data that somebody cannot currently sign in as: an unverified or
			// unknown claimant among several, or a profile the search lacked.
			status = "needs-attention"
		}
		out[p.UserID] = bindingView{status: status}
	}
	return out
}

// handlePeopleInvite creates a new Auth0 account, grants it the requested
// role, and sends the invite email — three steps against two different
// APIs (see auth0mgmt's own package doc), surfaced here as one request
// since a caller has no reasonable use for succeeding at only part of it.
// emailRef is a short, stable stand-in for an email address in a log line, so
// two lines about the same person can be matched without the address itself
// being written anywhere (the mailer package keeps the same policy). The Auth0
// user id is better where there is one; this is for the paths that fail before
// one exists.
func emailRef(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:4])
}

func (s *Server) handlePeopleInvite(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManagePeople) {
		return
	}
	if !s.peopleAvailable(w) {
		return
	}

	var body struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	body.Name = strings.TrimSpace(body.Name)
	if body.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}
	if body.Name == "" {
		body.Name = body.Email
	}

	roleNames, err := s.roleNamesFor(body.Role)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// A Google sign-in creates its own Auth0 identity the first time someone
	// uses it, entirely separate from — and possibly before — anyone tells
	// this app about them (see google_connection.tf in the lab repo). If
	// that already happened, grant access to it directly rather than
	// creating, and inviting, a second identity for the same person: they
	// already have a way to sign in, they just weren't let past the gate
	// role yet.
	existing, err := s.People.FindByEmail(r.Context(), body.Email)
	if err != nil {
		s.logger().Warn("checking for an existing account failed", "ref", emailRef(body.Email), "err", err)
		s.failUpstream(w, "Auth0", err)
		return
	}
	if len(existing) > 1 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("%s already has %d separate sign-ins on this tenant — resolve which one to grant access to in the Auth0 dashboard first", body.Email, len(existing)),
		})
		return
	}
	if len(existing) == 1 {
		if err := s.People.SetRoles(r.Context(), existing[0].UserID, roleNames); err != nil {
			s.logger().Warn("granting access to an existing account failed", "ref", emailRef(body.Email), "err", err)
			s.failUpstream(w, "Auth0", err)
			return
		}
		s.logger().Info("access granted to existing account", "ref", emailRef(body.Email), "role", body.Role, "by", auth.FromContext(r.Context()).User)
		granted := existing[0]
		granted.Roles = roleNames
		writeJSON(w, http.StatusOK, map[string]any{"person": s.personDTO(granted), "granted": true})
		return
	}

	person, err := s.People.Invite(r.Context(), body.Email, body.Name, roleNames)
	if err != nil {
		s.logger().Warn("inviting a person failed", "ref", emailRef(body.Email), "err", err)
		s.failUpstream(w, "Auth0", err)
		return
	}

	// The account and its access both exist at this point — a failure past
	// here means the invite email needs resending, not starting over.
	if err := s.People.SendInviteEmail(r.Context(), body.Email); err != nil {
		s.logger().Warn("sending the invite email failed", "ref", emailRef(body.Email), "err", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"person": s.personDTO(person),
			"error":  "The account was created, but the invite email could not be sent",
		})
		return
	}

	s.logger().Info("person invited", "ref", emailRef(body.Email), "role", body.Role, "by", auth.FromContext(r.Context()).User)
	writeJSON(w, http.StatusCreated, map[string]any{"person": s.personDTO(person)})
}

// handlePeopleSetRole changes which role a person holds — grant/revoke
// against Auth0, computed as a diff by auth0mgmt.SetRoles itself, not
// something this handler works out by hand.
func (s *Server) handlePeopleSetRole(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManagePeople) {
		return
	}
	if !s.peopleAvailable(w) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no person id"})
		return
	}

	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	roleNames, err := s.roleNamesFor(body.Role)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Found before the change, while the person still resolves: see ridersOfPerson.
	var riders []string
	if !s.keepsTraining(roleNames) {
		riders = s.ridersOfPerson(r.Context(), id, "")
	}

	if err := s.People.SetRoles(r.Context(), id, roleNames); err != nil {
		s.logger().Warn("changing a person's role failed", "id", id, "err", err)
		s.failUpstream(w, "Auth0", err)
		return
	}

	// Roles are resolved from the groups stored in the session at sign-in, so
	// a live session keeps the old role (a demoted admin stays one) until it
	// expires. Ending it makes the next sign-in pick the new roles up.
	s.endSessionsOf(r.Context(), id, "role changed")
	// A calendar link and a morning email work with no session, so a role
	// that can no longer train has to take them away explicitly.
	s.revokeBackgroundAccess(r.Context(), riders, "role changed")

	s.logger().Info("person role changed", "id", id, "role", body.Role, "by", auth.FromContext(r.Context()).User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleSetPersonBlocked blocks or unblocks a person — two writes against
// two different things, on purpose: Auth0's own blocked flag
// (SetBlocked) stops the specific identity an admin is looking at from
// signing in, and this app's own blocklist (checked at the OIDC callback,
// see internal/blocklist and sso.go) stops a fresh signup with the same
// email from getting back in too. Unblocking undoes both. Does not touch
// this rider's local data either way — blocking is reversible, unlike
// handleDeletePerson.
func (s *Server) handleSetPersonBlocked(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManagePeople) {
		return
	}
	if !s.peopleAvailable(w) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no person id"})
		return
	}

	var body struct {
		Blocked bool   `json:"blocked"`
		Email   string `json:"email"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	if body.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}

	// Found before the change, while the person's sessions still exist.
	var riders []string
	if body.Blocked {
		riders = s.ridersOfPerson(r.Context(), id, body.Email)
	}

	if err := s.People.SetBlocked(r.Context(), id, body.Blocked); err != nil {
		s.logger().Warn("changing a person's blocked status failed", "id", id, "err", err)
		s.failUpstream(w, "Auth0", err)
		return
	}

	if body.Blocked {
		// Auth0 refuses the next sign-in; a session they already hold, a
		// calendar link and the morning email would each carry on, so all three
		// are ended here. Unblocking restores nothing of this: the rider signs in,
		// makes a new link and opts in again.
		s.endSessionsOf(r.Context(), id, "person blocked")
		s.revokeBackgroundAccess(r.Context(), riders, "person blocked")
	}

	// Blocklist is wired unconditionally in a real deployment (see
	// server.go's own doc comment on the field) — nil here only in a test
	// that has not wired one, not a "feature not configured" case, so this
	// is a loud log rather than a 412 the way peopleAvailable degrades.
	if s.Blocklist == nil {
		s.logger().Error("blocklist unavailable — this should never happen outside tests")
	} else {
		var err error
		if body.Blocked {
			err = s.Blocklist.Block(r.Context(), body.Email, auth.FromContext(r.Context()).User, body.Reason)
		} else {
			err = s.Blocklist.Unblock(r.Context(), body.Email)
		}
		if err != nil {
			s.logger().Warn("updating the local blocklist failed", "ref", emailRef(body.Email), "err", err)
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "updated",
				"error":  "Auth0 was updated, but the local blocklist could not be",
			})
			return
		}
	}

	s.logger().Info("person blocked status changed", "id", id, "blocked", body.Blocked, "by", auth.FromContext(r.Context()).User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// endSessionsOf ends every login of one identity (an Auth0 user id, which is
// the session's sub). A failure is logged and does not fail the request: the
// Auth0 change the admin asked for is the point, and ending sessions is the
// follow-through that expires on its own within sessionTTL anyway.
func (s *Server) endSessionsOf(ctx context.Context, sub, why string) {
	if s.Sessions == nil {
		return
	}
	if err := s.Sessions.DeleteSub(ctx, sub); err != nil {
		s.logger().Error("could not end the sessions of an identity; they stay valid until they expire", "reason", why, "id", sub, "err", err)
	}
}

// handleDeletePerson removes a person entirely: this app's own local data
// for their rider identity first (best-effort guess at which one — see
// personDTO.LikelyRider, which the admin confirms or edits client-side
// before this request is even made), then their Auth0 identity itself.
// Purge-before-identity on purpose, the same ordering handleDeleteMe uses:
// a failure here never leaves an Auth0 identity gone with local data still
// attached to it. Unlike blocking, this does not touch the local blocklist
// — a deleted rider is explicitly allowed to sign up fresh.
func (s *Server) handleDeletePerson(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManagePeople) {
		return
	}
	if !s.peopleAvailable(w) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no person id"})
		return
	}
	rider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("rider")))

	// The rider name is the UI's guess and may be blank, so the identity
	// being deleted is ended by its own id as well: removing someone has to
	// log them out even when no local data is purged.
	s.endSessionsOf(r.Context(), id, "person deleted")

	if rider != "" {
		if _, err := s.purgeRiderData(r.Context(), rider); err != nil {
			s.logger().Warn("purging local data before deleting a person failed", "id", id, "rider", rider, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "could not remove this rider's local data — nothing was deleted",
			})
			return
		}
	}

	if err := s.People.DeleteUser(r.Context(), id); err != nil {
		s.logger().Warn("deleting a person failed", "id", id, "rider", rider, "err", err)
		s.failUpstream(w, "Auth0", err)
		return
	}

	// Last, and only now: with the account gone from Auth0 the binding points at
	// nothing. Done before the delete, a failed delete would leave an account
	// that can still sign in with no binding, which would give it a new rider.
	if s.Identities != nil {
		if _, err := s.Identities.Unbind(r.Context(), s.authenticator().OIDC().Issuer, id); err != nil {
			s.logger().Error("could not remove the binding of a deleted person", "rider", rider, "err", err)
		}
	}

	s.logger().Info("person deleted", "id", id, "rider", rider, "by", auth.FromContext(r.Context()).User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handlePeopleSetBinding points an account at a rider, or binds one that has
// no binding yet. This is how an admin settles what the startup bootstrap
// left alone (a conflict, an unverified address) and how a rider who ended up
// on a suffixed name gets back to their own data.
//
// Admin only, and never for the caller's own account: a binding decides whose
// data a person sees, and the one thing the whole design forbids is a person
// choosing that for themselves. An admin who needs their own moved uses the
// CLI on the server (bind-identity), which needs access to the database and so
// to the deployment rather than only to a login.
//
// The account's live sessions are ended, because they hold the old rider.
func (s *Server) handlePeopleSetBinding(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManagePeople) {
		return
	}
	if !s.peopleAvailable(w) {
		return
	}
	if s.Identities == nil {
		s.logger().Warn("binding requested but this deployment keeps no identity bindings")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment keeps no identity bindings (they exist under auth.mode: oidc)",
		})
		return
	}

	sub := r.PathValue("id")
	if sub == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no person id"})
		return
	}
	caller := auth.FromContext(r.Context())
	own := sub == caller.Sub

	var body struct {
		Rider string `json:"rider"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	rider := strings.ToLower(strings.TrimSpace(body.Rider))
	if rider == "" || !accounts.RiderPattern.MatchString(rider) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "rider must be letters, digits and . _ @ | -",
		})
		return
	}

	// Nobody moves their own sign-in onto a rider that someone is bound to: that
	// is the whole attack. The one exception is the way out of a lockout: an
	// admin whose own account landed on a suffixed rider (the bootstrap could not
	// tell them from a renamer) may take a rider that has no binding at all, so
	// the owner is not stuck needing a second admin that does not exist. Always
	// logged at Warn.
	if own {
		taken, err := s.Identities.RiderBound(r.Context(), rider)
		if err != nil {
			s.fail(w, err)
			return
		}
		if taken {
			s.logger().Warn("an admin tried to move their own identity binding onto a bound rider", "rider", caller.User, "target", rider)
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "you cannot move your own sign-in onto a rider someone is already bound to — ask another admin, or run `domestique bind-identity` on the server",
			})
			return
		}
	}

	issuer := s.authenticator().OIDC().Issuer
	from := ""
	existing, bound, err := s.Identities.Get(r.Context(), issuer, sub)
	if err != nil {
		s.fail(w, err)
		return
	}
	if bound {
		from = existing.Rider
		err = s.Identities.Move(r.Context(), issuer, sub, rider)
	} else {
		_, err = s.Identities.Bind(r.Context(), issuer, sub, rider, "")
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.endSessionsOf(r.Context(), sub, "identity binding changed")

	if own {
		s.logger().Warn("admin moved their own identity binding", "rider", rider, "from", from)
	} else {
		s.logger().Info("identity binding moved by admin", "rider", rider, "from", from, "by", caller.User)
	}
	writeJSON(w, http.StatusOK, map[string]string{"boundRider": rider})
}

// handlePeopleRiderInfo tells an admin, before they bind an account to a rider,
// whether that rider has data and how many accounts are already bound to it:
// binding to a rider with either is how one person ends up with another's
// routes, so the page asks for a confirmation.
func (s *Server) handlePeopleRiderInfo(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManagePeople) {
		return
	}
	if s.Identities == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment keeps no identity bindings (they exist under auth.mode: oidc)",
		})
		return
	}
	rider := strings.ToLower(strings.TrimSpace(r.PathValue("rider")))
	if rider == "" || !accounts.RiderPattern.MatchString(rider) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that is not a rider name"})
		return
	}
	all, err := s.Identities.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	bindings := 0
	for _, b := range all {
		if b.Rider == rider {
			bindings++
		}
	}
	data, err := s.Identities.RidersWithData(r.Context(), riderDataColumns())
	if err != nil {
		s.fail(w, err)
		return
	}
	_, hasData := data[rider]
	writeJSON(w, http.StatusOK, map[string]any{"rider": rider, "hasData": hasData, "bindings": bindings})
}

// handlePeopleOrphans lists riders that have data here but no account in the
// gate role to sign in as them and no binding: data that nobody can reach until
// an admin binds an account to it or removes it.
func (s *Server) handlePeopleOrphans(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManagePeople) {
		return
	}
	if !s.peopleAvailable(w) {
		return
	}
	if s.Identities == nil {
		writeJSON(w, http.StatusOK, map[string]any{"riders": []string{}})
		return
	}
	gate := s.Auth.RequiredGroup()
	if gate == "" {
		writeJSON(w, http.StatusOK, map[string]any{"riders": []string{}})
		return
	}
	adminRole, riderRole := s.permissionRoleNames()
	var roles []string
	if adminRole != "" {
		roles = append(roles, adminRole)
	}
	if riderRole != "" {
		roles = append(roles, riderRole)
	}
	people, err := s.People.ListPeople(r.Context(), gate, roles...)
	if err != nil {
		s.logger().Warn("listing people for the orphan riders failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Auth0 would not list who has access just now."})
		return
	}
	all, err := s.Identities.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	bound := map[string]bool{}
	for _, b := range all {
		bound[b.Rider] = true
	}
	data, err := s.Identities.RidersWithData(r.Context(), riderDataColumns())
	if err != nil {
		s.fail(w, err)
		return
	}
	plan := planBootstrap(people, func(rider string) bool { return bound[rider] }, data)
	orphans := plan.Orphans
	if orphans == nil {
		orphans = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"riders": orphans})
}
