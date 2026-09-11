// Package wahoo drives the OAuth2 authorization-code exchange against
// Wahoo's Cloud API (https://cloud-api.wahooligan.com/).
//
// Unlike Garmin and Komoot, which speak an undocumented sign-in flow this
// app has to reverse-engineer, Wahoo's API is a documented, ordinary OAuth2
// confidential-client flow: authorize, exchange a code for a token pair,
// refresh when it expires. No PKCE — this app is registered confidential
// (Wahoo's own registration reserves PKCE for apps that cannot hold a
// client secret), and the secret sent in Exchange/Refresh is what protects
// the code exchange instead. No dependency beyond the standard library: a
// plain form-encoded POST is small enough that pulling in
// golang.org/x/oauth2 for it would cost more than it saves, the same
// reasoning internal/oidcflow's own doc comment gives for hand-rolling its
// token exchange — and this package needs less than that one does, since
// there is no discovery document, no JWKS, and no ID token to verify.
package wahoo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	apiBase = "https://api.wahooligan.com"

	// wahooCDNHost is where GET /v1/routes' file.url actually points —
	// confirmed against the Cloud API docs' own example
	// (https://cdn.wahooligan.com/wahoo-cloud/.../testfile.fit). See
	// allowedFileHost for why this is a fixed allowlist entry rather than
	// "whatever host the response happens to name."
	wahooCDNHost = "cdn.wahooligan.com"

	// scopes is fixed, not operator-configurable: exactly what this app
	// functionally needs and what was registered with Wahoo. user_read is
	// mandatory regardless — Wahoo 403s any token that lacks it.
	//
	// workouts_read is the metrics-ingestion scope ListWorkouts needs —
	// confirmed as a real, separate scope from routes_read/routes_write
	// (see docs/training-plan.md's own account of the research behind
	// this), distinct from the further-gated "Plans" entitlement a
	// planned-workout *push* would need and which this app does not have.
	// Requesting it here is necessary but not sufficient: Wahoo's own
	// per-app registration has to list it too, or every token this app
	// requests keeps coming back without it regardless of what this
	// constant asks for.
	scopes = "user_read routes_read routes_write workouts_read"

	defaultTimeout = 30 * time.Second
)

// Config is this deployment's Wahoo app registration.
type Config struct {
	ClientID     string
	ClientSecret string
	// RedirectURL must equal, exactly, what is registered with Wahoo.
	RedirectURL string
}

// Session is what an authorization produced, on its way to being stored —
// the same role garmin.Session plays for Garmin's OAuth1 pair, just OAuth2.
type Session struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope"`
}

// Expired reports whether the access token needs a refresh before use.
func (s Session) Expired() bool { return time.Now().After(s.ExpiresAt) }

// Profile is the authenticated rider's own Wahoo account — enough to show
// whose connection this is.
type Profile struct {
	ID    int    `json:"id"`
	Email string `json:"email"`
	First string `json:"first"`
	Last  string `json:"last"`
}

// DisplayName is what the UI shows for a connection. Wahoo has no single
// display-name field, so this is First and Last joined, falling back to the
// email when a rider left their name blank.
func (p Profile) DisplayName() string {
	name := strings.TrimSpace(strings.TrimSpace(p.First) + " " + strings.TrimSpace(p.Last))
	if name == "" {
		return p.Email
	}
	return name
}

// Client drives one deployment's authorization-code flow.
type Client struct {
	HTTP *http.Client
	cfg  Config

	// APIBase, overridable so tests do not touch Wahoo.
	APIBase string
}

// New builds a Client. cfg's fields must all be non-empty — the caller (this
// app's own startup wiring) already knows whether Wahoo is configured at
// all and leaves Client nil rather than building one that would fail on
// first use.
func New(cfg Config) *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout:   defaultTimeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
		cfg:     cfg,
		APIBase: apiBase,
	}
}

// AuthCodeURL builds the redirect to Wahoo's authorization endpoint.
func (c *Client) AuthCodeURL(state string) string {
	v := url.Values{
		"client_id":     {c.cfg.ClientID},
		"redirect_uri":  {c.cfg.RedirectURL},
		"response_type": {"code"},
		"scope":         {scopes},
		"state":         {state},
	}
	return c.APIBase + "/oauth/authorize?" + v.Encode()
}

// Exchange trades an authorization code for a session.
func (c *Client) Exchange(ctx context.Context, code string) (Session, error) {
	return c.tokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.cfg.RedirectURL},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	})
}

// Refresh trades a refresh token for a new session, once a stored one has
// expired. Wahoo may or may not return a new refresh_token in the
// response; when it doesn't, the caller keeps using the one it already
// has — the grant only replaces what it actually returned.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	session, err := c.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	})
	if err != nil {
		return Session{}, err
	}
	if session.RefreshToken == "" {
		session.RefreshToken = refreshToken
	}
	return session, nil
}

// tokenResponse is the token endpoint's JSON shape.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (c *Client) tokenRequest(ctx context.Context, form url.Values) (Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.APIBase+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return Session{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("wahoo: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Session{}, fmt.Errorf("wahoo: reading token response: %w", err)
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return Session{}, fmt.Errorf("wahoo: unreadable token response (status %d): %s",
			resp.StatusCode, snippet(body))
	}
	if tok.Error != "" {
		return Session{}, fmt.Errorf("wahoo: %s: %s", tok.Error, tok.ErrorDescription)
	}
	if resp.StatusCode != http.StatusOK {
		return Session{}, fmt.Errorf("wahoo: token endpoint returned %d: %s", resp.StatusCode, snippet(body))
	}
	if tok.AccessToken == "" {
		return Session{}, errors.New("wahoo: token response carried no access_token")
	}

	return Session{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
		Scope:        tok.Scope,
	}, nil
}

// Me fetches the authenticated rider's own profile — called once at connect
// time so the stored connection has an email and display name to show, the
// same information Garmin's login response already carries inline and
// Wahoo's token response does not.
func (c *Client) Me(ctx context.Context, accessToken string) (Profile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.APIBase+"/v1/user", nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Profile{}, fmt.Errorf("wahoo: profile request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Profile{}, fmt.Errorf("wahoo: reading profile response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Profile{}, fmt.Errorf("wahoo: profile endpoint returned %d: %s", resp.StatusCode, snippet(body))
	}

	var p Profile
	if err := json.Unmarshal(body, &p); err != nil {
		return Profile{}, fmt.Errorf("wahoo: unreadable profile response: %s", snippet(body))
	}
	return p, nil
}

// Workout Type Family ids, per the Cloud API docs — the two this app can
// actually produce a route for. The others the API defines (swimming, snow
// sports, gym, ...) have no route type in this library to map from.
const (
	workoutTypeFamilyBiking  = 0
	workoutTypeFamilyRunning = 1
)

// workoutTypeFamilyFor maps a route's plain-string Sport (model.Sport's
// value, passed as a string rather than the type itself so this package —
// a leaf, no dependency on internal/model — does not need to import it for
// one enum) to the Cloud API's own classification. Unknown or empty maps to
// biking, the same default model.RouteMeta.EffectiveSport already applies:
// this library was cycling-only before Sport existed, so nothing here
// should ever produce anything else for a route that predates the field.
func workoutTypeFamilyFor(sport string) int {
	if sport == "running" {
		return workoutTypeFamilyRunning
	}
	return workoutTypeFamilyBiking
}

// RouteRequest is what CreateRoute/UpdateRoute send to Wahoo's Cloud API.
type RouteRequest struct {
	ExternalID  string
	Name        string
	Description string
	// UpdatedAt is route[provider_updated_at] — when this route last
	// changed on our side, since we are the "external" system from Wahoo's
	// point of view.
	UpdatedAt time.Time
	DistanceM float64
	AscentM   float64
	StartLat  float64
	StartLng  float64
	Filename  string
	// FIT is the raw course file. Wahoo will not take a GPX — see
	// internal/targets/wahoo.go's doc comment for why.
	FIT []byte
	// Sport is model.Sport's plain-string value ("cycling", "running") —
	// see workoutTypeFamilyFor, which decides route[workout_type_family_id]
	// from it. Empty behaves exactly like "cycling".
	Sport string
}

// routeResponse is the Cloud API's shape for a route object — this package
// reads only the id, the one field CreateRoute/UpdateRoute have to return.
type routeResponse struct {
	ID int64 `json:"id"`
}

// CreateRoute pushes a new route and returns Wahoo's id for it.
func (c *Client) CreateRoute(ctx context.Context, accessToken string, route RouteRequest) (string, error) {
	return c.routeRequest(ctx, http.MethodPost, c.APIBase+"/v1/routes", accessToken, route)
}

// UpdateRoute replaces an existing route's file and metadata in place.
//
// Unlike Garmin's course service, Wahoo's routes are a real REST resource —
// a PUT here is what it says it is, not an import-then-delete dance.
func (c *Client) UpdateRoute(ctx context.Context, accessToken, id string, route RouteRequest) (string, error) {
	return c.routeRequest(ctx, http.MethodPut, c.APIBase+"/v1/routes/"+id, accessToken, route)
}

// DeleteRoute removes a route from the account.
func (c *Client) DeleteRoute(ctx context.Context, accessToken, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.APIBase+"/v1/routes/"+id, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("wahoo: delete route: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("wahoo: reading delete response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("wahoo: delete route returned %d: %s", resp.StatusCode, snippet(body))
	}
	return nil
}

// Route is one route already on the rider's Wahoo account — GET /v1/routes'
// shape, trimmed to what this app needs. Deleted routes never reach here:
// ListRoutes filters route[deleted]==true out itself, the same reasoning
// Komoot's Tours filters out recorded rides by default — a caller that
// wants "what's actually here to sync back" should not have to re-derive
// that filter itself.
type Route struct {
	ID          string
	ExternalID  string
	Name        string
	Description string
	DistanceM   float64
	AscentM     float64
	StartLat    float64
	StartLng    float64
	// FileURL is where the FIT course lives — a CDN link, not this client's
	// own API host, so DownloadRoute deliberately does not attach the
	// bearer token used to fetch this list. See DownloadRoute's own doc
	// comment.
	FileURL   string
	UpdatedAt time.Time
	CreatedAt time.Time
}

// routeListItem is GET /v1/routes' JSON shape, verified against the Cloud
// API docs (cloud-api.wahooligan.com): id, user_id, name, description,
// file.url, workout_type_family_id, external_id, provider_updated_at,
// deleted, start_lat, start_lng, distance, ascent, descent, updated_at,
// created_at. Only the fields this app uses are decoded.
type routeListItem struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	File        struct {
		URL string `json:"url"`
	} `json:"file"`
	ExternalID string  `json:"external_id"`
	Deleted    bool    `json:"deleted"`
	StartLat   float64 `json:"start_lat"`
	StartLng   float64 `json:"start_lng"`
	Distance   float64 `json:"distance"`
	Ascent     float64 `json:"ascent"`
	UpdatedAt  string  `json:"updated_at"`
	CreatedAt  string  `json:"created_at"`
}

// ListRoutes lists what is already on the rider's Wahoo account — the sync-
// back direction, as distinct from CreateRoute/UpdateRoute/DeleteRoute
// which push the other way.
func (c *Client) ListRoutes(ctx context.Context, accessToken string) ([]Route, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.APIBase+"/v1/routes", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wahoo: list routes: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("wahoo: reading route list: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wahoo: list routes returned %d: %s", resp.StatusCode, snippet(body))
	}

	var items []routeListItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("wahoo: unreadable route list: %s", snippet(body))
	}

	routes := make([]Route, 0, len(items))
	for _, it := range items {
		if it.Deleted {
			continue
		}
		updated, _ := time.Parse(time.RFC3339, it.UpdatedAt)
		created, _ := time.Parse(time.RFC3339, it.CreatedAt)
		routes = append(routes, Route{
			ID:          strconv.FormatInt(it.ID, 10),
			ExternalID:  it.ExternalID,
			Name:        it.Name,
			Description: it.Description,
			DistanceM:   it.Distance,
			AscentM:     it.Ascent,
			StartLat:    it.StartLat,
			StartLng:    it.StartLng,
			FileURL:     it.File.URL,
			UpdatedAt:   updated,
			CreatedAt:   created,
		})
	}
	return routes, nil
}

// DownloadRoute fetches the FIT course for one route, from the CDN url
// ListRoutes returned in Route.FileURL.
//
// Deliberately does not attach an Authorization header unless fileURL is on
// this client's own API host. The CDN Wahoo hands the file back from lives
// on a different host than api.wahooligan.com (cdn.wahooligan.com in the
// Cloud API docs' own example), and sending this rider's bearer token to
// whatever host happens to show up in a response field is exactly the
// SSRF-with-credential-disclosure shape internal/komoot's allowedHost check
// exists to close off — see that package's doc comment for the fuller
// version of this reasoning, which applies here even though nothing here
// follows a paginated chain of links the way Komoot's client does. A plain
// GET is what a CDN link is for; if the file host ever does need auth, that
// will surface as a clear 401/403 rather than a silent credential leak.
func (c *Client) DownloadRoute(ctx context.Context, accessToken, fileURL string) ([]byte, error) {
	onAPIHost, err := c.allowedFileHost(fileURL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("wahoo: building route download request: %w", err)
	}
	if onAPIHost {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wahoo: download route: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("wahoo: reading route download: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wahoo: route download returned %d: %s", resp.StatusCode, snippet(body))
	}
	return body, nil
}

// allowedFileHost reports whether fileURL may be requested at all, and
// whether it lands on this client's own API host (as opposed to the CDN).
//
// This matters more than DownloadRoute's own doc comment first suggested:
// deciding whether to attach the bearer token is not the only thing at
// stake here. GET /v1/routes hands back file.url straight from Wahoo's
// response body (wahoo.go's routeListItem), and a rider imports by picking
// ids from that list (wahooroutes.go's handleWahooRouteImport) — a
// compromised or malicious upstream response could point fileURL at an
// internal address (a cloud metadata endpoint, an in-cluster service) and
// have this pod fetch it, credentials or not. Refusing anything outside a
// small allowlist closes that off entirely, the same as
// komoot.allowedHost and garmin.allowedHost already do for their own
// clients — this one just has two hosts to allow instead of one, since the
// file genuinely lives on a different host than the API (cdn.wahooligan.com
// in the Cloud API docs' own example, not api.wahooligan.com).
func (c *Client) allowedFileHost(fileURL string) (onAPIHost bool, err error) {
	target, err := url.Parse(fileURL)
	if err != nil {
		return false, fmt.Errorf("wahoo: unusable route file URL %q: %w", fileURL, err)
	}

	if api, err := url.Parse(c.APIBase); err == nil &&
		target.Scheme == api.Scheme && strings.EqualFold(target.Host, api.Host) {
		return true, nil
	}
	if target.Scheme == "https" && strings.EqualFold(target.Host, wahooCDNHost) {
		return false, nil
	}

	return false, fmt.Errorf("wahoo: refusing to fetch a route file from %q, which is not a configured host", target.Host)
}

// routeRequest is Create and Update's shared shape: same form-encoded body,
// same response parsing, different method and URL.
//
// route[file] carries a data URI, not bare base64 — "data:application/vnd.fit;base64,<...>",
// exactly as the Cloud API docs show it. Getting this wrong silently produces
// a 422 with no field-level detail worth trusting, which cost real time to
// track down; it is written out here so nobody has to rediscover it.
func (c *Client) routeRequest(ctx context.Context, method, endpoint, accessToken string, route RouteRequest) (string, error) {
	if len(route.FIT) == 0 {
		return "", errors.New("wahoo: refusing to push a route with no course file")
	}

	form := url.Values{
		"route[external_id]":            {route.ExternalID},
		"route[name]":                   {route.Name},
		"route[provider_updated_at]":    {route.UpdatedAt.UTC().Format(time.RFC3339)},
		"route[workout_type_family_id]": {strconv.Itoa(workoutTypeFamilyFor(route.Sport))},
		"route[start_lat]":              {strconv.FormatFloat(route.StartLat, 'f', -1, 64)},
		"route[start_lng]":              {strconv.FormatFloat(route.StartLng, 'f', -1, 64)},
		"route[distance]":               {strconv.FormatFloat(route.DistanceM, 'f', -1, 64)},
		"route[ascent]":                 {strconv.FormatFloat(route.AscentM, 'f', -1, 64)},
		"route[file]":                   {"data:application/vnd.fit;base64," + base64.StdEncoding.EncodeToString(route.FIT)},
	}
	if route.Description != "" {
		form.Set("route[description]", route.Description)
	}
	if route.Filename != "" {
		form.Set("route[filename]", route.Filename)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("wahoo: route request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("wahoo: reading route response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("wahoo: route request returned %d: %s", resp.StatusCode, snippet(body))
	}

	var parsed routeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("wahoo: unreadable route response: %s", snippet(body))
	}
	if parsed.ID == 0 {
		return "", fmt.Errorf("wahoo: route response carried no id: %s", snippet(body))
	}
	return strconv.FormatInt(parsed.ID, 10), nil
}

func snippet(raw []byte) string {
	const limit = 300
	text := strings.TrimSpace(string(raw))
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}
