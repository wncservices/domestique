// Package auth identifies the rider making a request, in one of three modes.
//
// Everything downstream — Identity, role resolution, Authorize — is the same
// regardless of which mode supplied the identity. Only Identify's source
// differs per mode; see its own doc comment and each Mode's.
//
//   - ModeNone: nobody is authenticated, everyone is the local admin. Right
//     for a laptop.
//   - ModeProxy: trusts a reverse proxy's headers — Traefik with an Authelia
//     forwardAuth middleware, typically. See ModeProxy's own doc comment for
//     the trust model this rests on and what has to stay true for it to hold.
//   - ModeOIDC: the app authenticates riders itself, against any OIDC issuer.
//     See ModeOIDC's own doc comment, and docs/oidc.md for the full design.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Mode selects how a request is identified.
type Mode string

const (
	// ModeNone runs without authentication. Every request is treated as the
	// local admin (see LocalIdentity) and nothing is gated — running without a
	// proxy means running on your own machine. Right for a laptop, wrong for
	// anything reachable.
	ModeNone Mode = "none"
	// ModeProxy trusts Authelia's forwardAuth headers: Remote-User,
	// Remote-Name, Remote-Email, Remote-Groups.
	//
	// The whole scheme rests on one assumption: those headers came from the
	// proxy and not from the client. A browser can set "Remote-User:
	// someone-else" just as easily as the proxy can. So header trust is
	// **opt-in** — Mode must be set to "proxy" explicitly — and the
	// deployment must guarantee the app is not reachable except through the
	// proxy. Running ModeProxy on a directly reachable port is the same as
	// having no authentication at all. trusted_proxies narrows who the
	// headers are trusted from, but does not replace this requirement — it
	// only helps when the app is reachable from more places than the proxy
	// alone, which should not be true in the first place.
	//
	// This constraint is specific to ModeProxy. ModeOIDC does not share it —
	// see its own comment.
	ModeProxy Mode = "proxy"
	// ModeOIDC authenticates the rider itself, against any OIDC issuer —
	// Auth0, Keycloak, Zitadel, whatever the operator points it at. Identity
	// comes from a server-side session the app created at login (see
	// internal/sessions), not from a header a proxy is trusted to set.
	//
	// ModeProxy's "must be unreachable except through the proxy" constraint
	// does not apply here: the app verifies a signed token itself rather than
	// trusting a header, so ModeOIDC is the mode for a deployment that faces
	// the public directly. What has to stay true instead: the client secret
	// only ever comes from oidcflow.EnvClientSecret, and an encryption key
	// (secrets.EnvKey) must be set — without one there is nowhere safe to
	// hold the session or the short-lived sign-in state, and /sso/login
	// refuses outright rather than run degraded.
	ModeOIDC Mode = "oidc"
)

// SessionCookieName holds the opaque token a ModeOIDC login is looked up by.
// OIDCStateCookie holds the sealed PKCE verifier, nonce and CSRF state
// between /sso/login and /sso/callback — short-lived, cleared once the
// callback consumes it.
const (
	SessionCookieName = "domestique_session"
	OIDCStateCookie   = "domestique_oidc_state"
)

// Header names Authelia sets and Traefik copies onto the upstream request.
const (
	HeaderUser   = "Remote-User"
	HeaderName   = "Remote-Name"
	HeaderEmail  = "Remote-Email"
	HeaderGroups = "Remote-Groups"
)

// Config is the auth section of domestique.yaml.
type Config struct {
	Mode Mode `yaml:"mode"`
	// TrustedProxies are CIDRs the forwarded headers may arrive from. Empty
	// means "trust any peer", which is only safe when the app has no route
	// from anywhere but the proxy — for example a ClusterIP service.
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`
	// RequiredGroup, when set, gates access on Authelia group membership.
	RequiredGroup string `yaml:"required_group,omitempty"`
	// Roles maps Authelia group names onto roles.
	Roles RoleMapping `yaml:"roles,omitempty"`
	// DefaultRole applies to an authenticated user whose groups match nothing
	// in Roles. Empty means "viewer" — read-only is the safe default for
	// someone Authelia let in but this app has no opinion about.
	DefaultRole string `yaml:"default_role,omitempty"`
	// LogoutURL is where "Sign out" goes. The app holds no session of its
	// own — the proxy does — so it cannot end one; only the identity provider
	// can. That address is deployment-specific (Authelia's portal is at
	// /auth/logout here, somewhere else entirely for another operator), so it
	// is configuration rather than something to derive. Empty hides the
	// button, which is right for mode "none": there is nothing to sign out of.
	LogoutURL string `yaml:"logout_url,omitempty"`
	// OIDC configures ModeOIDC. Ignored, and left unvalidated, in every other
	// mode — an operator experimenting with the block before flipping mode
	// over should not have it rejected for a typo in a section that is not
	// active yet.
	OIDC OIDCConfig `yaml:"oidc,omitempty"`
}

// OIDCConfig is the auth.oidc section of domestique.yaml.
//
// The client secret is deliberately not a field here: this struct is
// unmarshaled straight from a config file meant to be readable, and a secret
// belongs in the environment — DOMESTIQUE_OIDC_CLIENT_SECRET, the same rule
// KOMOOT_EMAIL/PASSWORD and the encryption key already follow.
type OIDCConfig struct {
	// Issuer is the base URL discovery is run against —
	// "<issuer>/.well-known/openid-configuration" must resolve.
	Issuer string `yaml:"issuer"`
	// ClientID identifies this app to the issuer. Not a secret.
	ClientID string `yaml:"client_id"`
	// RedirectURL is where the issuer sends the browser back after login —
	// must equal what is registered with the issuer, exactly, including
	// scheme and path.
	RedirectURL string `yaml:"redirect_url"`
	// PreviewRedirectURL is a second, optional registered redirect_uri —
	// for a blue-green preview host (a Rollout's own previewService,
	// reachable before promotion) that never receives real traffic but
	// still needs a real login to be checkable by hand, not just by the
	// automated postPromotionAnalysis check that runs after. Must also be
	// registered with the issuer, exactly, same as RedirectURL. Empty
	// means there is no second host — every login uses RedirectURL, the
	// only behavior that existed before this field did.
	//
	// Which of the two applies to a given request is chosen by comparing
	// that request's own Host against PreviewRedirectURL's host (see
	// sso.go's redirectURLForRequest) — never derived from an arbitrary
	// Host. The two are both our own already-registered destinations
	// either way, so there is nothing here for a spoofed Host header to
	// redirect a login to that was not already a legitimate landing spot
	// for one.
	PreviewRedirectURL string `yaml:"preview_redirect_url,omitempty"`
	// Scopes requested at login. "openid" is required by the spec and is
	// added automatically if the operator forgot it, rather than failing
	// startup for an easy mistake with an unhelpful downstream error.
	Scopes []string `yaml:"scopes,omitempty"`
	// GroupsClaim is the ID-token claim role mapping reads groups from.
	// Issuers disagree on this — Authelia sends "groups", Auth0 needs a
	// custom claim added by an Action and namespaced, Google sends none at
	// all — so it is configurable. Empty means "groups". An issuer with no
	// groups claim at all is not a misconfiguration: every rider falls
	// through to default_role, which is the point of that setting existing.
	GroupsClaim string `yaml:"groups_claim,omitempty"`
}

// Identity is who is making a request, and what they may do.
type Identity struct {
	User  string `json:"user"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	// EmailVerified is whether the identity provider vouches for Email: the ID
	// token's email_verified claim under ModeOIDC, and under ModeProxy simply
	// whether Remote-Email is present, because it comes from Authelia's own
	// user directory, which an administrator controls. Mail is only ever sent
	// to an address for which this is true. Never serialised: nothing in the UI
	// needs it, and the wire shape of /api/me stays as it was.
	EmailVerified bool     `json:"-"`
	Groups        []string `json:"groups,omitempty"`
	Role          Role     `json:"role"`
	// Sub is the OIDC subject claim, verbatim — for Auth0 this is the
	// issuer's own user id ("auth0|64f2a1b2c3d4e5f6"), the only thing that
	// unambiguously names one account when a rider might hold two identities
	// for the same email (a Google sign-in and a database one are never
	// linked on this tenant — see auth0mgmt's own FindByEmail doc comment).
	// Only ever set under ModeOIDC; empty in every other mode, since there is
	// no Management API account to point it at.
	Sub string `json:"-"`
}

// Anonymous reports whether nobody is identified.
func (i Identity) Anonymous() bool { return i.User == "" }

// Provider is the connection type embedded in Sub's prefix — "auth0" for a
// database (email+password) sign-in, "google-oauth2" for Google, and so on
// for whatever other connections a tenant enables. Empty when Sub itself is
// empty. Used to decide whether "change password" makes sense at all: a
// Google-only identity has no password on this app's side to change.
func (i Identity) Provider() string {
	if idx := strings.Index(i.Sub, "|"); idx > 0 {
		return i.Sub[:idx]
	}
	return ""
}

// DisplayName is the friendliest label available.
func (i Identity) DisplayName() string {
	if i.Name != "" {
		return i.Name
	}
	return i.User
}

// InGroup reports group membership.
func (i Identity) InGroup(group string) bool {
	for _, g := range i.Groups {
		if strings.EqualFold(g, group) {
			return true
		}
	}
	return false
}

// SessionLookup resolves a ModeOIDC session cookie to who it belongs to.
//
// An interface rather than a concrete type from internal/sessions: that
// package needs Identity, and this package must not import it back —
// *sessions.Store satisfies this structurally, wired in with UseSessions
// after construction rather than through New's parameter list, so no
// existing caller of New(cfg) changes.
type SessionLookup interface {
	Lookup(token string) (Identity, bool)
}

// Authenticator turns requests into identities.
type Authenticator struct {
	mode          Mode
	requiredGroup string
	trusted       []*net.IPNet
	roles         RoleMapping
	defaultRole   Role
	logoutURL     string
	sessions      SessionLookup
	oidc          OIDCConfig
}

// UseSessions wires the session store ModeOIDC reads from. Nil is a valid
// state — Identify treats it the same as a session nobody can find — because
// a server built before its sessions store exists (or without one at all, in
// a mode that never needs it) must not panic on the first request.
func (a *Authenticator) UseSessions(s SessionLookup) { a.sessions = s }

// OIDC returns the validated, defaulted OIDC config — "openid" present in
// Scopes, GroupsClaim never empty — for whatever builds the discovery client
// in ModeOIDC. Zero value in every other mode.
func (a *Authenticator) OIDC() OIDCConfig { return a.oidc }

// Roles is the configured group-name-to-role mapping — the admin People
// page's own source for which Auth0 role names "admin" and "rider" actually
// are, so it never hardcodes domestique-admins/cyclists itself and drifts
// from what roles.go actually resolves at sign-in.
func (a *Authenticator) Roles() RoleMapping { return a.roles }

// RequiredGroup is the gate role name — "allowed in at all," not a
// permission level. Empty means every authenticated identity is let in
// (see Authorize), which the People page needs to know before it tries to
// grant a "gate" role that was never configured.
func (a *Authenticator) RequiredGroup() string { return a.requiredGroup }

// validatedOIDC checks the shape of an OIDC config and fills in its
// defaults. Pure — no network call, no I/O. Discovery (an issuer actually
// being reachable) is a separate, later step; config.Validate runs this for
// every CLI subcommand, not only serve, and must never touch the network to
// do it.
func validatedOIDC(cfg OIDCConfig) (OIDCConfig, error) {
	if cfg.Issuer == "" {
		return OIDCConfig{}, errors.New("auth.oidc.issuer is required for mode oidc")
	}
	u, err := url.Parse(cfg.Issuer)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return OIDCConfig{}, fmt.Errorf("auth.oidc.issuer: %q is not a URL with a scheme and host", cfg.Issuer)
	}
	if cfg.ClientID == "" {
		return OIDCConfig{}, errors.New("auth.oidc.client_id is required for mode oidc")
	}
	if cfg.RedirectURL == "" {
		return OIDCConfig{}, errors.New("auth.oidc.redirect_url is required for mode oidc")
	}
	if _, err := url.Parse(cfg.RedirectURL); err != nil {
		return OIDCConfig{}, fmt.Errorf("auth.oidc.redirect_url: %q is not a URL: %w", cfg.RedirectURL, err)
	}
	if cfg.PreviewRedirectURL != "" {
		u, err := url.Parse(cfg.PreviewRedirectURL)
		if err != nil || u.Host == "" {
			return OIDCConfig{}, fmt.Errorf("auth.oidc.preview_redirect_url: %q is not a URL with a host", cfg.PreviewRedirectURL)
		}
	}

	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if !slices.Contains(cfg.Scopes, "openid") {
		// Required by the spec. Forgetting it is an easy mistake to make and
		// a confusing one to debug from the far side — the issuer's error
		// looks nothing like "you forgot a scope".
		cfg.Scopes = append([]string{"openid"}, cfg.Scopes...)
	}
	return cfg, nil
}

// New validates the config and builds an Authenticator.
func New(cfg Config) (*Authenticator, error) {
	a := &Authenticator{
		mode:          cfg.Mode,
		requiredGroup: cfg.RequiredGroup,
		roles:         cfg.Roles,
		defaultRole:   RoleViewer,
		logoutURL:     cfg.LogoutURL,
	}
	if a.mode == "" {
		a.mode = ModeNone
	}

	if cfg.DefaultRole != "" {
		role, err := parseRole(cfg.DefaultRole)
		if err != nil {
			return nil, fmt.Errorf("auth.default_role: %w", err)
		}
		a.defaultRole = role
	}

	switch a.mode {
	case ModeNone:
		if cfg.RequiredGroup != "" {
			return nil, fmt.Errorf("auth.required_group is set but auth.mode is %q: "+
				"nothing would enforce it", a.mode)
		}
	case ModeProxy:
	case ModeOIDC:
		oidcCfg, err := validatedOIDC(cfg.OIDC)
		if err != nil {
			return nil, err
		}
		a.oidc = oidcCfg
	default:
		return nil, fmt.Errorf("unknown auth mode %q (want none, proxy or oidc)", a.mode)
	}

	for _, cidr := range cfg.TrustedProxies {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			// A bare IP is a reasonable thing to write; accept it as a /32.
			if ip := net.ParseIP(cidr); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				network = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
			} else {
				return nil, fmt.Errorf("auth.trusted_proxies: %q is not a CIDR or IP", cidr)
			}
		}
		a.trusted = append(a.trusted, network)
	}

	return a, nil
}

// Mode reports the configured mode.
func (a *Authenticator) Mode() Mode { return a.mode }

// Enabled reports whether requests are authenticated at all.
func (a *Authenticator) Enabled() bool { return a.mode != ModeNone }

// Identify extracts the identity from a request.
//
// A real per-mode branch, not one early return with a case tacked on: modes
// disagree about where an identity comes from (a trusted proxy's headers, a
// session this app issued itself), not just about whether to trust one more
// source layered on the same check.
func (a *Authenticator) Identify(r *http.Request) Identity {
	switch a.mode {
	case ModeNone:
		return LocalIdentity()
	case ModeProxy:
		return a.identifyFromProxy(r)
	case ModeOIDC:
		return a.identifyFromSession(r)
	default:
		return Identity{}
	}
}

// identifyFromProxy trusts Authelia's forwardAuth headers, and only when the
// peer is a trusted proxy — headers from anywhere else are discarded rather
// than trusted, because that is exactly what a spoofing attempt looks like.
func (a *Authenticator) identifyFromProxy(r *http.Request) Identity {
	if !a.peerTrusted(r) {
		return Identity{}
	}

	user := strings.TrimSpace(r.Header.Get(HeaderUser))
	if user == "" {
		return Identity{}
	}

	groups := splitGroups(r.Header.Get(HeaderGroups))
	email := strings.TrimSpace(r.Header.Get(HeaderEmail))
	return Identity{
		User:  user,
		Name:  strings.TrimSpace(r.Header.Get(HeaderName)),
		Email: email,
		// Authelia's directory is the administrator's, and the header is only
		// honoured from a trusted proxy: see EmailVerified.
		EmailVerified: email != "",
		Groups:        groups,
		Role:          a.resolveRole(groups),
	}
}

// identifyFromSession looks up the session cookie a successful /sso/callback
// set. Role is recomputed from the stored Groups on every call rather than
// cached at login, so editing roles: in config takes effect for an
// already-signed-in rider immediately — the same as ModeProxy, where the
// header is re-read and re-resolved on every request too.
func (a *Authenticator) identifyFromSession(r *http.Request) Identity {
	if a.sessions == nil {
		return Identity{}
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return Identity{}
	}
	id, ok := a.sessions.Lookup(cookie.Value)
	if !ok {
		return Identity{}
	}
	id.Role = a.resolveRole(id.Groups)
	return id
}

// LocalIdentity is who you are when authentication is off. Running without a
// proxy means running on your own machine, so it is an admin — anything less
// would make the app unusable in development for no security gain.
func LocalIdentity() Identity {
	return Identity{User: "local", Name: "Local user", Role: RoleAdmin}
}

// Authorize reports whether an identity may use the app, and why not if it
// may not.
func (a *Authenticator) Authorize(id Identity) error {
	if a.mode == ModeNone {
		return nil
	}
	if id.Anonymous() {
		return ErrUnauthenticated
	}
	// An explicit role grant is a way in, not something the gate overrides.
	//
	// required_group exists to stop every account the IdP knows about falling
	// through to default_role — it is about the unmapped, not about people
	// somebody deliberately named. Checking it first meant an account in
	// domestique-admins was given the admin role and then refused entry,
	// which is not a position this app can coherently hold: it had already
	// decided who they were.
	if a.requiredGroup != "" && !id.InGroup(a.requiredGroup) && !a.hasRoleGroup(id.Groups) {
		return fmt.Errorf("%w: not a member of %q and no group granting a role",
			ErrForbidden, a.requiredGroup)
	}
	return nil
}

// hasRoleGroup reports whether any of these groups is named in the role
// mapping — that is, whether this identity's role was granted rather than
// defaulted.
func (a *Authenticator) hasRoleGroup(groups []string) bool {
	for _, mapped := range [][]string{a.roles.Admin, a.roles.Rider, a.roles.Viewer} {
		for _, name := range mapped {
			for _, g := range groups {
				if strings.EqualFold(g, name) {
					return true
				}
			}
		}
	}
	return false
}

func (a *Authenticator) peerTrusted(r *http.Request) bool {
	if len(a.trusted) == 0 {
		return true
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, network := range a.trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func splitGroups(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// Errors returned by Authorize.
var (
	ErrUnauthenticated = authError("not authenticated")
	ErrForbidden       = authError("forbidden")
)

type authError string

func (e authError) Error() string { return string(e) }

// context plumbing

type contextKey struct{}

// WithIdentity stores an identity on a request context.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext reads the identity a middleware stored.
func FromContext(ctx context.Context) Identity {
	if id, ok := ctx.Value(contextKey{}).(Identity); ok {
		return id
	}
	return Identity{}
}

// LogoutURL is where the UI should send someone signing out, or empty when
// there is nothing to sign out of.
func (a *Authenticator) LogoutURL() string {
	if a.mode == ModeNone {
		return ""
	}
	return a.logoutURL
}
