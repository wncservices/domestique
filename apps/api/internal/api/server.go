// Package api serves the JSON API behind the web UI, and the built frontend
// alongside it.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/basemap"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/fitcourse"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/oidcflow"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/ratelimit"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/sessions"
	"github.com/wncservices/domestique/apps/api/internal/settings"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
	syncer "github.com/wncservices/domestique/apps/api/internal/sync"
	"github.com/wncservices/domestique/apps/api/internal/targets"
	"github.com/wncservices/domestique/apps/api/internal/wahoo"
)

// maxUploadBytes bounds a multipart upload before it is read into memory.
const maxUploadBytes = 20 << 20 // 20 MiB

// Server holds the request-scoped dependencies.
type Server struct {
	Source   source.Library
	Config   *config.Config
	Store    state.Store
	Accounts *accounts.Store
	Auth     *auth.Authenticator
	Log      *slog.Logger
	// Komoot imports routes from a Komoot account. Nil disables the feature.
	Komoot KomootImporter

	// Links holds each rider's own sign-ins — Komoot, Garmin — made through
	// the UI. Nil disables connecting, but not the environment-configured
	// Komoot client.
	Links *providerlink.Store

	// Connector signs riders in to Komoot and resumes their stored sessions.
	Connector KomootConnector

	// Garmin signs riders in to Garmin Connect. Nil means the deployment
	// cannot offer it — see GarminConnector.
	Garmin GarminConnector

	// Wahoo drives the OAuth2 authorization-code flow for a rider's own
	// Wahoo account. Nil means WAHOO_CLIENT_ID/WAHOO_CLIENT_SECRET are not
	// set — /wahoo/connect and /wahoo/callback report "not configured"
	// rather than reaching for a client that does not exist, the same shape
	// Garmin's own missing-consumer case already uses.
	Wahoo *wahoo.Client

	// Settings holds deployment-wide configuration an admin sets from the UI,
	// today the Garmin OAuth1 consumer. Nil falls back to the environment for
	// everything.
	Settings *settings.Store

	// Basemap tracks the history of triggered tiles-basemap updates.
	// BasemapJobs actually runs them, against the Kubernetes API. Both nil
	// on a deployment without the tiles component's RBAC wired up
	// (basemapUpdate.enabled in domestique-chart) — the same "quietly
	// unavailable" shape Komoot/Garmin/Wahoo already use.
	Basemap     *basemap.Store
	BasemapJobs *basemap.Client

	// PreviewTiles fetches and decodes basemap layers for a route's card
	// preview; PreviewCache stores the result per route, keyed against
	// Basemap.LatestSucceeded so a rebuilt basemap invalidates every
	// cached preview automatically. Both nil — same "quietly unavailable"
	// shape as Basemap/BasemapJobs above — falls back to a bare 404, and
	// the client falls back to decoding the tiles itself.
	PreviewTiles *basemap.PreviewTiles
	PreviewCache *basemap.PreviewCache

	// KomootEnabled is what the operator asked for, which is not the same as
	// what they got: the config can turn Komoot on while the credentials are
	// missing, leaving Komoot nil. Keeping both apart lets the UI say "set
	// KOMOOT_EMAIL" instead of hiding a feature somebody deliberately enabled.
	KomootEnabled bool
	// WebFS is the built frontend. Nil serves an API-only server.
	WebFS fs.FS

	// LandingHost is the hostname that gets the logged-out page instead of
	// the app — the apex, while the app itself lives behind Authelia on a
	// subdomain. Empty serves the app to everyone, which is what a laptop
	// wants.
	//
	// One deployment serving both is deliberate: the landing page is three
	// screens of static content and does not earn a service of its own. If it
	// ever does, this is the seam to split on.
	LandingHost string
	// TargetFactory builds the provider adapter for an account. Nil uses the
	// real ones; tests substitute fakes, since the real adapters are stubs and
	// a successful push would otherwise be unreachable.
	TargetFactory func(model.Account) (targets.Target, error)

	// OIDC drives the authorization-code exchange and ID-token verification
	// for mode oidc. Nil in every other mode — the /sso/* endpoints 404
	// rather than reach for it.
	OIDC *oidcflow.Flow
	// Sessions holds a rider's login for mode oidc, the same store
	// auth.Authenticator.Identify reads from. Wired here too so /sso/login
	// and /sso/callback can create and delete sessions without a second path
	// back into internal/auth.
	Sessions *sessions.Store
	// Box seals the short-lived OIDC state cookie (PKCE verifier, nonce,
	// CSRF state) between /sso/login and /sso/callback. The same key as
	// Links/Settings/Sessions — one key, everything this app keeps sealed.
	Box *secrets.Box

	// People manages who has access, through Auth0's Management API — the
	// admin People page. Nil means the deployment has no Management API
	// credentials configured, which degrades the page to "not available"
	// rather than a 500, the same shape Komoot/Garmin already use for their
	// own optional credentials.
	People PeopleConnector

	// Crew holds who trusts whom with their routes — see internal/crew.
	// Unlike Links/Settings/People, no nil-degradation story: it needs no
	// external credential, only the database every deployment already has,
	// so it is wired unconditionally in runServe.
	Crew *crew.Store

	// ConnectLimiter throttles Garmin/Komoot connect by rider: both proxy a
	// password straight to a third party, so without a limit this server is
	// an unlimited, authenticated-only credential-stuffing proxy against
	// whichever Garmin or Komoot account a rider points it at — something
	// only the app can enforce, since it is the one thing here that knows
	// which endpoint relays a credential to somebody else's service. Wired
	// unconditionally in runServe, the same as Crew — pure in-memory state,
	// no external credential to be missing.
	//
	// This app's own traffic (its own auth, its own API) is deliberately
	// NOT rate limited here — Traefik and Cloudflare sit in front of every
	// deployment already and are the right layer for that, since they see
	// every request regardless of which app it hits and don't need
	// redeploying to change a limit.
	ConnectLimiter *ratelimit.Limiter

	// AuthActionLimiter throttles a rider's own self-service Auth0
	// Management API actions — a password-reset email
	// (handleSelfPasswordReset) and an MFA enrollment ticket
	// (handleEnrollMFA) — by rider. Same shape and same reasoning as
	// ConnectLimiter, a different third party: without a limit, this app
	// is an unlimited, authenticated-only proxy for spamming a rider's own
	// inbox or burning Auth0's send quota. A separate instance from
	// ConnectLimiter on purpose — failing a Garmin sign-in five times
	// should not also lock a rider out of resetting their password, an
	// unrelated action that happens to share the same rider key.
	AuthActionLimiter *ratelimit.Limiter

	// pushMu serialises pushes: two concurrent reconciles against the same
	// account would race on remote ids and on the state file.
	pushMu sync.Mutex
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.Handle("GET /api/metrics", metricsHandler())
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("PATCH /api/me", s.handleUpdateMe)
	mux.HandleFunc("POST /api/me/password-reset", s.handleSelfPasswordReset)
	mux.HandleFunc("GET /api/me/mfa", s.handleListMFA)
	mux.HandleFunc("POST /api/me/mfa/enroll", s.handleEnrollMFA)
	mux.HandleFunc("DELETE /api/me/mfa/{id}", s.handleRemoveMFA)
	mux.HandleFunc("GET /api/accounts", s.handleAccounts)
	mux.HandleFunc("POST /api/accounts", s.handleLinkAccount)
	mux.HandleFunc("DELETE /api/accounts/{id}", s.handleUnlinkAccount)
	mux.HandleFunc("PUT /api/accounts/{id}/auto-push", s.handleSetAccountAutoPush)
	mux.HandleFunc("GET /api/routes", s.handleRoutes)
	mux.HandleFunc("GET /api/routes/duplicates", s.handleRouteDuplicates)
	mux.HandleFunc("GET /api/plan", s.handlePlan)
	mux.HandleFunc("POST /api/push", s.handlePush)

	// The wildcard has to be last in a Go mux pattern, hence /api/tracks/<slug>
	// rather than /api/routes/<slug>/track. Same reasoning rules out a
	// /api/tracks/<slug>/preview suffix below: a slug can itself contain
	// "/", so {slug...} would swallow "/preview" as part of the slug for
	// one route while another registers it as a literal suffix — a
	// genuinely ambiguous, not just inelegant, combination. A separate
	// path prefix has no such overlap.
	mux.HandleFunc("GET /api/tracks/{slug...}", s.handleTrack)
	mux.HandleFunc("GET /api/track-preview/{slug...}", s.handleTrackPreview)
	mux.HandleFunc("GET /api/gpx/{slug...}", s.handleDownload)
	mux.HandleFunc("GET /api/fit/{slug...}", s.handleDownloadFIT)

	mux.HandleFunc("POST /api/routes", s.handleUpload)
	mux.HandleFunc("PATCH /api/routes/{slug...}", s.handleUpdate)
	mux.HandleFunc("DELETE /api/routes/{slug...}", s.handleDelete)

	mux.HandleFunc("GET /api/komoot/connection", s.handleKomootConnection)
	mux.HandleFunc("POST /api/komoot/connection", s.handleKomootConnect)
	mux.HandleFunc("DELETE /api/komoot/connection", s.handleKomootDisconnect)
	mux.HandleFunc("GET /api/komoot/tours", s.handleKomootTours)
	mux.HandleFunc("GET /api/komoot/tours/duplicates", s.handleKomootDuplicates)
	mux.HandleFunc("DELETE /api/komoot/tours/{id}", s.handleKomootTourDelete)
	mux.HandleFunc("POST /api/komoot/import", s.handleKomootImport)

	mux.HandleFunc("GET /api/garmin/connection", s.handleGarminConnection)
	mux.HandleFunc("POST /api/garmin/connection", s.handleGarminConnect)
	mux.HandleFunc("DELETE /api/garmin/connection", s.handleGarminDisconnect)
	mux.HandleFunc("GET /api/garmin/devices", s.handleGarminDevices)
	mux.HandleFunc("GET /api/garmin/courses", s.handleGarminCourseList)
	mux.HandleFunc("GET /api/garmin/courses/duplicates", s.handleGarminCourseDuplicates)
	mux.HandleFunc("DELETE /api/garmin/courses/{id}", s.handleGarminCourseDelete)
	mux.HandleFunc("POST /api/garmin/courses/import", s.handleGarminCourseImport)
	mux.HandleFunc("GET /api/garmin/consumer", s.handleGarminConsumer)
	mux.HandleFunc("PUT /api/garmin/consumer", s.handleSetGarminConsumer)
	mux.HandleFunc("DELETE /api/garmin/consumer", s.handleClearGarminConsumer)

	mux.HandleFunc("GET /api/settings/auto-sync", s.handleAutoSync)
	mux.HandleFunc("PUT /api/settings/auto-sync", s.handleSetAutoSync)

	mux.HandleFunc("GET /api/settings/basemap", s.handleBasemap)
	mux.HandleFunc("POST /api/settings/basemap/update", s.handleBasemapUpdate)

	mux.HandleFunc("GET /api/wahoo/connection", s.handleWahooConnection)
	mux.HandleFunc("DELETE /api/wahoo/connection", s.handleWahooDisconnect)
	mux.HandleFunc("GET /api/wahoo/routes", s.handleWahooRouteList)
	mux.HandleFunc("GET /api/wahoo/routes/duplicates", s.handleWahooRouteDuplicates)
	mux.HandleFunc("DELETE /api/wahoo/routes/{id}", s.handleWahooRouteDelete)
	mux.HandleFunc("POST /api/wahoo/routes/import", s.handleWahooRouteImport)

	mux.HandleFunc("GET /api/people", s.handlePeopleList)
	mux.HandleFunc("POST /api/people", s.handlePeopleInvite)
	mux.HandleFunc("PUT /api/people/{id}/role", s.handlePeopleSetRole)

	mux.HandleFunc("POST /api/crews", s.handleCreateCrew)
	mux.HandleFunc("GET /api/crews", s.handleListCrews)
	mux.HandleFunc("DELETE /api/crews/{id}", s.handleDeleteCrew)
	mux.HandleFunc("PATCH /api/crews/{id}", s.handleSetCrewAutoShare)
	mux.HandleFunc("POST /api/crews/{id}/join", s.handleJoinCrew)
	mux.HandleFunc("POST /api/crews/{id}/members", s.handleAddCrewMember)
	mux.HandleFunc("PUT /api/crews/{id}/members/{rider}", s.handleApproveCrewMember)
	mux.HandleFunc("DELETE /api/crews/{id}/members/{rider}", s.handleRemoveCrewMember)

	// Not under /api: these are browser navigations (redirects, a form post
	// from the SPA), not JSON calls, so they sit outside the /api/ 404
	// catch-all below and outside anything that assumes a JSON body.
	mux.HandleFunc("GET /sso/login", s.handleSSOLogin)
	mux.HandleFunc("GET /sso/callback", s.handleSSOCallback)
	mux.HandleFunc("POST /sso/logout", s.handleSSOLogout)

	mux.HandleFunc("GET /wahoo/connect", s.handleWahooConnect)
	mux.HandleFunc("GET /wahoo/callback", s.handleWahooCallback)

	// Anything else under /api is a 404 in JSON, not the SPA shell.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "no such endpoint: " + r.Method + " " + r.URL.Path,
		})
	})

	if s.WebFS != nil {
		mux.Handle("/", s.spaHandler())
	}

	// Outermost: otelhttp starts the span (extracting any inbound
	// traceparent, e.g. from Traefik) before anything else runs, so
	// authenticate/logRequests/instrument all execute inside it, and any
	// outbound call a handler makes has a real parent to attach to.
	return otelhttp.NewHandler(
		instrument(logRequests(s.logger(), s.authenticate(mux))),
		"domestique",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}

// authenticate resolves the identity once per request and puts it on the
// context. Endpoints then check permissions; this only decides *who* you are,
// not what you may do.
//
// /api/health stays open so a liveness probe does not need credentials.
// /api/me stays open for a different reason: it is how the frontend finds
// out whether anyone is signed in, and gating it the same as every other
// route means an anonymous visitor cannot even ask the question — they get a
// 401 instead of "you are not signed in", which is not the same thing. This
// was invisible under mode: proxy, where Traefik's forwardAuth blocks
// anonymous traffic before it ever reaches this app; mode: oidc is the first
// mode where the app itself is the front door, so it is the first mode where
// an anonymous request to /api/me is a real, expected case rather than one
// that never happens in practice.
//
// /api/config stays open for the same reason and was missed the first time:
// handleConfig carries no require() of its own — Komoot and most of Source
// were never meant to be secret — but the blanket Authorize check here
// gated it anyway. Under mode: proxy this was invisible for the same reason
// /api/me was: an anonymous request never arrived. Under mode: oidc it
// broke the anonymous bootstrap outright: useLibrary's initial Promise.all
// included config() alongside me(), so one 401 failed the whole batch and
// me stayed unset — no "Sign in" button, no visible explanation, just an
// empty error state.
//
// Identified like every other route, just never Authorize-blocked — unlike
// health/metrics below, handleConfig still needs to know who is asking:
// Source names the database host, internal cluster topology an admin can
// see and nobody else needs to.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" || r.URL.Path == "/api/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		id := s.authenticator().Identify(r)
		// The /api/me exemption is for reading who-you-are/why-you're-forbidden,
		// not for acting as that identity — an authenticated-but-unauthorized
		// caller (no recognized role) should still be blocked from PATCH /api/me
		// and everything else this path serves. GET is the only verb the
		// "explain the 403" case ever needed.
		exempt := r.URL.Path == "/api/config" || (r.URL.Path == "/api/me" && r.Method == http.MethodGet)
		if err := s.authenticator().Authorize(id); err != nil && !exempt {
			// Only gate the API. The SPA itself must still load, or the
			// browser gets a JSON blob instead of a page explaining itself.
			if strings.HasPrefix(r.URL.Path, "/api/") {
				status := http.StatusUnauthorized
				if errors.Is(err, auth.ErrForbidden) {
					status = http.StatusForbidden
				}
				writeJSON(w, status, map[string]string{"error": err.Error()})
				return
			}
		}

		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	})
}

func (s *Server) authenticator() *auth.Authenticator {
	if s.Auth != nil {
		return s.Auth
	}
	// A server built without an authenticator runs unauthenticated rather
	// than panicking; every caller in this repo sets one.
	a, _ := auth.New(auth.Config{Mode: auth.ModeNone})
	return a
}

// require checks a permission and writes the error itself, returning false
// when the caller should stop.
func (s *Server) require(w http.ResponseWriter, r *http.Request, p auth.Permission) bool {
	id := auth.FromContext(r.Context())
	if id.Role.Can(p) {
		return true
	}

	s.logger().Info("permission denied",
		"user", id.User, "role", id.Role, "permission", p, "path", r.URL.Path)
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": fmt.Sprintf("your role (%s) does not allow %s", roleLabel(id.Role), p),
	})
	return false
}

func roleLabel(r auth.Role) string {
	if r == auth.RoleNone {
		return "none"
	}
	return string(r)
}

// ---------- payloads ----------

type configDTO struct {
	// Source names the database and, for PostgreSQL, its host and port — not
	// a secret (the DSN's password is never in here, see dbx.Redact), but
	// still internal cluster topology nobody but an admin needs to see.
	// Empty, not just hidden client-side: the same reasoning the Garmin
	// consumer's own DTO already follows.
	Source string `json:"source,omitempty"`
	// Komoot is one of "disabled", "unconfigured" or "ready".
	Komoot string `json:"komoot"`
}

type accountDTO struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Rider    string `json:"rider"`
	Label    string `json:"label"`
	// Implemented reports whether pushes to this provider actually work yet.
	Implemented bool `json:"implemented"`
	// Mine tells the UI whether the viewer may unlink this one.
	Mine bool `json:"mine"`
	// AutoPush is whether auto-sync's background push includes this account
	// — see accounts.Store.SetAutoPush's own doc comment. Editable by the
	// same "mine" rule as unlinking.
	AutoPush bool `json:"autoPush"`
	// PossibleDuplicateOf names every other rider with an account for the
	// same provider carrying the same label. A hint, not a certainty — see
	// duplicateRiders — but usually means the same real device account,
	// linked twice because an OIDC login resolved to a rider identity this
	// deployment had not yet recognised as the same person.
	PossibleDuplicateOf []string `json:"possibleDuplicateOf,omitempty"`
}

type routeDTO struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	DistanceM   float64  `json:"distanceM"`
	AscentM     float64  `json:"ascentM"`
	StartLat    float64  `json:"startLat"`
	StartLng    float64  `json:"startLng"`
	PointCount  int      `json:"pointCount"`
	ContentHash string   `json:"contentHash"`
	Origin      string   `json:"origin"`
	Owner       string   `json:"owner,omitempty"`
	UpdatedAt   string   `json:"updatedAt"`
	// Sport is always resolved, never empty — model.RouteMeta.EffectiveSport,
	// not the raw stored value — so the UI never has to apply the "empty
	// means cycling" default itself.
	Sport string `json:"sport"`
	// Targets holds crew ids, not accounts — see internal/crew. Sharing a
	// route to a crew is the only way a client may name in here; own
	// devices are implicit and never listed.
	Targets []string `json:"targets"`
	// UnknownTargets names crew ids in Targets that do not currently
	// resolve — a crew deleted since, one the owner left, or (from before
	// crews existed) a raw account id. Never resolves to a push either way.
	UnknownTargets []string `json:"unknownTargets"`
	// OwnerCrews is every crew the route's *owner* currently, approvedly,
	// belongs to — exactly what a target picker may legally offer, correct
	// even when an admin is editing someone else's route.
	OwnerCrews []crewOptionDTO `json:"ownerCrews"`
	SyncState  []syncStatus    `json:"syncState"`
}

type crewOptionDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type syncStatus struct {
	AccountID string `json:"accountId"`
	Status    string `json:"status"` // synced | pending | stale
	RemoteID  string `json:"remoteId,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type planItemDTO struct {
	Op        string `json:"op"`
	AccountID string `json:"accountId"`
	Slug      string `json:"slug"`
	Reason    string `json:"reason"`
}

type libraryResponse struct {
	Routes   []routeDTO `json:"routes"`
	Problems []string   `json:"problems"`
}

type planResponse struct {
	Items    []planItemDTO `json:"items"`
	InSync   int           `json:"inSync"`
	Problems []string      `json:"problems"`
}

type pushResponse struct {
	Applied  int           `json:"applied"`
	Failures []string      `json:"failures"`
	Items    []planItemDTO `json:"items"`
}

// ---------- read handlers ----------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	dto := configDTO{Komoot: s.komootState()}
	if auth.FromContext(r.Context()).Role.Can(auth.PermManageSettings) {
		dto.Source = s.Source.Describe()
	}
	writeJSON(w, http.StatusOK, dto)
}

// komootState separates "nobody asked for Komoot" from "somebody asked and it
// could not start". Hiding the second looks identical to the first, which is
// how a missing environment variable turns into a feature that silently is
// not there.
func (s *Server) komootState() string {
	switch {
	case !s.KomootEnabled:
		return "disabled"
	case s.Komoot != nil || s.Links.CanStore():
		// Either the deployment has an account, or a rider can connect their
		// own. Both are usable, and the panel belongs on screen.
		return "ready"
	default:
		return "unconfigured"
	}
}

type meDTO struct {
	Authenticated bool              `json:"authenticated"`
	AuthMode      string            `json:"authMode"`
	User          string            `json:"user,omitempty"`
	Name          string            `json:"name,omitempty"`
	Email         string            `json:"email,omitempty"`
	Groups        []string          `json:"groups"`
	Role          string            `json:"role"`
	Permissions   []auth.Permission `json:"permissions"`
	// LogoutURL is the identity provider's, not this app's: the session being
	// ended belongs to the proxy. Empty means no sign-out button.
	LogoutURL string `json:"logoutUrl,omitempty"`
	// CanEditName and CanChangePassword tell Settings' Profile card whether
	// it has anything to offer. Both need id.Sub (only ModeOIDC ever
	// populates it) and a configured Management API client; changing a
	// password additionally needs the identity to be a database connection
	// — a Google-linked rider has no password here to change.
	CanEditName       bool `json:"canEditName"`
	CanChangePassword bool `json:"canChangePassword"`
}

// handleMe tells the UI who it is talking to and what to show. Without it the
// frontend would have to guess, and would offer buttons that 403.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	canEditName := s.People != nil && id.Sub != ""
	writeJSON(w, http.StatusOK, meDTO{
		// Enabled() alone used to be enough, because under mode: proxy an
		// anonymous request never reached this handler at all — Traefik's
		// forwardAuth stopped it first, so "auth is on" and "this visitor is
		// signed in" were the same fact by construction. mode: oidc breaks
		// that: /api/me is now reachable while anonymous (see authenticate),
		// on purpose, so the two questions have to be asked separately.
		Authenticated:     s.authenticator().Enabled() && !id.Anonymous(),
		AuthMode:          string(s.authenticator().Mode()),
		User:              id.User,
		Name:              id.Name,
		Email:             id.Email,
		Groups:            orEmpty(id.Groups),
		Role:              roleLabel(id.Role),
		Permissions:       orEmpty(id.Role.Permissions()),
		LogoutURL:         s.authenticator().LogoutURL(),
		CanEditName:       canEditName,
		CanChangePassword: canEditName && id.Provider() == "auth0",
	})
}

// handleUpdateMe lets a signed-in rider change their own display name —
// Auth0 is the system of record (Update writes there first), and the
// current session is patched to match afterward so the change is visible
// immediately rather than after sessionTTL forces a fresh login.
func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.meAuth0Sub(w, r)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name cannot be empty"})
		return
	}

	// identityFromToken falls back to name (then nickname, then sub) as the
	// rider string whenever an issuer sends no preferred_username — true of
	// this deployment's Auth0 database connection. That makes this endpoint
	// capable of far more than a display-name edit: without this check,
	// renaming to "wilant" and signing in again would make the caller BE
	// wilant — inheriting their routes, their linked Garmin/Wahoo sessions
	// (including delete/push), and their crew memberships. Refusing a name
	// that already belongs to somebody else closes that off. It does not
	// address the older, narrower issue that a rider legitimately renaming
	// their OWN identity this way still needs the `rename-rider` CLI's
	// migration to carry their own existing rows forward — that gap
	// predates this check and is out of scope for it.
	rider := auth.FromContext(r.Context()).User
	if taken, err := s.riderIdentityInUse(r.Context(), name, rider); err != nil {
		s.fail(w, err)
		return
	} else if taken {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "that name is already in use — pick a different one",
		})
		return
	}

	if _, err := s.People.UpdateName(r.Context(), sub, name); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	// Best-effort: the Auth0 write above already succeeded, so a session
	// that fails to pick up the new name here just keeps showing the old
	// one until it expires or the rider signs in again — not worth failing
	// an otherwise-successful request over.
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		if err := s.Sessions.UpdateName(cookie.Value, name); err != nil {
			s.logger().Warn("updating session after name change failed", "err", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// riderIdentityInUse reports whether candidate, once normalized the same
// way every rider comparison in this codebase is (lowercased, trimmed —
// see crew.normalizeRider and source.normalizeRider), already identifies
// someone other than self. Checked against every place a rider string is
// the key: route ownership, linked head units, provider connections (each
// probed directly rather than listed — providerlink.Store has no List, and
// there are only three providers to ask), and crew membership of any
// status. self is excluded so a rider fixing the case of their own existing
// name, or making no real change, is never refused.
func (s *Server) riderIdentityInUse(ctx context.Context, candidate, self string) (bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(candidate))
	self = strings.ToLower(strings.TrimSpace(self))
	if normalized == "" || normalized == self {
		return false, nil
	}

	if s.Source != nil {
		routes, _, err := s.Source.List(ctx)
		if err != nil {
			return false, fmt.Errorf("checking route ownership: %w", err)
		}
		for _, rt := range routes {
			if strings.EqualFold(rt.Owner, normalized) {
				return true, nil
			}
		}
	}

	if s.Accounts != nil {
		accountList, err := s.Accounts.List()
		if err != nil {
			return false, fmt.Errorf("checking linked accounts: %w", err)
		}
		for _, a := range accountList {
			if strings.EqualFold(a.Rider, normalized) {
				return true, nil
			}
		}
	}

	if s.Links != nil {
		for _, provider := range []string{garminProvider, wahooProvider, komootProvider} {
			switch _, err := s.Links.Get(provider, normalized); {
			case err == nil:
				return true, nil
			case errors.Is(err, providerlink.ErrNotFound):
				// expected — that provider, that name, nobody connected
			default:
				return false, fmt.Errorf("checking %s connections: %w", provider, err)
			}
		}
	}

	if s.Crew != nil {
		has, err := s.Crew.HasRider(ctx, normalized)
		if err != nil {
			return false, fmt.Errorf("checking crew membership: %w", err)
		}
		if has {
			return true, nil
		}
	}

	return false, nil
}

// handleSelfPasswordReset sends the signed-in rider Auth0's own
// "reset your password" email — the same public endpoint the People page
// reuses for invites (see auth0mgmt.SendInviteEmail's doc comment); a
// forgotten password and a rider who wants a new one complete the identical
// flow. Only offered for a database-connection identity: a Google-linked
// rider has no password here to reset in the first place.
func (s *Server) handleSelfPasswordReset(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	if id.Sub == "" {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this account has no Auth0 identity to reset a password for",
		})
		return
	}
	if provider := id.Provider(); provider != "auth0" {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": fmt.Sprintf("this account signs in through %s — there is no password to reset here", provider),
		})
		return
	}
	if !s.peopleAvailable(w) {
		return
	}
	if id.Email == "" {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "no email on file for this account"})
		return
	}
	if !s.rateLimitAuthAction(w, id.User) {
		return
	}

	if err := s.People.SendInviteEmail(r.Context(), id.Email); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

type enrollmentDTO struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Type   string `json:"type"`
	Name   string `json:"name,omitempty"`
}

// meAuth0Sub is the sub-check shared by every /api/me/mfa handler — separate
// from peopleAvailable, which only speaks to whether a Management API client
// exists at all.
func (s *Server) meAuth0Sub(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := auth.FromContext(r.Context())
	if id.Sub == "" {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this account has no Auth0 identity to manage",
		})
		return "", false
	}
	if !s.peopleAvailable(w) {
		return "", false
	}
	return id.Sub, true
}

// handleListMFA reports the rider's own enrolled factors — an authenticator
// app, a phone, a security key, whatever Guardian's tenant-wide policy
// allows. Not gated on a database-connection identity the way password
// reset is: MFA applies regardless of how a rider signs in.
func (s *Server) handleListMFA(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.meAuth0Sub(w, r)
	if !ok {
		return
	}
	enrollments, err := s.People.ListEnrollments(r.Context(), sub)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	out := make([]enrollmentDTO, 0, len(enrollments))
	for _, e := range enrollments {
		out = append(out, enrollmentDTO{ID: e.ID, Status: e.Status, Type: e.Type, Name: e.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleEnrollMFA hands back a one-time link to Auth0's own hosted
// enrollment page — this app never renders a QR code or talks to an
// authenticator app itself, it only asks Guardian for the ticket and lets
// the rider's browser take it from there.
func (s *Server) handleEnrollMFA(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.meAuth0Sub(w, r)
	if !ok {
		return
	}
	// Keyed by rider, not sub — the same key space handleSelfPasswordReset
	// uses, so the two share one AuthActionLimiter budget per person
	// rather than each getting its own on a technicality.
	if !s.rateLimitAuthAction(w, auth.FromContext(r.Context()).User) {
		return
	}
	ticketURL, err := s.People.CreateGuardianEnrollmentTicket(r.Context(), sub)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticketUrl": ticketURL})
}

// handleRemoveMFA deletes one of the rider's own factors. The ownership
// check here is load-bearing, not defensive dressing: Guardian's delete
// endpoint (see auth0mgmt.DeleteEnrollment) is keyed by enrollment id alone,
// with no user scoping of its own — without first confirming the id belongs
// to whoever is asking, any signed-in rider could strip another rider's MFA
// by guessing or having ever seen their enrollment id, which is exactly the
// kind of account-takeover step MFA exists to prevent.
func (s *Server) handleRemoveMFA(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.meAuth0Sub(w, r)
	if !ok {
		return
	}
	enrollmentID := r.PathValue("id")

	enrollments, err := s.People.ListEnrollments(r.Context(), sub)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	owned := false
	for _, e := range enrollments {
		if e.ID == enrollmentID {
			owned = true
			break
		}
	}
	if !owned {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your enrollment"})
		return
	}

	if err := s.People.DeleteEnrollment(r.Context(), enrollmentID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
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

	identity := auth.FromContext(r.Context())
	// mode: none has no multi-tenancy to scope in the first place — every
	// request is LocalIdentity, the one operator running this on their own
	// machine (see auth.LocalIdentity's own doc comment: "nothing is
	// gated... anything less would make the app unusable in development for
	// no security gain"). Scoping there would not protect anyone from
	// anyone; it would just hide their own data from them.
	toList := linked
	if s.authenticator().Mode() != auth.ModeNone {
		toList = listableAccounts(identity, linked, crews)
	}
	out := make([]accountDTO, 0, len(toList))
	for _, a := range toList {
		out = append(out, accountDTO{
			ID:                  a.ID,
			Provider:            string(a.Provider),
			Rider:               a.Rider,
			Label:               a.Label,
			Implemented:         targets.Implemented(a.Provider),
			Mine:                identity.CanEditRoute(a.Rider),
			AutoPush:            a.AutoPush,
			PossibleDuplicateOf: duplicateRiders(linked, a),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// duplicateRiders names every other rider with an account for the same
// provider and the same non-empty label as a.
//
// The label a Garmin (or Wahoo, once implemented) account gets is the
// provider's own display name, set once at link time from the live session
// (handleGarminSignIn's ensureAccount call) — not something a rider types.
// Two different rider identities carrying the same provider display name is
// exactly what this deployment's own history produced: an OIDC login
// resolving to a rider string that had not been recognised as an existing
// person yet, linking the same real device account a second time. It is a
// hint, not a certainty — two unrelated real accounts could coincidentally
// share a display name — which is why this only flags, it never hides or
// blocks anything itself.
func duplicateRiders(all []model.Account, a model.Account) []string {
	if a.Label == "" {
		return nil
	}
	var out []string
	for _, other := range all {
		if other.ID == a.ID || other.Provider != a.Provider || other.Label != a.Label {
			continue
		}
		out = append(out, other.Rider)
	}
	return out
}

// handleLinkAccount connects a provider for the signed-in rider.
//
// The rider comes from the session, never the request body: an account is
// yours because you linked it, and letting the body say otherwise would let
// someone create an account they cannot then unlink.
func (s *Server) handleLinkAccount(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}

	var body struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
		// Rider is honoured only for an admin linking on someone's behalf.
		Rider string `json:"rider"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	identity := auth.FromContext(r.Context())
	rider := identity.User
	if body.Rider != "" && !strings.EqualFold(body.Rider, identity.User) {
		if !identity.Role.Can(auth.PermEditAny) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "only an admin can link an account for somebody else",
			})
			return
		}
		rider = body.Rider
	}

	account, err := s.Accounts.Link(model.Provider(body.Provider), rider, body.Label)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, accounts.ErrExists) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("account linked", "id", account.ID, "by", identity.User)
	writeJSON(w, http.StatusCreated, accountDTO{
		ID:          account.ID,
		Provider:    string(account.Provider),
		Rider:       account.Rider,
		Label:       account.Label,
		Implemented: targets.Implemented(account.Provider),
		Mine:        true,
		AutoPush:    account.AutoPush,
	})
}

// handleSetAccountAutoPush flips whether auto-sync's background push
// includes one account — the same ownership rule as unlinking it: the
// rider who linked it, or an admin.
func (s *Server) handleSetAccountAutoPush(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}

	id := cleanSlug(r.PathValue("id"))
	account, err := s.Accounts.Get(id)
	if err != nil {
		s.failAccount(w, err)
		return
	}

	identity := auth.FromContext(r.Context())
	if !identity.CanEditRoute(account.Rider) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "that account belongs to " + account.Rider + "; only they or an admin can change it",
		})
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if err := s.Accounts.SetAutoPush(id, body.Enabled); err != nil {
		s.failAccount(w, err)
		return
	}
	account.AutoPush = body.Enabled

	s.logger().Info("account auto-push changed", "id", id, "enabled", body.Enabled, "by", identity.User)
	writeJSON(w, http.StatusOK, accountDTO{
		ID:          account.ID,
		Provider:    string(account.Provider),
		Rider:       account.Rider,
		Label:       account.Label,
		Implemented: targets.Implemented(account.Provider),
		Mine:        true,
		AutoPush:    account.AutoPush,
	})
}

// handleUnlinkAccount removes a linked provider. Riders may unlink their own;
// admins anyone's.
func (s *Server) handleUnlinkAccount(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageAccounts) {
		return
	}

	id := cleanSlug(r.PathValue("id"))
	account, err := s.Accounts.Get(id)
	if err != nil {
		s.failAccount(w, err)
		return
	}

	identity := auth.FromContext(r.Context())
	if !identity.CanEditRoute(account.Rider) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "that account belongs to " + account.Rider + "; only they or an admin can unlink it",
		})
		return
	}

	if err := s.Accounts.Unlink(id); err != nil {
		s.failAccount(w, err)
		return
	}

	s.logger().Info("account unlinked", "id", id, "by", identity.User)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) failAccount(w http.ResponseWriter, err error) {
	if errors.Is(err, accounts.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.fail(w, err)
}

// linkedAccounts reads the accounts, or writes the error and reports false.
func (s *Server) linkedAccounts(w http.ResponseWriter) ([]model.Account, bool) {
	linked, err := s.Accounts.List()
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	return linked, true
}

// crewSnapshot reads every crew and its current approved membership, or
// writes the error and reports false — the same shape linkedAccounts keeps,
// for the same reason: fetched fresh per request, never cached, so a
// membership change takes effect on the very next call.
//
// Nil-safe on purpose, the same reasoning providerlink.Store.CanStore's own
// doc comment gives: production always wires Server.Crew (runServe builds
// it unconditionally), but a Server built by hand for a test that has
// nothing to do with crews should not have to set one just to reach a route
// handler. An empty Snapshot is the correct, real state of a deployment
// before anyone has created a crew — TargetsFor falls back to the owner's
// own accounts, exactly as if crews did not exist yet.
func (s *Server) crewSnapshot(w http.ResponseWriter, r *http.Request) (crew.Snapshot, bool) {
	if s.Crew == nil {
		return crew.Snapshot{}, true
	}
	snap, err := s.Crew.Snapshot(r.Context())
	if err != nil {
		s.fail(w, err)
		return crew.Snapshot{}, false
	}
	return snap, true
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
		return
	}

	routes, problems, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
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
	identity := auth.FromContext(r.Context())
	routes = visibleRoutes(routes, identity, crews)
	// ownAccountsOnly, not listableAccounts or visibleAccounts: this is a
	// read-only display of what would happen on *your own* devices, not a
	// roster of a crew's or authority to push/delete on anyone's behalf.
	// toRouteDTO passes this same slice straight into config.TargetsFor,
	// which can only ever resolve a route's SyncState against the accounts
	// it is given — narrowing the input here is what narrows the display,
	// for every role including the route's own owner and an admin. Pushing
	// (handlePush) and planning (handlePlan) call TargetsFor with the full
	// crew-wide account list separately — a route shared to a crew still
	// genuinely reaches every member's devices; only this viewer-facing
	// display is scoped down to one person's own.
	linked = ownAccountsOnly(identity, linked)

	writeJSON(w, http.StatusOK, libraryResponse{
		Routes:   s.toRouteDTOs(r.Context(), routes, linked, crews),
		Problems: orEmpty(problems),
	})
}

// handleTrack returns the raw coordinates so the UI can draw a route preview
// without shipping a map library or calling out to a tile server.
func (s *Server) handleTrack(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
		return
	}

	slug := cleanSlug(r.PathValue("slug"))
	if !s.mayView(w, r, slug) {
		return
	}
	points, err := s.Source.Track(r.Context(), slug)
	if err != nil {
		s.failLookup(w, err)
		return
	}

	coords := make([][2]float64, 0, len(points))
	for _, p := range points {
		coords = append(coords, [2]float64{p.Lat, p.Lon})
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "points": coords})
}

// handleTrackPreview returns the earth/landuse/water/roads background wash
// for a route's card preview, precomputed and cached server-side (see
// basemap.PreviewTiles/PreviewCache) instead of every client re-fetching
// and decoding PMTiles vector tiles itself. Unavailable — same "quietly
// missing" shape as the rest of this package's optional features — is a
// plain 404 with no body worth parsing; the client's own fallback is to
// fall back to decoding the tiles itself, exactly as it did before this
// endpoint existed, so a 404 here is not a broken deployment.
func (s *Server) handleTrackPreview(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
		return
	}

	slug := cleanSlug(r.PathValue("slug"))
	if !s.mayView(w, r, slug) {
		return
	}

	if s.Basemap == nil || s.PreviewTiles == nil || s.PreviewCache == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	basemapRec, err := s.Basemap.LatestSucceeded()
	if err != nil {
		// basemap.ErrNoRecord (nobody has ever successfully built one) is
		// the overwhelmingly likely case; any other error is logged, but
		// either way there is nothing to compute a preview against.
		if !errors.Is(err, basemap.ErrNoRecord) {
			s.logger().Error("reading latest succeeded basemap failed", "err", err)
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if cached, found, err := s.PreviewCache.Get(slug, basemapRec.ID); err != nil {
		s.logger().Error("reading track preview cache failed", "slug", slug, "err", err)
	} else if found {
		writeTrackPreviewJSON(w, cached)
		return
	}

	points, err := s.Source.Track(r.Context(), slug)
	if err != nil {
		s.failLookup(w, err)
		return
	}
	coords := make([][2]float64, 0, len(points))
	for _, p := range points {
		coords = append(coords, [2]float64{p.Lat, p.Lon})
	}

	start := time.Now()
	layers := basemap.PreviewLayers{}
	west, south, east, north, ok := basemap.RouteBBox(coords)
	if !ok {
		// Fewer than 2 points — the same threshold TrackPreview.vue's own
		// projection computed uses, below which there is no route shape
		// to fit a background to at all. Worth a log line of its own:
		// unlike every other empty-result path here, this one means the
		// route itself is degenerate, not that the fetch failed or the
		// basemap doesn't cover it.
		s.logger().Warn("track preview: route has too few points to compute a bbox", "slug", slug, "points", len(coords))
	} else {
		layers, err = s.PreviewTiles.FetchLayers(r.Context(), west, south, east, north)
		if err != nil {
			s.logger().Error("fetching track preview layers failed", "slug", slug, "err", err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		s.logger().Info("computed track preview",
			"slug", slug, "elapsed", time.Since(start),
			"earth_rings", len(layers.Earth), "landuse_rings", len(layers.Landuse),
			"water_rings", len(layers.Water), "water_lines", len(layers.WaterLines),
			"roads", len(layers.Roads))
	}

	body, err := json.Marshal(layers)
	if err != nil {
		s.logger().Error("encoding track preview failed", "slug", slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not encode preview"})
		return
	}
	if err := s.PreviewCache.Put(slug, basemapRec.ID, string(body)); err != nil {
		// The response is still good; only the next request pays the
		// recompute cost again.
		s.logger().Error("caching track preview failed", "slug", slug, "err", err)
	}

	writeTrackPreviewJSON(w, string(body))
}

// writeTrackPreviewJSON writes an already-serialized track-preview JSON
// document verbatim — for handleTrackPreview's two paths (a cache hit's
// stored string, and a fresh json.Marshal result) where re-decoding just
// to hand it to writeJSON would be pointless work.
//
// Cache-Control here is what makes a page *reload* fast, not just a first
// visit: the server-side cache (basemap.PreviewCache) already makes a
// cache-miss recompute rare, but without this header every reload still
// paid a full network round trip per visible card to re-fetch content
// that had not changed at all. private (not public — Cloudflare sits in
// front of this app, and this response is gated by mayView, so a shared
// cache must never serve one rider's answer to another) with a day-long
// max-age: the only thing that can actually change this response is an
// admin rebuilding the basemap (see PreviewCache's own doc comment on
// what invalidates it), which is rare and manual — a browser showing a
// slightly stale background wash for up to a day afterward is a cosmetic
// gap, not a correctness one, and cheaper than adding conditional-GET
// (ETag/If-None-Match) support for a staleness window this low-stakes.
func writeTrackPreviewJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	// nosniff stops a browser deciding otherwise, same reasoning as
	// handleDownload/handleDownloadFIT below.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// #nosec G705 -- this app's own server-computed JSON (route geometry),
	// never user-supplied HTML, served with a fixed non-HTML content type.
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
		return
	}

	slug := cleanSlug(r.PathValue("slug"))
	if !s.mayView(w, r, slug) {
		return
	}
	raw, err := s.Source.GPX(r.Context(), slug)
	if err != nil {
		s.failLookup(w, err)
		return
	}

	filename := strings.ReplaceAll(slug, "/", "-") + ".gpx"
	w.Header().Set("Content-Type", "application/gpx+xml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	// The body is a GPX file served as a download, never rendered as a page.
	// nosniff stops a browser deciding otherwise.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// #nosec G705 -- served as an attachment with a fixed content type, not HTML.
	if _, err := w.Write(raw); err != nil {
		s.logger().Error("write gpx", "err", err)
	}
}

// handleDownloadFIT converts a route to a Garmin FIT course on the fly.
//
// Useful on its own — a FIT can be copied to a device over USB — and it is the
// same conversion the Wahoo adapter will use, so being able to download one
// and load it onto a real head unit is how the conversion gets proven.
//
// Turn cues are opt-in with ?cues=1, because they are inferred from the track's
// geometry rather than authored: see fitcourse.DeriveTurns.
func (s *Server) handleDownloadFIT(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
		return
	}

	slug := cleanSlug(r.PathValue("slug"))
	if !s.mayView(w, r, slug) {
		return
	}
	raw, err := s.Source.GPX(r.Context(), slug)
	if err != nil {
		s.failLookup(w, err)
		return
	}

	points, err := gpx.ParsePoints(raw)
	if err != nil {
		s.fail(w, err)
		return
	}

	name := slug
	sport := model.SportCycling
	if routes, _, listErr := s.Source.List(r.Context()); listErr == nil {
		for _, route := range routes {
			if route.Slug == slug {
				name = route.Name
				sport = route.EffectiveSport()
				break
			}
		}
	}

	fitBytes, err := fitcourse.Encode(points, fitcourse.Options{
		Name:     name,
		Sport:    fitcourse.SportFromString(string(sport)),
		TurnCues: r.URL.Query().Get("cues") == "1",
	})
	if err != nil {
		s.fail(w, err)
		return
	}

	filename := strings.ReplaceAll(slug, "/", "-") + ".fit"
	w.Header().Set("Content-Type", "application/vnd.ant.fit")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// #nosec G705 -- binary FIT served as an attachment with a fixed content
	// type and nosniff, never rendered as a page.
	if _, err := w.Write(fitBytes); err != nil {
		s.logger().Error("write fit", "err", err)
	}
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
		return
	}

	routes, problems, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
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
	identity := auth.FromContext(r.Context())
	routes = visibleRoutes(routes, identity, crews)
	linked = visibleAccounts(identity, linked, crews)

	plan, err := syncer.BuildPlan(r.Context(), routes, linked, s.Store, crews)
	if err != nil {
		s.fail(w, err)
		return
	}

	changes := plan.Changes()
	writeJSON(w, http.StatusOK, planResponse{
		Items:    toPlanDTOs(changes),
		InSync:   len(plan.Items) - len(changes),
		Problems: orEmpty(problems),
	})
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermPush) {
		return
	}

	selected, ok := readPushSelection(w, r)
	if !ok {
		return
	}

	// autoPushOnly is false: a rider clicking "Push to devices" themselves
	// pushes to whatever they picked, regardless of any account's own
	// auto-push preference — that preference only governs the unattended
	// path (autoSyncIfEnabled, the auto-import poller's own push), never a
	// push a human triggered on purpose.
	//
	// identity is not nil: an HTTP-triggered push is always scoped to what
	// the caller may act on (their own accounts, a crew fellow's, or
	// everything for an admin) — see runPush's own doc comment for why
	// that matters here specifically, more than for reading.
	identity := auth.FromContext(r.Context())
	resp, err := s.runPush(r.Context(), selected, false, &identity)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// runPush is handlePush's own logic, pulled out so autoSyncIfEnabled (a
// background push after an upload/edit, with no HTTP request driving it —
// see autosync.go) can run exactly the same build-plan-and-apply sequence
// rather than a second, drifting copy of it. selected nil means "everything
// out of sync", the same meaning readPushSelection already gives a request
// with no body.
//
// autoPushOnly restricts the whole push to accounts with AutoPush set —
// true for every unattended caller (autoSyncIfEnabled, the auto-import
// poller), false for a rider's own "Push to devices" click, which always
// honors whatever they picked regardless of that per-account preference.
//
// caller, when non-nil, restricts the whole push (including which pending
// deletes ever get considered, not just which get applied) to accounts
// caller may see — see visibleAccounts. nil is the unattended path's own
// identity: the poller and auto-sync are not any one rider acting, they
// are the deployment's own settings taking effect, so they need the full
// account list the same way BuildPlan always has. handlePush always passes
// the real caller — an HTTP request is a specific rider acting, and
// selected alone was not enough of a guard: nothing stopped that map from
// naming another rider's account id, plan or no plan naming it back.
func (s *Server) runPush(ctx context.Context, selected map[model.PlanKey]bool, autoPushOnly bool, caller *auth.Identity) (pushResponse, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()

	routes, _, err := s.Source.List(ctx)
	if err != nil {
		return pushResponse{}, err
	}

	linked, err := s.Accounts.List()
	if err != nil {
		return pushResponse{}, err
	}
	var crews crew.Snapshot
	if s.Crew != nil {
		crews, err = s.Crew.Snapshot(ctx)
		if err != nil {
			return pushResponse{}, err
		}
	}
	if caller != nil {
		linked = visibleAccounts(*caller, linked, crews)
	}
	if autoPushOnly {
		linked = autoPushAccounts(linked)
	}

	build := s.TargetFactory
	if build == nil {
		build = s.targetFactory().Build
	}

	byAccount := map[string]targets.Target{}
	for _, account := range linked {
		target, err := build(account)
		if err != nil {
			return pushResponse{}, err
		}
		byAccount[account.ID] = target
	}

	plan, err := syncer.BuildPlan(ctx, routes, linked, s.Store, crews)
	if err != nil {
		return pushResponse{}, err
	}
	plan = plan.Select(selected)

	changes := plan.Changes()
	failures := syncer.Apply(ctx, plan, s.Store, byAccount, s.recordPushResult)

	messages := make([]string, 0, len(failures))
	for _, f := range failures {
		messages = append(messages, f.Error())
	}

	// Log why, not just how many. "failures=30" is a number nobody can act
	// on, and the reasons only existed in the HTTP response — which is no
	// help when the push was a scheduled one, and little help when it was
	// not. Thirty failures are usually one cause thirty times, so the
	// distinct reasons are what is worth having.
	//
	// Error, not Warn: a route that was supposed to reach a head unit and
	// did not is a failed push, not a degraded one. Today's own case is why
	// that distinction matters — this exact line sat at Warn through several
	// deploys where every single push failed, which reads as noise rather
	// than as the incident it was.
	if len(failures) > 0 {
		s.logger().Error("push finished with failures",
			"changes", len(changes), "failures", len(failures),
			"reasons", distinctReasons(messages))
	} else {
		s.logger().Info("push finished", "changes", len(changes), "failures", 0)
	}
	return pushResponse{
		Applied:  len(changes) - len(failures),
		Failures: messages,
		Items:    toPlanDTOs(changes),
	}, nil
}

// autoPushAccounts narrows a linked-account list to only the ones opted in
// to auto-push — see runPush's own doc comment for when this applies.
func autoPushAccounts(linked []model.Account) []model.Account {
	out := make([]model.Account, 0, len(linked))
	for _, a := range linked {
		if a.AutoPush {
			out = append(out, a)
		}
	}
	return out
}

// readPushSelection reads the optional {"items": [{"accountId","slug"}, ...]}
// body that narrows a push to specific plan items. No body at all — the
// shape every push sent before this existed, and what a scripted client
// still sends today — means "everything", the same as before.
func readPushSelection(w http.ResponseWriter, r *http.Request) (map[model.PlanKey]bool, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, false
	}
	if len(raw) == 0 {
		return nil, true
	}

	var body struct {
		Items []struct {
			AccountID string `json:"accountId"`
			Slug      string `json:"slug"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, false
	}
	if len(body.Items) == 0 {
		return nil, true
	}

	selected := make(map[model.PlanKey]bool, len(body.Items))
	for _, item := range body.Items {
		selected[model.PlanKey{AccountID: item.AccountID, Slug: item.Slug}] = true
	}
	return selected, true
}

// ---------- write handlers (writable sources only) ----------

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermUploadRoute) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	// #nosec G120 -- the body is bounded by MaxBytesReader on the line above.
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "could not read the upload: " + err.Error(),
		})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "expected a GPX file in the `file` field",
		})
		return
	}
	defer func() { _ = file.Close() }()

	raw, err := io.ReadAll(file)
	if err != nil {
		s.fail(w, err)
		return
	}

	// Ownership comes from the authenticated identity, never from the form:
	// a rider could otherwise upload a route as somebody else and put it
	// beyond their own ability to delete.
	uploader := auth.FromContext(r.Context()).User
	if uploader == "" {
		uploader = r.FormValue("uploadedBy")
	}

	sport, err := parseSport(r.FormValue("sport"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	req := source.CreateRequest{
		Filename:   header.Filename,
		Name:       r.FormValue("name"),
		Descript:   r.FormValue("description"),
		Tags:       splitCSV(r.FormValue("tags")),
		UploadedBy: uploader,
		GPX:        raw,
		Sport:      sport,
	}
	if targetsField := r.FormValue("targets"); targetsField != "" {
		list := splitCSV(targetsField)
		req.Targets = &list
	}

	crews, ok := s.crewSnapshot(w, r)
	if !ok {
		return
	}
	if req.Targets != nil {
		if err := validateCrewTargets(*req.Targets, uploader, crews); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	} else if auto := crews.AutoShareCrewsFor(uploader); len(auto) > 0 {
		// Only fills in when the uploader made no target choice of their
		// own — an explicit empty selection ("targets=" with nothing after
		// it doesn't reach this branch at all, since it never sets
		// req.Targets in the first place) still can't currently opt out of
		// auto-share through this form field, but a rider who wants that
		// can always retarget afterward from the route card.
		req.Targets = &auto
	}

	route, err := s.Source.Create(r.Context(), req)
	if err != nil {
		// A bad GPX is the caller's problem, not a server fault.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	linked, ok := s.linkedAccounts(w)
	if !ok {
		return
	}
	// See handleRoutes' identical comment: this response renders the same
	// SyncState shape the library grid does, so it takes the same rule —
	// only the uploader's own accounts, not a crew fellow's.
	linked = ownAccountsOnly(auth.FromContext(r.Context()), linked)

	s.logger().Info("route uploaded", "slug", route.Slug, "by", req.UploadedBy)
	s.autoSyncIfEnabled(req.UploadedBy)
	writeJSON(w, http.StatusCreated, s.toRouteDTO(r.Context(), route, linked, crews))
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermEditOwn) {
		return
	}

	slug := cleanSlug(r.PathValue("slug"))
	if !s.mayEdit(w, r, slug) {
		return
	}

	var body struct {
		Name        *string   `json:"name"`
		Description *string   `json:"description"`
		Tags        *[]string `json:"tags"`
		Targets     *[]string `json:"targets"`
		Enabled     *bool     `json:"enabled"`
		Sport       *string   `json:"sport"`
		// ClaimOwner lets a rider become the owner of a route that
		// currently has none — an import with no --owner, or a Garmin
		// course sync-back nobody has claimed. mayEdit already treats an
		// ownerless route as fair game for any edit-own rider (not just an
		// admin — see auth.Identity.CanEditRoute), so this only has to
		// enforce the one thing that check doesn't: the route must
		// actually still be ownerless when this request lands, or two
		// riders racing to claim the same orphan could otherwise silently
		// steal it from each other. The read below catches that for the
		// common case with a friendly, immediate 409; source.UpdateRequest's
		// own ClaimOwner is what actually closes the race, atomically, at
		// write time — see its doc comment for why the read alone is not
		// enough.
		ClaimOwner bool `json:"claimOwner,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	crews, ok := s.crewSnapshot(w, r)
	if !ok {
		return
	}

	var newOwner *string
	var ownerForValidation string
	if body.Targets != nil || body.ClaimOwner {
		// Fetched before the write either way: Targets validates against
		// who owns the route right now, and ClaimOwner has to confirm
		// nobody already does.
		owner, err := s.routeOwner(r.Context(), slug)
		if err != nil {
			s.failLookup(w, err)
			return
		}
		ownerForValidation = owner

		if body.ClaimOwner {
			if owner != "" {
				writeJSON(w, http.StatusConflict, map[string]string{
					"error": "this route already has an owner",
				})
				return
			}
			claimed := auth.FromContext(r.Context()).User
			newOwner = &claimed
			ownerForValidation = claimed
		}
	}

	if body.Targets != nil {
		if err := validateCrewTargets(*body.Targets, ownerForValidation, crews); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	var sport *model.Sport
	if body.Sport != nil {
		parsed, err := parseSport(*body.Sport)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		sport = &parsed
	}

	route, err := s.Source.Update(r.Context(), slug, source.UpdateRequest{
		Name:       body.Name,
		Descript:   body.Description,
		Tags:       body.Tags,
		Targets:    body.Targets,
		Enabled:    body.Enabled,
		Owner:      newOwner,
		Sport:      sport,
		ClaimOwner: body.ClaimOwner,
	})
	if errors.Is(err, source.ErrAlreadyOwned) {
		// The pre-check above already caught the common case; landing here
		// means another claim won the race in the gap between that read and
		// this write — same response either way, since from the caller's
		// side both mean exactly the same thing: somebody else got there.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "this route already has an owner",
		})
		return
	}
	if err != nil {
		s.failLookup(w, err)
		return
	}
	linked, ok := s.linkedAccounts(w)
	if !ok {
		return
	}
	identity := auth.FromContext(r.Context())
	// See handleRoutes' identical comment: this response renders the same
	// SyncState shape the library grid does, so it takes the same rule —
	// only the caller's own accounts, not a crew fellow's.
	linked = ownAccountsOnly(identity, linked)

	s.autoSyncIfEnabled(identity.User)
	writeJSON(w, http.StatusOK, s.toRouteDTO(r.Context(), route, linked, crews))
}

// routeOwner looks up one route's current owner, the same list-and-match
// mayEdit already does for its own ownership check — a second scan rather
// than threading mayEdit's result through, since mayEdit answers a
// different question (may this identity edit it) than this one (who
// currently owns it, regardless of who is asking).
func (s *Server) routeOwner(ctx context.Context, slug string) (string, error) {
	routes, _, err := s.Source.List(ctx)
	if err != nil {
		return "", err
	}
	for _, route := range routes {
		if route.Slug == slug {
			return route.Owner, nil
		}
	}
	return "", source.ErrNotFound
}

// validateCrewTargets checks a client-supplied targets list against the
// route owner's current, approved crew membership — every entry must be a
// crew the owner currently belongs to, or it is rejected at write time
// rather than silently accepted and quietly non-functional. Own devices are
// implicit and never need naming; crews are the only sharing mechanism a
// client may name here.
func validateCrewTargets(targets []string, owner string, crews crew.Snapshot) error {
	if len(targets) == 0 {
		return nil
	}
	if owner == "" {
		// Every crew's ApprovedRiders is keyed by a real rider — an empty
		// owner (an import with no --owner) belongs to none of them, so the
		// loop below would always fail here anyway. Naming that directly
		// beats "\"crew:x\" is not a crew  currently belongs to", which is
		// what owner interpolating to "" produced instead.
		return fmt.Errorf("this route has no owner, so it cannot be shared to a crew — set an owner first")
	}
	for _, t := range targets {
		if !crews.ApprovedRiders.Has(t, owner) {
			return fmt.Errorf("%q is not a crew %s currently belongs to", t, owner)
		}
	}
	return nil
}

// handleDelete removes a route from the source. It deliberately leaves sync
// state alone: the next plan will show a delete against every account that
// still holds it, which is exactly what should happen.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermEditOwn) {
		return
	}

	slug := cleanSlug(r.PathValue("slug"))
	if !s.mayEdit(w, r, slug) {
		return
	}
	if err := s.Source.Delete(r.Context(), slug); err != nil {
		s.failLookup(w, err)
		return
	}

	s.logger().Info("route deleted", "slug", slug)
	s.autoSyncIfEnabled(auth.FromContext(r.Context()).User)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- plumbing ----------

func (s *Server) toRouteDTOs(ctx context.Context, routes []model.Route, linked []model.Account, crews crew.Snapshot) []routeDTO {
	out := make([]routeDTO, 0, len(routes))
	for _, route := range routes {
		out = append(out, s.toRouteDTO(ctx, route, linked, crews))
	}
	return out
}

// stateFor reads an account's recorded state, logging and returning nothing on
// failure. Callers use this only to decorate the UI; the plan and the push read
// state properly and refuse to run when it cannot be read.
func (s *Server) stateFor(ctx context.Context, accountID string) map[string]state.Entry {
	entries, err := s.Store.ForAccount(ctx, accountID)
	if err != nil {
		s.logger().Error("could not read sync state", "account", accountID, "err", err)
		return nil
	}
	return entries
}

func (s *Server) toRouteDTO(ctx context.Context, r model.Route, linked []model.Account, crews crew.Snapshot) routeDTO {
	targetIDs := config.TargetsFor(r, linked, crews)
	statuses := make([]syncStatus, 0, len(targetIDs))
	for _, id := range targetIDs {
		entry, seen := s.stateFor(ctx, id)[r.Slug]
		switch {
		case !seen:
			statuses = append(statuses, syncStatus{AccountID: id, Status: "pending"})
		case entry.ContentHash != r.ContentHash:
			statuses = append(statuses, syncStatus{
				AccountID: id, Status: "stale",
				RemoteID: entry.RemoteID, UpdatedAt: entry.UpdatedAt,
			})
		default:
			statuses = append(statuses, syncStatus{
				AccountID: id, Status: "synced",
				RemoteID: entry.RemoteID, UpdatedAt: entry.UpdatedAt,
			})
		}
	}

	var rawTargets []string
	if r.Targets != nil {
		rawTargets = *r.Targets
	}

	return routeDTO{
		Slug:           r.Slug,
		Name:           r.Name,
		Description:    r.Description,
		Tags:           orEmpty(r.Tags),
		DistanceM:      r.Stats.DistanceM,
		AscentM:        r.Stats.AscentM,
		StartLat:       r.Stats.StartLat,
		StartLng:       r.Stats.StartLng,
		PointCount:     r.Stats.PointCount,
		ContentHash:    r.ContentHash,
		Origin:         r.Origin,
		Owner:          r.Owner,
		UpdatedAt:      r.UpdatedAt,
		Sport:          string(r.EffectiveSport()),
		Targets:        orEmpty(rawTargets),
		UnknownTargets: orEmpty(config.UnknownTargets(r, crews)),
		OwnerCrews:     ownerCrewOptions(r.Owner, crews),
		SyncState:      statuses,
	}
}

// ownerCrewOptions is every crew a rider currently, approvedly, belongs to
// — what a target picker may legally offer for a route they own.
func ownerCrewOptions(owner string, crews crew.Snapshot) []crewOptionDTO {
	out := make([]crewOptionDTO, 0, len(crews.Crews))
	for _, c := range crews.Crews {
		if crews.ApprovedRiders.Has(c.ID, owner) {
			out = append(out, crewOptionDTO{ID: c.ID, Name: c.Name})
		}
	}
	return out
}

func (s *Server) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// rateLimitConnect enforces ConnectLimiter for a Garmin/Komoot connect
// attempt, keyed by the caller's own rider identity — the abuse this stops
// is one authenticated rider using this server as a laundered
// credential-stuffing proxy against somebody else's Garmin or Komoot
// account, not brute-forcing this app's own auth. Nil ConnectLimiter (a
// test harness that never sets one) allows everything, matching every
// other optional-dependency nil check in this file.
func (s *Server) rateLimitConnect(w http.ResponseWriter, rider string) bool {
	return rateLimit(w, s.ConnectLimiter, rider, "too many sign-in attempts — wait a few minutes and try again")
}

// rateLimitAuthAction enforces AuthActionLimiter for a rider triggering
// their own Auth0-relayed self-service action — see that field's own doc
// comment for why this is a separate budget from rateLimitConnect's.
func (s *Server) rateLimitAuthAction(w http.ResponseWriter, rider string) bool {
	return rateLimit(w, s.AuthActionLimiter, rider, "too many requests — wait a few minutes and try again")
}

// rateLimit is rateLimitConnect and rateLimitAuthAction's shared check: nil
// limiter or an allowed key proceeds, anything else writes 429. A package
// function, not a method — neither caller needs anything else off Server.
func rateLimit(w http.ResponseWriter, limiter *ratelimit.Limiter, key, message string) bool {
	if limiter == nil || limiter.Allow(key) {
		return true
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": message})
	return false
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.logger().Error("request failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func (s *Server) failLookup(w http.ResponseWriter, err error) {
	if errors.Is(err, source.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.fail(w, err)
}

// isLandingHost reports whether this request arrived on the host that gets
// the logged-out page.
//
// Host only. Which *path* gets the landing page is a separate question, and
// conflating them is what let /settings on the apex serve the app: matching
// the root path here meant every other path fell through to the SPA
// fallback, and the SPA fallback is index.html.
func (s *Server) isLandingHost(r *http.Request) bool {
	if s.LandingHost == "" {
		return false
	}

	// Host can carry a port, and comparison is case-insensitive.
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(host, s.LandingHost)
}

// isPreviewHost reports whether r arrived on the blue-green preview host —
// auth.oidc.preview_redirect_url's own host, the one other host besides
// LandingHost that an anonymous visitor needs to reach the SPA shell on
// rather than be bounced away from. It has to agree with
// redirectURLForRequest's own host match: an anonymous preview-host visit
// that got redirected to LandingHost here would defeat the reason that
// second redirect_uri was registered at all — there would be no anonymous
// /sso/login on this host left to start the flow it exists to let through.
func (s *Server) isPreviewHost(r *http.Request) bool {
	cfg := s.authenticator().OIDC()
	if cfg.PreviewRedirectURL == "" {
		return false
	}
	preview, err := url.Parse(cfg.PreviewRedirectURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(r.Host, preview.Host)
}

// spaHandler serves the built frontend, falling back to index.html so client
// side routes survive a refresh.
func (s *Server) spaHandler() http.Handler {
	files := http.FileServer(http.FS(s.WebFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
		if clean == "." || clean == "" {
			clean = "index.html"
		}

		// Real files are served as themselves on either host: /assets/... is
		// shared by both pages, and the landing page has none of its own.
		//
		// A directory counts as missing. http.FileServer lists one otherwise,
		// so /assets/ answered with an index of every file in the build — on
		// the public host as well. Nothing secret is in those names, but a
		// listing is not something anyone asked this server to publish, and
		// treating a directory as "not a file" both removes it and leaves the
		// path handled by the same fallback as any other unknown one.
		info, statErr := fs.Stat(s.WebFS, clean)
		missing := errors.Is(statErr, os.ErrNotExist) || (statErr == nil && info.IsDir())

		// On the landing host every path that is not a real file is the
		// logged-out page — not just "/". The app is a different host, and
		// serving its shell here put a UI on the one address that is meant to
		// be public. Nothing behind it was reachable (every API call arrives
		// without Remote-User and is refused), but a logged-out front door
		// that renders the application is not a front door.
		landing := s.isLandingHost(r)
		if landing {
			// Fall through to the app when the file is missing, so an unbuilt
			// frontend degrades rather than 404s the front door.
			if _, err := fs.Stat(s.WebFS, "landing.html"); err != nil {
				landing = false
			}
		}

		// An anonymous visitor to the app host is sent to the front door
		// instead of the app shell. mode: proxy never had this case —
		// Traefik's forwardAuth refused the request before it reached this
		// server — and mode: none is never anonymous (LocalIdentity), so
		// this only ever fires for mode: oidc, the one mode where the app
		// itself decides who gets in. Gated on the mode explicitly, not just
		// on being anonymous, so a caller with no Auth configured (every
		// other test in this file) keeps getting the app rather than a
		// redirect to nowhere.
		//
		// Real files are exempted the same way the landing page's own
		// content is: this only replaces what would otherwise serve the SPA
		// shell, not /assets/... , which the redirect target needs too.
		//
		// The preview host is exempted too: it is neither LandingHost nor
		// the production app host, so without this check every anonymous
		// visit there — which is all of them, since its session cookie is
		// scoped to a different host — was bounced straight to LandingHost
		// before a login could ever start. See isPreviewHost.
		if !landing && s.LandingHost != "" && s.authenticator().Mode() == auth.ModeOIDC &&
			(missing || clean == "index.html") && !s.isPreviewHost(r) {
			if auth.FromContext(r.Context()).Anonymous() {
				http.Redirect(w, r, "https://"+s.LandingHost+"/", http.StatusFound)
				return
			}
		}

		switch {
		case landing && (missing || clean == "index.html"):
			r = r.Clone(r.Context())
			r.URL.Path = "/landing.html"
		case missing:
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

// mayEdit enforces ownership: a rider may change what they uploaded, an admin
// anything. It writes the error itself and reports whether to continue.
func (s *Server) mayEdit(w http.ResponseWriter, r *http.Request, slug string) bool {
	id := auth.FromContext(r.Context())
	if id.Role.Can(auth.PermEditAny) {
		return true
	}

	routes, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return false
	}

	for _, route := range routes {
		if route.Slug != slug {
			continue
		}
		if id.CanEditRoute(route.Owner) {
			return true
		}
		s.logger().Info("edit denied on another rider's route",
			"user", id.User, "slug", slug, "owner", route.Owner)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "that route belongs to " + route.Owner + "; only they or an admin can change it",
		})
		return false
	}

	// Unknown slug: let the source produce the 404 rather than leaking
	// whether it exists through the permission check.
	return true
}

// mayView is mayEdit's read-side counterpart, for the single-route handlers
// keyed by slug (handleTrack, handleDownload, handleDownloadFIT) — without
// it, hiding a route from handleRoutes' list would do nothing to stop a
// caller who already knows (or guesses) its slug from downloading the full
// track anyway.
//
// Unlike mayEdit, a route that exists but is not visible to this caller
// writes the identical 404 a genuinely nonexistent slug does ("no such
// route", the same source.ErrNotFound text failLookup already renders) —
// existence is exactly what visibility is scoped to hide here, so a 403
// confirming "that one is real, just not yours to see" would leak the one
// thing this exists to protect. mayEdit can afford a 403 because the whole
// library is listed either way; this cannot.
func (s *Server) mayView(w http.ResponseWriter, r *http.Request, slug string) bool {
	id := auth.FromContext(r.Context())
	if id.Role.Can(auth.PermEditAny) {
		return true
	}

	routes, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return false
	}
	crews, ok := s.crewSnapshot(w, r)
	if !ok {
		return false
	}

	for _, route := range routes {
		if route.Slug != slug {
			continue
		}
		if config.VisibleTo(route, id.User, crews) {
			return true
		}
		break
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such route"})
	return false
}

// visibleRoutes narrows routes down to what identity may see (config.
// VisibleTo), for the handlers that list or plan across the whole library
// rather than looking up one slug — handleRoutes and handlePlan share this
// rather than each filtering separately, so the two can never drift apart
// on what "visible" means. An admin (PermEditAny) sees everything
// unfiltered, the same bypass mayEdit and mayView already give them.
func visibleRoutes(routes []model.Route, identity auth.Identity, crews crew.Snapshot) []model.Route {
	if identity.Role.Can(auth.PermEditAny) {
		return routes
	}
	out := make([]model.Route, 0, len(routes))
	for _, rt := range routes {
		if config.VisibleTo(rt, identity.User, crews) {
			out = append(out, rt)
		}
	}
	return out
}

// visibleAccounts narrows linked to the ones identity may act on — push to,
// delete from, or plan changes against: their own, a crew fellow's, or
// everything for an admin, the same PermEditAny bypass mayEdit and mayView
// already give them elsewhere. The one definition of that boundary, shared
// by handlePlan (what pending changes they may see) and runPush/handlePush
// (what they may actually push to, including delete) — both used to trust
// every caller with PermPush, the lowest tier there is, with every account
// in the deployment.
//
// Not used by handleAccounts, which lists accounts for a rider's own
// Settings page rather than granting authority over them — see
// listableAccounts for that narrower boundary, which never bypasses for an
// admin. Managing a route someone's crew shares to needs the authority
// above; it does not need — and should not grant — a directory of who
// links which head unit deployment-wide.
func visibleAccounts(identity auth.Identity, linked []model.Account, crews crew.Snapshot) []model.Account {
	if identity.Role.Can(auth.PermEditAny) {
		return linked
	}
	return listableAccounts(identity, linked, crews)
}

// listableAccounts is visibleAccounts' narrower sibling, for handleAccounts
// alone: own account, or a crew fellow's — never everything, not even for
// an admin. Seeing who links which head unit is a different question from
// having authority to push or delete on their behalf (visibleAccounts,
// above); an admin already has that authority everywhere it actually
// matters without needing a general directory of every rider's devices.
func listableAccounts(identity auth.Identity, linked []model.Account, crews crew.Snapshot) []model.Account {
	out := make([]model.Account, 0, len(linked))
	for _, a := range linked {
		if config.AccountVisibleTo(a, identity.User, crews) {
			out = append(out, a)
		}
	}
	return out
}

// ownAccountsOnly narrows linked to identity's own — not a crew fellow's,
// not everything for an admin — everywhere a route's own SyncState is
// rendered for a caller to look at: handleRoutes (the library grid),
// handleUpload and handleUpdate (both render the same routeDTO shape right
// back at whoever just wrote the route). A route's SyncState answers "what
// would happen on my devices," not "who else in my crew has this" (that
// question belongs to a crew membership view, not a route's own detail);
// TargetsFor resolving against only this viewer's own accounts is what
// makes the display show only their own devices, whoever they are.
// handlePlan/handlePush stay on visibleAccounts — deciding what to push
// and to whom is a different question, genuinely crew-wide by nature.
func ownAccountsOnly(identity auth.Identity, linked []model.Account) []model.Account {
	out := make([]model.Account, 0, len(linked))
	for _, a := range linked {
		if strings.EqualFold(a.Rider, identity.User) {
			out = append(out, a)
		}
	}
	return out
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toPlanDTOs(items []model.PlanItem) []planItemDTO {
	out := make([]planItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, planItemDTO{
			Op:        string(item.Op),
			AccountID: item.AccountID,
			Slug:      item.Slug,
			Reason:    item.Reason,
		})
	}
	return out
}

func cleanSlug(raw string) string { return strings.Trim(raw, "/") }

// parseSport validates a client-supplied sport string, resolving an empty
// one to model.SportCycling — the same default model.RouteMeta.
// EffectiveSport applies, made explicit here so a caller never has to store
// an unresolved empty value. Anything other than "", "cycling" or "running"
// is refused rather than silently accepted and quietly wrong on every push
// (see internal/fitcourse.SportFromString and internal/wahoo's
// workoutTypeFamilyFor, which both treat anything unrecognised as cycling
// too — this is what stops a typo from reaching either of them at all).
func parseSport(raw string) (model.Sport, error) {
	switch model.Sport(raw) {
	case "", model.SportCycling:
		return model.SportCycling, nil
	case model.SportRunning:
		return model.SportRunning, nil
	default:
		return "", fmt.Errorf("unknown sport %q (want cycling or running)", raw)
	}
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("encode response", "err", err)
	}
}

// orEmpty keeps JSON arrays as [] rather than null, so the frontend never has
// to null-check a list.
func orEmpty[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debug("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// targetFactory gives the adapters what they need to push for real: a route's
// points, and a signed-in client for the account's rider.
//
// Resolved per push rather than held on the server, so a rider who reconnects
// or disconnects between pushes gets the session they have now.
func (s *Server) targetFactory() targets.Factory {
	return targets.Factory{
		Track:  s.Source.Track,
		Garmin: s.garminCourses,
		Wahoo:  s.wahooRoutes,
		// Off, matching the download's default. The cues are inferred from
		// the track's shape, not authored, and a wrong one at a junction is
		// worse than none — not something to opt a rider into silently on
		// every push. The FIT download offers ?cues=1 for anyone who wants
		// them.
		TurnCues: false,
		Log:      s.logger().Warn,
	}
}

// garminCourses resolves one rider's Garmin client from their stored sign-in.
//
// Every failure here is "this rider cannot push to Garmin", never "the push
// is broken": one account failing must leave the rest of the plan alone, which
// is what returning an error per adapter gets us.
func (s *Server) garminCourses(rider string) (targets.Courses, error) {
	if s.Garmin == nil {
		return nil, errors.New("this deployment has no Garmin sign-in configured")
	}
	if s.Links == nil || rider == "" {
		return nil, errors.New("no Garmin sign-in to push with")
	}

	_, secret, err := s.Links.Secret(garminProvider, rider)
	if err != nil {
		return nil, fmt.Errorf("%s has not connected Garmin: %w", rider, err)
	}

	var session garmin.Session
	if err := json.Unmarshal([]byte(secret), &session); err != nil {
		return nil, fmt.Errorf("the stored Garmin sign-in for %s is unreadable: %w", rider, err)
	}
	if session.Expired(time.Now()) {
		// Saying so beats a 401 from Connect that reads like an outage.
		return nil, fmt.Errorf("%s's Garmin sign-in has expired: reconnect it in Settings", rider)
	}

	consumer, _ := s.garminConsumer()
	return s.Garmin.Courses(consumer, session)
}

// distinctReasons collapses failure messages to the handful worth logging.
//
// A failing push tends to fail identically for every route — one expired
// session, one moved endpoint — so the useful line is the set of reasons, not
// thirty repetitions of it. The account and slug prefix is what makes each
// message unique, so it is dropped before comparing: what is left is the
// cause.
func distinctReasons(messages []string) []string {
	const maxReasons = 5

	seen := map[string]bool{}
	out := make([]string, 0, maxReasons)
	for _, msg := range messages {
		reason := msg
		// "<account> <slug>: <op> failed: <cause>" — keep the cause.
		if i := strings.Index(msg, " failed: "); i >= 0 {
			reason = msg[i+len(" failed: "):]
		}
		if seen[reason] {
			continue
		}
		seen[reason] = true
		if out = append(out, reason); len(out) == maxReasons {
			break
		}
	}
	return out
}
