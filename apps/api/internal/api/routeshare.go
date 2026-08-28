// Share a single route with someone who isn't a rider on this deployment
// at all — no crew, no account, nothing beyond a link.
//
// This is deliberately not another layer on top of config.VisibleTo/
// TargetsFor: every existing access-control primitive in this app is
// deployment-wide once granted (a role, crew membership), and grafting a
// one-off, single-route exception onto that machinery would mean either
// widening what "viewer" means or teaching VisibleTo about a second kind of
// grant everywhere it's checked. Instead, a share is its own narrow thing —
// internal/routeshare — with its own small set of endpoints that never
// touch the ordinary library listing at all. See authenticate's own doc
// comment in server.go for the one place this does interact with the rest
// of the auth system: the GET /api/shares/{token}... paths are carved out
// of the blanket Authorize gate, because a recipient is, by design, someone
// this deployment's role system has never heard of.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/fitcourse"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routeshare"
)

// sharesAvailable mirrors crewAvailable/scheduleAvailable — see either's own
// doc comment for why every handler checks this rather than assuming.
func (s *Server) sharesAvailable(w http.ResponseWriter) bool {
	if s.Shares != nil {
		return true
	}
	s.logger().Error("route share store unavailable — this should never happen outside tests")
	writeJSON(w, http.StatusPreconditionFailed, map[string]string{
		"error": "this deployment has no route share store configured",
	})
	return false
}

// allowedShareTTLs maps the request's ttlDays to the duration
// routeshare.Store itself validates against — kept as a lookup rather than
// a bare *24*time.Hour conversion so an unsupported number 400s with a
// clear message before ever reaching the store.
var allowedShareTTLs = map[int]time.Duration{
	7:  7 * 24 * time.Hour,
	30: 30 * 24 * time.Hour,
	90: 90 * 24 * time.Hour,
}

type shareDTO struct {
	ID         string          `json:"id"`
	RouteSlug  string          `json:"routeSlug"`
	CreatedBy  string          `json:"createdBy"`
	CreatedAt  string          `json:"createdAt"`
	ExpiresAt  string          `json:"expiresAt"`
	RevokedAt  string          `json:"revokedAt,omitempty"`
	RedeemedBy []redemptionDTO `json:"redeemedBy"`
}

type redemptionDTO struct {
	Rider      string `json:"rider"`
	RedeemedAt string `json:"redeemedAt"`
}

// createShareResponse carries the raw token and the ready-to-copy URL — the
// only response that ever will, since only Create has the raw token to
// hand. See routeshare.Store.Create's own doc comment.
type createShareResponse struct {
	shareDTO
	Token string `json:"token"`
	URL   string `json:"url"`
}

func shareDTOFor(share routeshare.Share, redemptions []routeshare.Redemption) shareDTO {
	dto := shareDTO{
		ID:         share.ID,
		RouteSlug:  share.RouteSlug,
		CreatedBy:  share.CreatedBy,
		CreatedAt:  share.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:  share.ExpiresAt.UTC().Format(time.RFC3339),
		RedeemedBy: make([]redemptionDTO, 0, len(redemptions)),
	}
	if share.RevokedAt != nil {
		dto.RevokedAt = share.RevokedAt.UTC().Format(time.RFC3339)
	}
	for _, r := range redemptions {
		dto.RedeemedBy = append(dto.RedeemedBy, redemptionDTO{
			Rider: r.Rider, RedeemedAt: r.RedeemedAt.UTC().Format(time.RFC3339),
		})
	}
	return dto
}

// routeForSharing finds slug among every route this deployment holds
// (never narrowed by config.VisibleTo — a share is created or managed by
// its owner, who can always see their own route) and checks the caller may
// edit it. Shared by every owner-side handler below.
func (s *Server) routeForSharing(w http.ResponseWriter, r *http.Request, slug string) (model.Route, bool) {
	routes, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return model.Route{}, false
	}
	for _, route := range routes {
		if route.Slug != slug {
			continue
		}
		identity := auth.FromContext(r.Context())
		if !identity.CanEditRoute(route.Owner) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "only this route's owner or an admin may manage its share links",
			})
			return model.Route{}, false
		}
		return route, true
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such route"})
	return model.Route{}, false
}

// handleCreateRouteShare issues a new share link for one route — the
// owner's own route, or any route for an admin. Requires auth.mode: oidc
// in practice (the frontend gates the button on meDTO.AuthMode), but the
// handler itself has no opinion about mode: a share created under any mode
// is just a row; only redeeming one needs a real sign-in to exist at all.
func (s *Server) handleCreateRouteShare(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermEditOwn) {
		return
	}
	if !s.sharesAvailable(w) {
		return
	}
	slug := cleanSlug(r.PathValue("slug"))
	route, ok := s.routeForSharing(w, r, slug)
	if !ok {
		return
	}

	var body struct {
		TTLDays int `json:"ttlDays"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	ttl, known := allowedShareTTLs[body.TTLDays]
	if !known {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "ttlDays must be 7, 30 or 90",
		})
		return
	}

	identity := auth.FromContext(r.Context())
	token, share, err := s.Shares.Create(r.Context(), route.Slug, identity.User, ttl)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("route share created", "slug", route.Slug, "expires", share.ExpiresAt, "by", identity.User)
	writeJSON(w, http.StatusCreated, createShareResponse{
		shareDTO: shareDTOFor(share, nil),
		Token:    token,
		URL:      requestOrigin(r) + "/shared/" + url.PathEscape(token),
	})
}

// handleListRouteShares lists every share ever created for a route — active,
// expired and revoked alike — for the owner's own manage-shares panel. Never
// includes a raw token: once issued, only its hash is ever stored, so there
// is nothing to leak even by mistake.
func (s *Server) handleListRouteShares(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermEditOwn) {
		return
	}
	if !s.sharesAvailable(w) {
		return
	}
	slug := cleanSlug(r.PathValue("slug"))
	route, ok := s.routeForSharing(w, r, slug)
	if !ok {
		return
	}

	shares, err := s.Shares.ListForRoute(r.Context(), route.Slug)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]shareDTO, 0, len(shares))
	for _, share := range shares {
		redemptions, err := s.Shares.Redemptions(r.Context(), share.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		out = append(out, shareDTOFor(share, redemptions))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevokeRouteShare ends a share immediately. id is the share's own id
// (sha256(token) hex) — safe to expose, since it is not the bearer secret a
// token is.
func (s *Server) handleRevokeRouteShare(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermEditOwn) {
		return
	}
	if !s.sharesAvailable(w) {
		return
	}

	id := r.PathValue("id")
	share, err := s.Shares.Get(r.Context(), id)
	if err != nil {
		s.failShareLookup(w, err)
		return
	}
	if _, ok := s.routeForSharing(w, r, share.RouteSlug); !ok {
		return
	}

	if err := s.Shares.Revoke(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("route share revoked", "id", id, "slug", share.RouteSlug, "by", auth.FromContext(r.Context()).User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) failShareLookup(w http.ResponseWriter, err error) {
	if errors.Is(err, routeshare.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such share link"})
		return
	}
	s.fail(w, err)
}

// sharedRouteDTO is deliberately narrow — a name, distance, ascent and
// sport, nothing a routeDTO carries about ownership, targets or sync state.
// A share grants exactly this much, never a window into the library.
type sharedRouteDTO struct {
	Slug      string  `json:"slug"`
	Name      string  `json:"name"`
	DistanceM float64 `json:"distanceM"`
	AscentM   float64 `json:"ascentM"`
	Sport     string  `json:"sport"`
	ExpiresAt string  `json:"expiresAt"`
}

// resolveShare is every GET /api/shares/{token}... handler's shared first
// step: the caller must hold a real signed-in identity (Identify still ran
// in authenticate even though Authorize was skipped for this path — see
// its own doc comment), the token must resolve to a share that is neither
// unknown/revoked nor expired, and the route it names must still exist.
// Touches the share on the way out, recording this rider's access — see
// routeshare.Store.Touch's own doc comment for why that isn't a separate
// step.
func (s *Server) resolveShare(w http.ResponseWriter, r *http.Request) (routeshare.Share, model.Route, bool) {
	if !s.sharesAvailable(w) {
		return routeshare.Share{}, model.Route{}, false
	}
	identity := auth.FromContext(r.Context())
	if identity.Anonymous() {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to view this shared route"})
		return routeshare.Share{}, model.Route{}, false
	}

	token := r.PathValue("token")
	share, err := s.Shares.Lookup(r.Context(), token)
	if err != nil {
		s.failShareLookup(w, err)
		return routeshare.Share{}, model.Route{}, false
	}
	if share.Expired(time.Now()) {
		writeJSON(w, http.StatusGone, map[string]string{"error": "this share link has expired"})
		return routeshare.Share{}, model.Route{}, false
	}

	routes, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return routeshare.Share{}, model.Route{}, false
	}
	idx := -1
	for i, route := range routes {
		if route.Slug == share.RouteSlug {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this route no longer exists"})
		return routeshare.Share{}, model.Route{}, false
	}

	if err := s.Shares.Touch(r.Context(), share.ID, identity.User); err != nil {
		// Logged, not fatal: the share is still valid and the route still
		// resolves — losing this one access-log write is not worth failing
		// the view over.
		s.logger().Warn("recording share access failed", "share", share.ID, "err", err)
	}
	return share, routes[idx], true
}

// handleSharedRoute is what a recipient's browser loads first — enough to
// render the shared-route page (name, stats) before it asks for the map or
// a download.
func (s *Server) handleSharedRoute(w http.ResponseWriter, r *http.Request) {
	share, route, ok := s.resolveShare(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sharedRouteDTO{
		Slug: route.Slug, Name: route.Name,
		DistanceM: route.Stats.DistanceM, AscentM: route.Stats.AscentM,
		Sport: string(route.EffectiveSport()), ExpiresAt: share.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleSharedRouteTrack is handleTrack's own shape, scoped through a share
// instead of library visibility — see that handler's doc comment for why
// the response is cached privately.
func (s *Server) handleSharedRouteTrack(w http.ResponseWriter, r *http.Request) {
	_, route, ok := s.resolveShare(w, r)
	if !ok {
		return
	}
	points, err := s.Source.Track(r.Context(), route.Slug)
	if err != nil {
		s.failLookup(w, err)
		return
	}
	coords := make([][2]float64, 0, len(points))
	for _, p := range points {
		coords = append(coords, [2]float64{p.Lat, p.Lon})
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	writeJSON(w, http.StatusOK, map[string]any{"slug": route.Slug, "points": coords})
}

// handleSharedRouteGPX mirrors handleDownload, scoped through a share.
func (s *Server) handleSharedRouteGPX(w http.ResponseWriter, r *http.Request) {
	_, route, ok := s.resolveShare(w, r)
	if !ok {
		return
	}
	raw, err := s.Source.GPX(r.Context(), route.Slug)
	if err != nil {
		s.failLookup(w, err)
		return
	}
	writeGPXAttachment(s.logger(), w, route.Slug, raw)
}

// handleSharedRouteFIT mirrors handleDownloadFIT, scoped through a share —
// turn cues off by default, the same as the ordinary download.
func (s *Server) handleSharedRouteFIT(w http.ResponseWriter, r *http.Request) {
	_, route, ok := s.resolveShare(w, r)
	if !ok {
		return
	}
	raw, err := s.Source.GPX(r.Context(), route.Slug)
	if err != nil {
		s.failLookup(w, err)
		return
	}
	points, err := gpx.ParsePoints(raw)
	if err != nil {
		s.fail(w, err)
		return
	}
	fitBytes, err := fitcourse.Encode(points, fitcourse.Options{
		Name:     route.Name,
		Sport:    fitcourse.SportFromString(string(route.EffectiveSport())),
		TurnCues: r.URL.Query().Get("cues") == "1",
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeFITAttachment(s.logger(), w, route.Slug, fitBytes)
}
