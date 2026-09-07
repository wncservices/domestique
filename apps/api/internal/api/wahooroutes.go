package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/fitcourse"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
	"github.com/wncservices/domestique/apps/api/internal/wahoo"
)

type wahooRouteDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	DistanceM float64 `json:"distanceM"`
	AscentM   float64 `json:"ascentM"`
	UpdatedAt string  `json:"updatedAt,omitempty"`
	// Imported is true when this exact route is already tracked as
	// something this app pushed to this account (sync_state.remote_id) —
	// the same certainty Garmin's and Komoot's own checks have.
	Imported bool `json:"imported"`
	// PossibleDuplicate names a library route that looks like the same
	// ride, when one exists. Checked two ways: an exact match on
	// route[external_id] (this app stamps every route it pushes with the
	// library slug — see wahootarget.go's prepare — so a match there is not
	// a guess), falling back to the same distance-and-start-point heuristic
	// Garmin's own possibleDuplicateOf uses for a route that reached Wahoo
	// some other way.
	PossibleDuplicate string `json:"possibleDuplicate,omitempty"`
}

type wahooRouteImportResult struct {
	Imported []string          `json:"imported"`
	Skipped  map[string]string `json:"skipped"`
}

// wahooSessionFor reads and resolves the caller's own Wahoo access token,
// refreshing it first if it has expired. ok is false whenever there is
// simply nothing to act on — not connected, an unreadable session, a
// refresh that failed — which every caller here treats the same way
// Garmin's garminSessionFor already does: an empty result, not an error
// screen.
func (s *Server) wahooSessionFor(r *http.Request) (rider, token string, ok bool) {
	if s.Wahoo == nil || s.Links == nil {
		return "", "", false
	}
	rider = auth.FromContext(r.Context()).User
	if rider == "" {
		return "", "", false
	}
	token, err := s.wahooAccessToken(r.Context(), rider)
	if err != nil {
		return "", "", false
	}
	return rider, token, true
}

// handleWahooRouteList lists what is already on the caller's Wahoo
// account, for syncing back into the library and spotting duplicates —
// distinct from handleWahooConnection, which reports the connection
// itself, not routes.
func (s *Server) handleWahooRouteList(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermWahooSync) {
		return
	}

	rider, token, ok := s.wahooSessionFor(r)
	if !ok {
		writeJSON(w, http.StatusOK, []wahooRouteDTO{})
		return
	}

	routes, err := s.Wahoo.ListRoutes(r.Context(), token)
	if err != nil {
		s.logger().Warn("wahoo route list failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Wahoo would not list the routes on this account just now.",
		})
		return
	}

	library, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	tracked := s.wahooTrackedRemoteIDs(r.Context(), rider)

	out := make([]wahooRouteDTO, 0, len(routes))
	for _, rt := range routes {
		out = append(out, wahooRouteDTO{
			ID: rt.ID, Name: rt.Name, DistanceM: rt.DistanceM, AscentM: rt.AscentM,
			UpdatedAt:         formatTime(rt.UpdatedAt),
			Imported:          tracked[rt.ID],
			PossibleDuplicate: possibleWahooDuplicateOf(rt, library),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// wahooTagPrefix marks a route as synced back from Wahoo, the same way
// garminTagPrefix and komootTagPrefix do for their own providers — dedup
// itself still runs on sync_state (wahooTrackedRemoteIDs, below), not this
// tag; it exists so the origin is visible on the route itself.
const wahooTagPrefix = "wahoo:"

func wahooTag(id string) string { return wahooTagPrefix + id }

// wahooTrackedRemoteIDs is the set of Wahoo route ids this app has already
// recorded pushing to (or pulling from) the caller's own linked account —
// the exact-match half of dedup, free of any heuristic. Same shape as
// garminTrackedRemoteIDs.
func (s *Server) wahooTrackedRemoteIDs(ctx context.Context, rider string) map[string]bool {
	out := map[string]bool{}
	entries, err := s.Store.ForAccount(ctx, accounts.ID(model.ProviderWahoo, rider))
	if err != nil {
		// Not fatal to listing: worst case some already-tracked routes show
		// up without the Imported flag, which just makes them look like
		// ordinary un-imported ones.
		s.logger().Warn("could not read wahoo sync state for dedup", "rider", rider, "err", err)
		return out
	}
	for _, e := range entries {
		if e.RemoteID != "" {
			out[e.RemoteID] = true
		}
	}
	return out
}

// possibleWahooDuplicateOf flags a library route that looks like the same
// ride as a Wahoo route — checked two ways. ExternalID first: this app
// stamps every route it pushes with route[external_id] = the library slug
// (wahootarget.go's prepare), so a match there names the exact route, not a
// guess. Falling back to distance within 2% (or 100m, whichever is more
// forgiving) and a start point within roughly 100m — the same forgiving
// comparison garmincourses.go's possibleDuplicateOf uses, for a route that
// reached Wahoo some other way (the rider's own app, a head unit) and so
// never got an external_id pointing back at the library.
func possibleWahooDuplicateOf(route wahoo.Route, routes []model.Route) string {
	if route.ExternalID != "" {
		for _, rt := range routes {
			if rt.Slug == route.ExternalID {
				return fmt.Sprintf("this is %q, already in the library", rt.Name)
			}
		}
	}

	const startPointToleranceDeg = 0.001 // roughly 100m at these latitudes
	for _, rt := range routes {
		distDelta := math.Abs(rt.Stats.DistanceM - route.DistanceM)
		distOK := distDelta <= 100 || distDelta <= rt.Stats.DistanceM*0.02
		latOK := math.Abs(rt.Stats.StartLat-route.StartLat) <= startPointToleranceDeg
		lngOK := math.Abs(rt.Stats.StartLng-route.StartLng) <= startPointToleranceDeg
		if distOK && latOK && lngOK {
			return fmt.Sprintf("looks like %q already in the library (similar distance and start point)", rt.Name)
		}
	}
	return ""
}

type wahooDuplicateGroupDTO struct {
	Name   string          `json:"name"`
	Routes []wahooRouteDTO `json:"routes"`
}

// handleWahooRouteDuplicates groups the caller's own Wahoo routes that look
// like repeated copies of each other — distinct from handleWahooRouteList's
// PossibleDuplicate, which compares a Wahoo route against the library. This
// compares Wahoo's route list against itself, the same relationship
// garmincourses.go's handleGarminCourseDuplicates has to handleGarminCourseList.
func (s *Server) handleWahooRouteDuplicates(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermWahooSync) {
		return
	}

	rider, token, ok := s.wahooSessionFor(r)
	if !ok {
		writeJSON(w, http.StatusOK, []wahooDuplicateGroupDTO{})
		return
	}

	routes, err := s.Wahoo.ListRoutes(r.Context(), token)
	if err != nil {
		s.logger().Warn("wahoo route list failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Wahoo would not list the routes on this account just now.",
		})
		return
	}

	writeJSON(w, http.StatusOK, groupDuplicateWahooRoutes(routes))
}

// groupDuplicateWahooRoutes groups routes sharing a name (case-insensitive,
// trimmed) and a distance within tolerance of each other
// (distanceWithinTolerance, shared with routeduplicates.go's,
// garmincourses.go's and komoot.go's identical problem), returning only
// groups with more than one member — same shape and same reasoning as
// groupDuplicateCourses and groupDuplicateTours.
func groupDuplicateWahooRoutes(routes []wahoo.Route) []wahooDuplicateGroupDTO {
	type group struct {
		name    string
		anchor  float64
		members []wahoo.Route
	}

	var groups []*group
	for _, rt := range routes {
		name := strings.ToLower(strings.TrimSpace(rt.Name))
		var target *group
		for _, g := range groups {
			if g.name == name && distanceWithinTolerance(g.anchor, rt.DistanceM) {
				target = g
				break
			}
		}
		if target == nil {
			target = &group{name: name, anchor: rt.DistanceM}
			groups = append(groups, target)
		}
		target.members = append(target.members, rt)
	}

	out := make([]wahooDuplicateGroupDTO, 0)
	for _, g := range groups {
		if len(g.members) < 2 {
			continue
		}
		dto := wahooDuplicateGroupDTO{Name: g.members[0].Name}
		for _, rt := range g.members {
			dto.Routes = append(dto.Routes, wahooRouteDTO{
				ID: rt.ID, Name: rt.Name, DistanceM: rt.DistanceM, AscentM: rt.AscentM,
				UpdatedAt: formatTime(rt.UpdatedAt),
			})
		}
		out = append(out, dto)
	}
	return out
}

// handleWahooRouteDelete removes one route from the caller's own Wahoo
// account — the other half of duplicate cleanup: handleWahooRouteDuplicates
// finds the groups, this removes whichever copies were picked to go.
//
// Unlike Komoot's equivalent, this calls an official, documented Wahoo
// endpoint — the very same DeleteRoute the push adapter already uses to
// remove a route it pushed — so there is no extra caution banner to show
// here the way KomootDuplicatesPanel.vue has for its own, reverse-engineered
// delete.
func (s *Server) handleWahooRouteDelete(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermWahooSync) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no route id"})
		return
	}

	rider, token, ok := s.wahooSessionFor(r)
	if !ok {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "Not connected to Wahoo — connect your account in Settings",
		})
		return
	}

	if err := s.Wahoo.DeleteRoute(r.Context(), token, id); err != nil {
		s.logger().Warn("wahoo route delete failed", "rider", rider, "route", id, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("wahoo route deleted", "rider", rider, "route", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleWahooRouteImport pulls selected Wahoo routes into the library.
func (s *Server) handleWahooRouteImport(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermWahooSync) {
		return
	}

	var body struct {
		RouteIDs []string `json:"routeIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(body.RouteIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no routeIds given"})
		return
	}

	identity := auth.FromContext(r.Context())
	rider, token, ok := s.wahooSessionFor(r)
	if !ok {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "Not connected to Wahoo — connect your account in Settings",
		})
		return
	}

	result, err := s.importWahooRoutes(r.Context(), identity.User, rider, token, body.RouteIDs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("wahoo route sync-back finished",
		"user", identity.User, "rider", rider, "imported", len(result.Imported), "skipped", len(result.Skipped))
	writeJSON(w, http.StatusOK, result)
}

// importWahooRoutes pulls the given Wahoo route ids into the library for
// rider, attributing the created routes to uploader (the caller, for a
// manual import; rider itself, for the unattended one autoImportWahoo
// runs). Split out of handleWahooRouteImport so both paths run exactly the
// same create-and-record sequence rather than a second, drifting copy of it.
func (s *Server) importWahooRoutes(ctx context.Context, uploader, rider, token string, routeIDs []string) (wahooRouteImportResult, error) {
	// Re-listed rather than trusting names the caller already had: the
	// routes' own metadata is authoritative, the same reason
	// handleGarminCourseImport and handleKomootImport re-fetch too.
	routes, err := s.Wahoo.ListRoutes(ctx, token)
	if err != nil {
		return wahooRouteImportResult{}, fmt.Errorf("wahoo would not list the routes on this account just now: %w", err)
	}
	byID := map[string]wahoo.Route{}
	for _, rt := range routes {
		byID[rt.ID] = rt
	}

	result := wahooRouteImportResult{Imported: []string{}, Skipped: map[string]string{}}

	wanted := make([]string, 0, len(routeIDs))
	for _, id := range routeIDs {
		if _, ok := byID[id]; !ok {
			result.Skipped[id] = "not on this Wahoo account"
			continue
		}
		wanted = append(wanted, id)
	}

	// Already recorded as synced — re-selecting one on purpose is how a
	// route that ended up missing its "wahoo" tag gets healed, the same
	// idea as ensureGarminTags. Split out up front so the download step
	// below only fetches FIT files for routes actually going to be created.
	tracked, err := s.Store.ForAccount(ctx, accounts.ID(model.ProviderWahoo, rider))
	if err != nil {
		s.logger().Warn("could not read wahoo sync state for healing", "rider", rider, "err", err)
	}
	trackedByRemoteID := map[string]state.Entry{}
	for _, e := range tracked {
		if e.RemoteID != "" {
			trackedByRemoteID[e.RemoteID] = e
		}
	}
	libraryRoutes, _, err := s.Source.List(ctx)
	if err != nil {
		return wahooRouteImportResult{}, err
	}
	routesBySlug := map[string]model.Route{}
	for _, rt := range libraryRoutes {
		routesBySlug[rt.Slug] = rt
	}

	var toDownload []string
	for _, id := range wanted {
		if _, already := trackedByRemoteID[id]; !already {
			toDownload = append(toDownload, id)
		}
	}

	// Downloaded a few at a time, same reasoning and the same bound as
	// Garmin's and Komoot's own imports.
	const parallel = 4
	downloads := fetchWahooRoutes(ctx, s.Wahoo, token, byID, toDownload, parallel)

	for _, id := range wanted {
		route := byID[id]

		if entry, already := trackedByRemoteID[id]; already {
			libraryRoute, found := routesBySlug[entry.Slug]
			if !found {
				result.Skipped[id] = "already tracked, but the route it points to no longer exists"
				continue
			}
			if err := s.ensureWahooTags(ctx, libraryRoute, id); err != nil {
				result.Skipped[id] = err.Error()
				continue
			}
			result.Imported = append(result.Imported, id)
			continue
		}

		got := downloads[id]
		if got.err != nil {
			result.Skipped[id] = got.err.Error()
			continue
		}

		created, err := s.Source.Create(ctx, source.CreateRequest{
			Filename: route.Name + ".gpx",
			Name:     route.Name,
			// No Descript: the "wahoo" tag below already says where this
			// came from, the same call garmincourses.go and komoot.go make.
			Tags:       []string{"wahoo", wahooTag(id)},
			UploadedBy: uploader,
			GPX:        got.gpx,
		})
		if err != nil {
			result.Skipped[id] = err.Error()
			continue
		}

		// The route just came FROM this Wahoo account — record that as
		// already synced, or the very next plan sees "targets this
		// account, no sync state" and offers to push right back what was
		// just pulled down. RemoteID is the route id it came from, so a
		// later push recognises it as already there instead of creating a
		// second copy.
		if err := s.Store.Record(ctx, state.Entry{
			AccountID:   accounts.ID(model.ProviderWahoo, rider),
			Slug:        created.Slug,
			RemoteID:    id,
			ContentHash: created.ContentHash,
			Name:        created.Name,
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			// The import itself already succeeded — losing this record is
			// not worth undoing that over. Worst case is one extra push
			// offered next time, recoverable, unlike losing the route.
			s.logger().Warn("recording wahoo sync state after import failed",
				"rider", rider, "route", id, "err", err)
		}

		result.Imported = append(result.Imported, id)
	}

	return result, nil
}

// ensureWahooTags backfills the "wahoo"/"wahoo:<id>" tags onto a route that
// sync_state already records as coming from this exact Wahoo route, but
// which is somehow missing them — the self-healing half of re-selecting an
// already-imported route, rather than creating a duplicate. A no-op when
// both are already present. Same shape as garmincourses.go's ensureGarminTags.
func (s *Server) ensureWahooTags(ctx context.Context, route model.Route, routeID string) error {
	want := []string{"wahoo", wahooTag(routeID)}
	have := make(map[string]bool, len(route.Tags))
	for _, t := range route.Tags {
		have[t] = true
	}

	// Copied rather than appended-to-in-place: route.Tags may share its
	// backing array with a cached copy elsewhere, and append can grow into
	// unused capacity in that same array rather than always allocating a
	// fresh one — see ensureGarminTags's identical comment.
	newTags := append([]string{}, route.Tags...)
	changed := false
	for _, t := range want {
		if !have[t] {
			newTags = append(newTags, t)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	_, err := s.Source.Update(ctx, route.Slug, source.UpdateRequest{Tags: &newTags})
	return err
}

type wahooRouteDownload struct {
	gpx []byte
	err error
}

// WahooDownloader is the slice of *wahoo.Client fetchWahooRoutes needs — an
// interface so tests can substitute a fake, same reasoning as
// KomootImporter and GarminConnector.
type WahooDownloader interface {
	DownloadRoute(ctx context.Context, accessToken, fileURL string) ([]byte, error)
}

// fetchWahooRoutes downloads and decodes several routes' FIT files at once,
// bounded by parallel — same shape as garmincourses.go's fetchGPX and
// komoot.go's fetchTours. Decoding (FIT bytes -> points -> a GPX
// source.CreateRequest can use) happens here too, not in the caller's loop,
// for the same reason it is exactly as slow and exactly as parallel-safe as
// the download itself.
func fetchWahooRoutes(ctx context.Context, client WahooDownloader, token string, routesByID map[string]wahoo.Route, ids []string, parallel int) map[string]wahooRouteDownload {
	out := make(map[string]wahooRouteDownload, len(ids))
	if len(ids) == 0 {
		return out
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)

	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			gpxBytes, err := downloadAndRenderWahooRoute(ctx, client, token, routesByID[id])

			mu.Lock()
			out[id] = wahooRouteDownload{gpx: gpxBytes, err: err}
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return out
}

func downloadAndRenderWahooRoute(ctx context.Context, client WahooDownloader, token string, route wahoo.Route) ([]byte, error) {
	fit, err := client.DownloadRoute(ctx, token, route.FileURL)
	if err != nil {
		return nil, fmt.Errorf("downloading the course file: %w", err)
	}
	points, err := fitcourse.Decode(fit)
	if err != nil {
		return nil, fmt.Errorf("reading the course file: %w", err)
	}
	rendered, err := gpx.Render(route.Name, points, nil)
	if err != nil {
		return nil, fmt.Errorf("rendering the track: %w", err)
	}
	return rendered, nil
}
