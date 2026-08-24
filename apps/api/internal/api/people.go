package api

import (
	"context"
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

	out := make([]personDTO, 0, len(people))
	for _, p := range people {
		out = append(out, s.personDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePeopleInvite creates a new Auth0 account, grants it the requested
// role, and sends the invite email — three steps against two different
// APIs (see auth0mgmt's own package doc), surfaced here as one request
// since a caller has no reasonable use for succeeding at only part of it.
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
		s.logger().Warn("checking for an existing account failed", "email", body.Email, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
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
			s.logger().Warn("granting access to an existing account failed", "email", body.Email, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		s.logger().Info("access granted to existing account", "email", body.Email, "role", body.Role, "by", auth.FromContext(r.Context()).User)
		granted := existing[0]
		granted.Roles = roleNames
		writeJSON(w, http.StatusOK, map[string]any{"person": s.personDTO(granted), "granted": true})
		return
	}

	person, err := s.People.Invite(r.Context(), body.Email, body.Name, roleNames)
	if err != nil {
		s.logger().Warn("inviting a person failed", "email", body.Email, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	// The account and its access both exist at this point — a failure past
	// here means the invite email needs resending, not starting over.
	if err := s.People.SendInviteEmail(r.Context(), body.Email); err != nil {
		s.logger().Warn("sending the invite email failed", "email", body.Email, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"person": s.personDTO(person),
			"error":  "The account was created, but the invite email could not be sent: " + err.Error(),
		})
		return
	}

	s.logger().Info("person invited", "email", body.Email, "role", body.Role, "by", auth.FromContext(r.Context()).User)
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

	if err := s.People.SetRoles(r.Context(), id, roleNames); err != nil {
		s.logger().Warn("changing a person's role failed", "id", id, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

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

	if err := s.People.SetBlocked(r.Context(), id, body.Blocked); err != nil {
		s.logger().Warn("changing a person's blocked status failed", "id", id, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
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
			s.logger().Warn("updating the local blocklist failed", "email", body.Email, "err", err)
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "updated",
				"error":  "Auth0 was updated, but the local blocklist could not be: " + err.Error(),
			})
			return
		}
	}

	s.logger().Info("person blocked status changed", "id", id, "email", body.Email, "blocked", body.Blocked, "by", auth.FromContext(r.Context()).User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
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

	if rider != "" {
		if _, err := s.purgeRiderData(r.Context(), rider); err != nil {
			s.logger().Warn("purging local data before deleting a person failed", "id", id, "rider", rider, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "could not remove this rider's local data — nothing was deleted: " + err.Error(),
			})
			return
		}
	}

	if err := s.People.DeleteUser(r.Context(), id); err != nil {
		s.logger().Warn("deleting a person failed", "id", id, "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("person deleted", "id", id, "rider", rider, "by", auth.FromContext(r.Context()).User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
