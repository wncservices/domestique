package auth

import (
	"fmt"
	"strings"
)

// Role is what someone is allowed to do. Roles are ordered: every role can do
// everything the role below it can.
//
//	viewer  read routes, download GPX, see what would be pushed
//	rider   + upload, import from Komoot, edit and delete their own routes,
//	          link their own head units, push routes to them
//	admin   + edit and delete anyone's routes, and anyone's linked accounts
//
// Roles come from Authelia groups, mapped in domestique.yaml. Authelia is the
// source of truth for who is in which group; this only translates.
type Role string

const (
	RoleNone   Role = ""
	RoleViewer Role = "viewer"
	RoleRider  Role = "rider"
	RoleAdmin  Role = "admin"
)

var rank = map[Role]int{RoleNone: 0, RoleViewer: 1, RoleRider: 2, RoleAdmin: 3}

// AtLeast reports whether r is this role or a more privileged one.
func (r Role) AtLeast(other Role) bool { return rank[r] >= rank[other] }

// Valid reports whether r is a role we know.
func (r Role) Valid() bool {
	_, ok := rank[r]
	return ok && r != RoleNone
}

func parseRole(raw string) (Role, error) {
	role := Role(strings.ToLower(strings.TrimSpace(raw)))
	if !role.Valid() {
		return RoleNone, fmt.Errorf("unknown role %q (want viewer, rider or admin)", raw)
	}
	return role, nil
}

// RoleMapping maps Authelia group names onto roles.
type RoleMapping struct {
	Admin  []string `yaml:"admin,omitempty"`
	Rider  []string `yaml:"rider,omitempty"`
	Viewer []string `yaml:"viewer,omitempty"`
}

// Permission is a thing someone might try to do.
type Permission string

const (
	PermReadRoutes  Permission = "routes:read"
	PermUploadRoute Permission = "routes:upload"
	PermEditOwn     Permission = "routes:edit-own"
	PermEditAny     Permission = "routes:edit-any"
	PermPush        Permission = "sync:push"
	PermKomootSync  Permission = "komoot:import"
	// PermGarminSync is listing and importing what is already on a rider's
	// own Garmin account — sync-back, as distinct from PermPush, which sends
	// the library's routes the other way.
	PermGarminSync Permission = "garmin:sync"
	// PermWahooSync is the same sync-back relationship PermGarminSync has to
	// PermPush, for a rider's own Wahoo account.
	PermWahooSync Permission = "wahoo:sync"
	// PermManageAccounts is linking and unlinking head units. A rider manages
	// their own; touching somebody else's additionally needs PermEditAny.
	PermManageAccounts Permission = "accounts:manage"
	// PermManageSettings is changing deployment-wide configuration from the
	// UI — today, the Garmin OAuth1 consumer. Admin only: these settings
	// belong to the whole deployment rather than to the rider changing them,
	// and a bad value breaks the feature for everybody.
	PermManageSettings Permission = "settings:manage"
	// PermManagePeople is inviting a rider and changing who holds which
	// role — a separate permission from PermManageSettings, not folded into
	// it, because this one can grant another identity admin access to
	// everything else, which a bad deployment-wide config value cannot.
	PermManagePeople Permission = "people:manage"
	// PermManageCrews is creating a crew, requesting to join one, and — for
	// a crew's own owner — approving or removing members. Rider-level, not
	// admin: a crew is a relationship between riders that trusts each other
	// with routes, not deployment configuration. Ownership (who may decide
	// a specific crew's membership) is a separate, per-crew check —
	// Identity.CanEditRoute reused for it, same idiom as accounts.
	PermManageCrews Permission = "crews:manage"
	// PermManageTraining is setting a training goal, editing a rider's own
	// fitness profile, and building or editing a structured workout — see
	// internal/workout and docs/training-plan.md. Rider-level: this is a
	// rider's own training data, not deployment configuration. Ownership
	// (a goal or workout belongs to exactly the rider who created it, never
	// shared the way a route can be) is Identity.CanEditRoute reused again,
	// the same idiom PermManageCrews already uses for its own per-crew check.
	PermManageTraining Permission = "training:manage"
)

// minimumRole is the least privileged role that holds each permission.
var minimumRole = map[Permission]Role{
	PermReadRoutes:     RoleViewer,
	PermUploadRoute:    RoleRider,
	PermEditOwn:        RoleRider,
	PermPush:           RoleRider,
	PermKomootSync:     RoleRider,
	PermGarminSync:     RoleRider,
	PermWahooSync:      RoleRider,
	PermManageAccounts: RoleRider,
	PermManageCrews:    RoleRider,
	PermManageTraining: RoleRider,
	PermEditAny:        RoleAdmin,
	PermManageSettings: RoleAdmin,
	PermManagePeople:   RoleAdmin,
}

// Can reports whether a role holds a permission.
func (r Role) Can(p Permission) bool {
	required, known := minimumRole[p]
	if !known {
		// An unknown permission is a programming error. Deny rather than
		// silently allow, so a typo cannot open a hole.
		return false
	}
	return r.AtLeast(required)
}

// Permissions lists everything a role can do, for the UI to switch on.
func (r Role) Permissions() []Permission {
	var out []Permission
	for _, p := range []Permission{
		PermReadRoutes, PermUploadRoute, PermEditOwn, PermEditAny, PermPush,
		PermKomootSync, PermGarminSync, PermWahooSync, PermManageAccounts, PermManageCrews,
		PermManageTraining, PermManageSettings, PermManagePeople,
	} {
		if r.Can(p) {
			out = append(out, p)
		}
	}
	return out
}

// CanEditRoute reports whether this identity may change or delete something
// owned by `owner` — a route, a linked account, or a crew.
// Riders may touch what they uploaded (or created, or linked); admins may
// touch anything.
//
// An unowned route (uploaded before ownership was recorded, or imported by the
// CLI) is editable by any rider — refusing would strand it with nobody able to
// fix it.
func (i Identity) CanEditRoute(owner string) bool {
	if i.Role.Can(PermEditAny) {
		return true
	}
	if !i.Role.Can(PermEditOwn) {
		return false
	}
	if owner == "" {
		return true
	}
	return strings.EqualFold(owner, i.User)
}

// ResolveRole is resolveRole, exported so the admin People page can show
// what role a person's current Auth0 role membership actually resolves to
// — the same computation Identify runs at sign-in, not a second copy of
// "most privileged wins" that could drift from it.
func (a *Authenticator) ResolveRole(groups []string) Role { return a.resolveRole(groups) }

// resolveRole picks the role for a set of Authelia groups.
//
// The most privileged match wins: someone in both the admin and rider groups
// is an admin, which is what a reader of the config expects.
func (a *Authenticator) resolveRole(groups []string) Role {
	has := func(names []string) bool {
		for _, name := range names {
			for _, g := range groups {
				if strings.EqualFold(g, name) {
					return true
				}
			}
		}
		return false
	}

	switch {
	case has(a.roles.Admin):
		return RoleAdmin
	case has(a.roles.Rider):
		return RoleRider
	case has(a.roles.Viewer):
		return RoleViewer
	default:
		return a.defaultRole
	}
}
