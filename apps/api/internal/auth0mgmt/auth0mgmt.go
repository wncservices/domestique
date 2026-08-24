// Package auth0mgmt is a minimal client for the three things the admin
// People page needs from Auth0's Management API: list who has access,
// invite a new rider, and change which roles someone holds.
//
// Deliberately not a general-purpose Auth0 SDK. Auth0's Management API is
// real, versioned, and documented — unlike Garmin's or Komoot's, there is
// a spec here, which is why this is hand-rolled with net/http rather than
// reached for as an excuse to add a dependency: three endpoints and a
// client_credentials token exchange do not need one.
//
// The invite email itself is not sent from here. It goes out through the
// public, unauthenticated Authentication API endpoint
// /dbconnections/change_password (see SendInviteEmail) — Auth0's own
// "reset your password" flow, reused as an invite: the account already
// exists with no usable password, so completing that flow *is* accepting
// the invite. That call needs no Management API token and no scope, which
// is why SignInClientID (Domestique's own regular_web OIDC client) is
// separate from ClientID/ClientSecret (the narrowly-scoped M2M app) in
// Config below — they authenticate to two different APIs.
package auth0mgmt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	defaultTimeout = 20 * time.Second
	maxBody        = 4 << 20

	// tokenSafetyMargin is subtracted from a fetched token's own expiry, so
	// a token already borderline stale by the time a request actually goes
	// out is refreshed rather than sent and rejected.
	tokenSafetyMargin = 30 * time.Second
)

// Config points the client at one tenant. Domain carries no scheme — the
// client adds https:// itself, the same way it is stored in
// auth.OIDCConfig.Issuer stripped of its own scheme+slash by the caller.
type Config struct {
	Domain         string
	ClientID       string
	ClientSecret   string
	SignInClientID string
}

// Person is one rider (or admin, or someone gated out of everything but
// still holding the account) as the People page shows them.
type Person struct {
	UserID string
	Email  string
	Name   string
	// Nickname is Auth0's own separate profile field — auto-populated to an
	// email's local part or a Google given_name for social sign-ins, distinct
	// from Name. Exists on Person only to feed the same name/nickname/sub
	// priority identityFromToken uses on an OIDC token's claims, so the
	// People page can offer a best-effort guess at a not-yet-logged-in
	// person's eventual rider identity — see internal/api/people.go's
	// likelyRider.
	Nickname string
	Roles    []string
	// Blocked mirrors Auth0's own blocked flag on this identity — set by
	// SetBlocked, read back here so the People page can render a toggle
	// instead of firing the action blind. Populated from the Users Search
	// API the same way CreatedAt/LastLogin are (see lastSeen) since, like
	// those two, the role-members endpoint ListPeople otherwise uses does
	// not return it.
	Blocked   bool
	CreatedAt time.Time
	LastLogin time.Time
}

// Client talks to one Auth0 tenant's Management API.
type Client struct {
	cfg  Config
	http *http.Client

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
	roleIDs     map[string]string // role name -> role id, resolved once and kept
}

// New builds a client. It makes no network call itself — the first real
// request is what proves the credentials work, the same way garmin.New and
// komoot.New defer their own first call.
func New(cfg Config) *Client {
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout:   defaultTimeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
		roleIDs: map[string]string{},
	}
}

// baseURL is https://Domain, unless Domain already carries a scheme — which
// only ever happens in this package's own tests, pointed at a plain-http
// httptest.Server. A real tenant's domain never does.
func (c *Client) baseURL() string {
	if strings.HasPrefix(c.cfg.Domain, "http://") || strings.HasPrefix(c.cfg.Domain, "https://") {
		return strings.TrimSuffix(c.cfg.Domain, "/")
	}
	return "https://" + c.cfg.Domain
}

// accessToken returns a valid Management API bearer, fetching or refreshing
// one as needed. Guarded by c.mu so concurrent callers (the People page can
// legitimately fire off ListPeople for three roles at once) share one token
// exchange rather than each doing their own.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}

	body, err := json.Marshal(map[string]string{
		"client_id":     c.cfg.ClientID,
		"client_secret": c.cfg.ClientSecret,
		"audience":      c.baseURL() + "/api/v2/",
		"grant_type":    "client_credentials",
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	raw, status, err := c.doRaw(req)
	if err != nil {
		return "", err
	}
	if status >= 300 {
		return "", fmt.Errorf("auth0mgmt: token exchange returned %d: %s", status, snippet(raw))
	}

	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &token); err != nil || token.AccessToken == "" {
		return "", fmt.Errorf("auth0mgmt: unreadable token response: %s", snippet(raw))
	}

	c.token = token.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(token.ExpiresIn)*time.Second - tokenSafetyMargin)
	return c.token, nil
}

// do makes an authenticated Management API request and decodes a JSON
// response into out (nil to discard the body, for a 204 No Content call).
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	raw, status, err := c.doRaw(req)
	if err != nil {
		return err
	}
	if status >= 300 {
		return apiError(status, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("auth0mgmt: unreadable response from %s: %s", path, snippet(raw))
	}
	return nil
}

func (c *Client) doRaw(req *http.Request) ([]byte, int, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// apiError turns a Management API error response into something readable.
// The shape ({"statusCode", "error", "message", "errorCode"}) is documented
// and consistent across every endpoint here, unlike Garmin's, so this is
// worth a real parse rather than a snippet of raw JSON.
func apiError(status int, raw []byte) error {
	var body struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Message != "" {
		if body.ErrorCode != "" {
			return fmt.Errorf("auth0mgmt: %s (%s)", body.Message, body.ErrorCode)
		}
		return errors.New("auth0mgmt: " + body.Message)
	}
	return fmt.Errorf("auth0mgmt: request returned %d: %s", status, snippet(raw))
}

func snippet(raw []byte) string {
	const limit = 300
	text := strings.TrimSpace(string(raw))
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}

// roleID resolves a role's name to its id, the one thing every role-scoped
// call here needs and nothing else offers a shortcut for. Resolved once per
// name and kept — a role is renamed by a human, rarely enough that this
// process's lifetime is a perfectly good cache horizon.
func (c *Client) roleID(ctx context.Context, name string) (string, error) {
	c.mu.Lock()
	if id, ok := c.roleIDs[name]; ok {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	var roles []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet,
		"/api/v2/roles?name_filter="+url.QueryEscape(name), nil, &roles); err != nil {
		return "", err
	}
	for _, r := range roles {
		if r.Name == name {
			c.mu.Lock()
			c.roleIDs[name] = r.ID
			c.mu.Unlock()
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("auth0mgmt: no role named %q on this tenant", name)
}

// roleUser is the shape a user object comes back as from any of the three
// endpoints this file decodes one from. They do not all return the same
// fields: confirmed against Auth0's own published OpenAPI schema,
// GET /api/v2/roles/{id}/users returns only user_id/name/email/picture —
// no created_at or last_login at all, not even blank — while
// GET /api/v2/users-by-email and GET /api/v2/users (the search endpoint
// lastSeen below uses) return the full user object, both of those included.
// One struct still covers all three: unknown fields are ignored and missing
// ones simply zero-value, so decoding a role-members response into this
// just leaves CreatedAt/LastLogin blank, which lastSeen then fills in
// separately rather than trusting the role listing for them.
type roleUser struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Nickname  string `json:"nickname"`
	Blocked   bool   `json:"blocked"`
	CreatedAt string `json:"created_at"`
	LastLogin string `json:"last_login"`
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// lastSeen batches a created_at/last_login/nickname lookup for a set of
// user ids through the Users Search API, since the role-members endpoint
// that ListPeople otherwise uses does not return any of those three (see
// roleUser's own doc comment). One Lucene query ORing every id together —
// the documented way to search a specific set of ids — stays a single
// request regardless of how many people are on the tenant, the same N+1
// avoidance ListPeople's permission-role merge already relies on.
// search_engine=v3 is required for the q parameter to be honored at all;
// fields/include_fields keep the response to exactly what this needs.
func (c *Client) lastSeen(ctx context.Context, userIDs []string) (map[string]roleUser, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	terms := make([]string, len(userIDs))
	for i, id := range userIDs {
		terms[i] = fmt.Sprintf("user_id:%q", id)
	}
	q := url.Values{
		"search_engine":  {"v3"},
		"q":              {strings.Join(terms, " OR ")},
		"fields":         {"user_id,created_at,last_login,nickname,blocked"},
		"include_fields": {"true"},
	}

	var users []roleUser
	if err := c.do(ctx, http.MethodGet, "/api/v2/users?"+q.Encode(), nil, &users); err != nil {
		return nil, fmt.Errorf("looking up sign-in history: %w", err)
	}
	out := make(map[string]roleUser, len(users))
	for _, u := range users {
		out[u.UserID] = u
	}
	return out, nil
}

// ListPeople lists everyone in gateRole (the "allowed in at all" role —
// domestique-users, in this deployment), each annotated with which of the
// other named roles they also hold. adminRole/riderRole name the two
// permission-level roles this app actually offers a choice between; any
// other role a person holds on the tenant for unrelated reasons is not this
// page's business and is not reported.
func (c *Client) ListPeople(ctx context.Context, gateRole string, permissionRoles ...string) ([]Person, error) {
	gateID, err := c.roleID(ctx, gateRole)
	if err != nil {
		return nil, err
	}
	var gateMembers []roleUser
	if err := c.do(ctx, http.MethodGet, "/api/v2/roles/"+gateID+"/users", nil, &gateMembers); err != nil {
		return nil, fmt.Errorf("listing %s: %w", gateRole, err)
	}

	// memberOf[userID] accumulates every permission role a gate member also
	// holds — built from separate per-role membership lists rather than one
	// roles-lookup per person, so this stays a handful of requests
	// regardless of how many people are on the tenant.
	memberOf := map[string][]string{}
	for _, roleName := range permissionRoles {
		roleID, err := c.roleID(ctx, roleName)
		if err != nil {
			return nil, err
		}
		var members []roleUser
		if err := c.do(ctx, http.MethodGet, "/api/v2/roles/"+roleID+"/users", nil, &members); err != nil {
			return nil, fmt.Errorf("listing %s: %w", roleName, err)
		}
		for _, m := range members {
			memberOf[m.UserID] = append(memberOf[m.UserID], roleName)
		}
	}

	ids := make([]string, len(gateMembers))
	for i, m := range gateMembers {
		ids[i] = m.UserID
	}
	seen, err := c.lastSeen(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]Person, 0, len(gateMembers))
	for _, m := range gateMembers {
		s := seen[m.UserID]
		out = append(out, Person{
			UserID:    m.UserID,
			Email:     m.Email,
			Name:      m.Name,
			Nickname:  s.Nickname,
			Roles:     memberOf[m.UserID],
			Blocked:   s.Blocked,
			CreatedAt: parseTime(s.CreatedAt),
			LastLogin: parseTime(s.LastLogin),
		})
	}
	return out, nil
}

// FindByEmail looks up every Auth0 identity already registered with an
// exact email address. Plural, deliberately: a Google sign-in
// (google-oauth2|<id>) and a database account (auth0|<id>) are two entirely
// separate Auth0 users even for the same address, and this tenant does not
// link them (see google_connection.tf in the lab repo). The People page
// uses this to grant access to someone who already has an identity — most
// often: signed in with Google once, before anyone told this app about
// them — instead of creating, and inviting, a second one for the same
// person. Returned Persons carry no Roles; SetRoles is still the caller's
// job.
func (c *Client) FindByEmail(ctx context.Context, email string) ([]Person, error) {
	var users []roleUser
	if err := c.do(ctx, http.MethodGet, "/api/v2/users-by-email?email="+url.QueryEscape(email), nil, &users); err != nil {
		return nil, fmt.Errorf("looking up %s: %w", email, err)
	}
	out := make([]Person, 0, len(users))
	for _, u := range users {
		out = append(out, Person{
			UserID:    u.UserID,
			Email:     u.Email,
			Name:      u.Name,
			Nickname:  u.Nickname,
			Blocked:   u.Blocked,
			CreatedAt: parseTime(u.CreatedAt),
			LastLogin: parseTime(u.LastLogin),
		})
	}
	return out, nil
}

// randomPassword satisfies Auth0's create-user requirement for one, even
// though nobody will ever type it: SendInviteEmail is what actually gets
// this account its first real password, through Auth0's own reset flow.
// Same shape as sessions.newToken and oidcflow.randomString — each package
// here keeps its own copy of this rather than sharing one, on purpose; see
// either of theirs for why.
func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth0mgmt: generating a password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Invite creates a new Auth0 user and grants the named roles (the gate role
// plus whichever permission role an admin picked). It does not send the
// invite email itself — call SendInviteEmail with the result, kept as two
// steps because the second one is a different API with no scope of its own
// and a caller may reasonably want to retry it independently of the first.
func (c *Client) Invite(ctx context.Context, email, name string, roleNames []string) (Person, error) {
	password, err := randomPassword()
	if err != nil {
		return Person{}, err
	}

	var created struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
		Name   string `json:"name"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v2/users", map[string]any{
		"connection":     "Username-Password-Authentication",
		"email":          email,
		"name":           name,
		"password":       password,
		"email_verified": false,
	}, &created); err != nil {
		return Person{}, fmt.Errorf("creating the account: %w", err)
	}

	if err := c.SetRoles(ctx, created.UserID, roleNames); err != nil {
		return Person{}, fmt.Errorf("account created but granting access failed: %w", err)
	}

	return Person{UserID: created.UserID, Email: created.Email, Name: created.Name, Roles: roleNames}, nil
}

// UpdateName sets userID's display name — the self-service Settings page's
// "change name," not the admin People page's business, so it takes no roles
// and does no gate/permission-role bookkeeping the way Invite/SetRoles do.
func (c *Client) UpdateName(ctx context.Context, userID, name string) (Person, error) {
	var updated struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
		Name   string `json:"name"`
	}
	if err := c.do(ctx, http.MethodPatch, "/api/v2/users/"+url.PathEscape(userID),
		map[string]any{"name": name}, &updated); err != nil {
		return Person{}, fmt.Errorf("updating name: %w", err)
	}
	return Person{UserID: updated.UserID, Email: updated.Email, Name: updated.Name}, nil
}

// SetBlocked flips Auth0's own blocked flag on an existing identity. This
// only refuses sign-in for *this* identity — it does not stop a fresh
// signup with the same email from getting in, which is why the API package
// pairs every call to this with a write to its own local blocklist (see
// internal/blocklist) checked at the OIDC callback, not with this alone.
func (c *Client) SetBlocked(ctx context.Context, userID string, blocked bool) error {
	if err := c.do(ctx, http.MethodPatch, "/api/v2/users/"+url.PathEscape(userID),
		map[string]any{"blocked": blocked}, nil); err != nil {
		return fmt.Errorf("changing blocked status: %w", err)
	}
	return nil
}

// DeleteUser permanently removes an Auth0 identity — the admin People
// page's "delete this rider" and the self-service Settings page's "delete
// my account." Irreversible, and Auth0 keeps no local record afterward to
// retry against, which is why callers purge this app's own data for the
// rider *first* (see the API package's purgeRiderData): a failure here
// after that purge already succeeded leaves nothing stranded, whereas the
// reverse order would leave local data with no identity left to explain it.
func (c *Client) DeleteUser(ctx context.Context, userID string) error {
	if err := c.do(ctx, http.MethodDelete, "/api/v2/users/"+url.PathEscape(userID), nil, nil); err != nil {
		return fmt.Errorf("deleting the account: %w", err)
	}
	return nil
}

// SetRoles makes userID's role membership exactly want — granting whatever
// is missing, revoking whatever is present but not wanted. current is read
// fresh rather than trusted from a caller's stale copy of the page.
func (c *Client) SetRoles(ctx context.Context, userID string, want []string) error {
	var current []struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v2/users/"+url.PathEscape(userID)+"/roles", nil, &current); err != nil {
		return fmt.Errorf("reading current roles: %w", err)
	}
	currentIDs := make(map[string]bool, len(current))
	for _, r := range current {
		currentIDs[r.ID] = true
	}

	wantIDs := make(map[string]bool, len(want))
	for _, name := range want {
		id, err := c.roleID(ctx, name)
		if err != nil {
			return err
		}
		wantIDs[id] = true
	}

	var toGrant, toRevoke []string
	for id := range wantIDs {
		if !currentIDs[id] {
			toGrant = append(toGrant, id)
		}
	}
	for id := range currentIDs {
		if !wantIDs[id] {
			toRevoke = append(toRevoke, id)
		}
	}

	if len(toGrant) > 0 {
		if err := c.do(ctx, http.MethodPost, "/api/v2/users/"+url.PathEscape(userID)+"/roles",
			map[string]any{"roles": toGrant}, nil); err != nil {
			return fmt.Errorf("granting roles: %w", err)
		}
	}
	if len(toRevoke) > 0 {
		if err := c.doWithBody(ctx, http.MethodDelete, "/api/v2/users/"+url.PathEscape(userID)+"/roles",
			map[string]any{"roles": toRevoke}); err != nil {
			return fmt.Errorf("revoking roles: %w", err)
		}
	}
	return nil
}

// doWithBody is do, for the one call here (DELETE .../roles) that carries a
// body on a method net/http's client sends fine but c.do's signature does
// not otherwise need to distinguish from a bodyless GET/DELETE.
func (c *Client) doWithBody(ctx context.Context, method, path string, body any) error {
	return c.do(ctx, method, path, body, nil)
}

// SendInviteEmail triggers Auth0's own "reset your password" flow for
// email — the public, unauthenticated Authentication API endpoint every
// rider's own "forgot password" link already uses on the real login page,
// reused here as the invite: an account that has never had a usable
// password and one that has forgotten its password complete the identical
// flow. No Management API token, no scope — this call authenticates with
// nothing but SignInClientID, Domestique's own regular_web OIDC client.
func (c *Client) SendInviteEmail(ctx context.Context, email string) error {
	body, err := json.Marshal(map[string]string{
		"client_id":  c.cfg.SignInClientID,
		"email":      email,
		"connection": "Username-Password-Authentication",
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/dbconnections/change_password", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	raw, status, err := c.doRaw(req)
	if err != nil {
		return err
	}
	if status >= 300 {
		// Not JSON on this endpoint — a plain confirmation sentence on
		// success, and on failure whatever Auth0's own error page says.
		return fmt.Errorf("auth0mgmt: invite email request returned %d: %s", status, snippet(raw))
	}
	return nil
}

// Enrollment is one MFA factor tied to a rider's Auth0 account — an
// authenticator app, a phone number, a security key, whatever Guardian
// factor they finished enrolling. ID is what DeleteEnrollment takes, not the
// user id: Guardian's own delete endpoint is keyed by enrollment, not user,
// which is exactly why a caller must confirm ownership before calling it
// (see the API package's handleRemoveMFA).
type Enrollment struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "pending" or "confirmed"
	Type   string `json:"type"`   // "totp", "sms", "email", "push-notification", "webauthn-roaming", "webauthn-platform", "recovery-code"
	Name   string `json:"name"`   // e.g. a phone number's last digits — empty for most factor types
}

// ListEnrollments reports every MFA factor userID has finished or started
// enrolling.
func (c *Client) ListEnrollments(ctx context.Context, userID string) ([]Enrollment, error) {
	var enrollments []Enrollment
	if err := c.do(ctx, http.MethodGet, "/api/v2/users/"+url.PathEscape(userID)+"/enrollments", nil, &enrollments); err != nil {
		return nil, fmt.Errorf("listing MFA enrollments: %w", err)
	}
	return enrollments, nil
}

// CreateGuardianEnrollmentTicket asks Auth0 for a one-time link to its own
// hosted enrollment page — scanning a QR code, adding a security key,
// whatever factors this tenant's Guardian policy allows — so this app never
// has to build or maintain that UI itself. send_mail is always false: the
// rider is already looking at Settings when they ask for this, an email
// would just be a second hop to the same place.
func (c *Client) CreateGuardianEnrollmentTicket(ctx context.Context, userID string) (string, error) {
	var ticket struct {
		TicketID  string `json:"ticket_id"`
		TicketURL string `json:"ticket_url"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v2/guardian/enrollments/ticket",
		map[string]any{"user_id": userID, "send_mail": false}, &ticket); err != nil {
		return "", fmt.Errorf("creating an enrollment ticket: %w", err)
	}
	if ticket.TicketURL == "" {
		return "", errors.New("auth0mgmt: enrollment ticket response carried no URL")
	}
	return ticket.TicketURL, nil
}

// DeleteEnrollment removes one MFA factor. Keyed by the enrollment's own id,
// not by user — Auth0's endpoint enforces nothing about ownership, so a
// caller must confirm the enrollment actually belongs to the rider asking
// before calling this (see the API package's handleRemoveMFA).
func (c *Client) DeleteEnrollment(ctx context.Context, enrollmentID string) error {
	if err := c.do(ctx, http.MethodDelete, "/api/v2/guardian/enrollments/"+url.PathEscape(enrollmentID), nil, nil); err != nil {
		return fmt.Errorf("removing MFA enrollment: %w", err)
	}
	return nil
}
