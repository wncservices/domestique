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
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/state"
)

type garminCourseDTO struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	DistanceM    float64 `json:"distanceM"`
	AscentM      float64 `json:"ascentM"`
	ActivityType string  `json:"activityType"`
	CreatedAt    string  `json:"createdAt,omitempty"`
	// Imported is true when this exact course is already tracked as
	// something this app pushed to this account (sync_state.remote_id) —
	// the same certainty Komoot's tag-based check has.
	Imported bool `json:"imported"`
	// PossibleDuplicate names a library route that looks like the same ride
	// by distance and start point, when one exists. A hint, not a
	// certainty: Garmin re-encodes track points its own way, so an exact
	// content match is not realistic here the way Komoot's tag check is —
	// the rider decides, this only flags it worth a look.
	PossibleDuplicate string `json:"possibleDuplicate,omitempty"`
}

type garminCourseImportResult struct {
	Imported []string          `json:"imported"`
	Skipped  map[string]string `json:"skipped"`
}

// garminSessionFor reads and decodes the caller's own stored Garmin session.
// ok is false whenever there is simply nothing to act on — not connected, an
// unreadable session — which every caller here treats the same way Devices
// already does: an empty result, not an error screen.
func (s *Server) garminSessionFor(r *http.Request) (rider string, session garmin.Session, ok bool) {
	rider = auth.FromContext(r.Context()).User
	if rider == "" {
		return "", garmin.Session{}, false
	}
	session, ok = s.garminSessionForRider(rider)
	return rider, session, ok
}

// garminSessionForRider is garminSessionFor without an *http.Request behind
// it — the auto-import poller has no request, just a rider name it read
// from accounts.Store.
func (s *Server) garminSessionForRider(rider string) (garmin.Session, bool) {
	if s.Garmin == nil || s.Links == nil || rider == "" {
		return garmin.Session{}, false
	}
	_, secret, err := s.Links.Secret(garminProvider, rider)
	if err != nil {
		return garmin.Session{}, false
	}
	var session garmin.Session
	if err := json.Unmarshal([]byte(secret), &session); err != nil {
		s.logger().Warn("stored garmin session is unreadable", "rider", rider, "err", err)
		return garmin.Session{}, false
	}
	return session, true
}

// handleGarminCourseList lists what is already on the caller's Garmin
// account, for syncing back into the library and spotting duplicates —
// distinct from handleGarminDevices, which lists head units, not routes.
func (s *Server) handleGarminCourseList(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermGarminSync) {
		return
	}

	rider, session, ok := s.garminSessionFor(r)
	if !ok {
		writeJSON(w, http.StatusOK, []garminCourseDTO{})
		return
	}

	consumer, _ := s.garminConsumer()
	courses, err := s.Garmin.ListCourses(r.Context(), consumer, session)
	if err != nil {
		s.logger().Warn("garmin course list failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Garmin would not list the courses on this account just now.",
		})
		return
	}
	courses = s.dropPacingCourses(r.Context(), rider, courses)

	routes, _, err := s.Source.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	tracked := s.garminTrackedRemoteIDs(r.Context(), rider)

	out := make([]garminCourseDTO, 0, len(courses))
	for _, c := range courses {
		out = append(out, garminCourseDTO{
			ID: c.ID, Name: c.Name, DistanceM: c.DistanceM, AscentM: c.AscentM,
			ActivityType:      c.ActivityType,
			CreatedAt:         formatTime(c.CreatedAt),
			Imported:          tracked[c.ID],
			PossibleDuplicate: possibleDuplicateOf(c, routes),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// garminTagPrefix marks a route as synced back from Garmin, the same way
// komootTagPrefix does for Komoot — dedup itself still runs on sync_state
// (garminTrackedRemoteIDs, below), not this tag; it exists so the origin is
// visible on the route itself, in the library and not just in the account
// it happened to come from.
const garminTagPrefix = "garmin:"

func garminTag(id string) string { return garminTagPrefix + id }

// garminTrackedRemoteIDs is the set of Garmin course ids this app has
// already recorded pushing to the caller's own linked account — the
// exact-match half of dedup, free of any heuristic.
func (s *Server) garminTrackedRemoteIDs(ctx context.Context, rider string) map[string]bool {
	out := map[string]bool{}
	entries, err := s.Store.ForAccount(ctx, accounts.ID(model.ProviderGarmin, rider))
	if err != nil {
		// Not fatal to listing: worst case some already-tracked courses show
		// up without the Imported flag, which just makes them look like
		// ordinary un-imported ones.
		s.logger().Warn("could not read garmin sync state for dedup", "rider", rider, "err", err)
		return out
	}
	for _, e := range entries {
		if e.RemoteID != "" {
			out[e.RemoteID] = true
		}
	}
	return out
}

// possibleDuplicateOf flags a library route that looks like the same ride as
// a Garmin course — distance within 2% (or 100m, whichever is more
// forgiving) and a start point within roughly 100m. Both loose on purpose:
// this is a hint for a rider to glance at, not a filter that silently hides
// anything, and Garmin's own re-encoding of a track's points means an exact
// match is never the realistic bar here.
func possibleDuplicateOf(course garmin.Course, routes []model.Route) string {
	const startPointToleranceDeg = 0.001 // roughly 100m at these latitudes
	for _, route := range routes {
		distDelta := math.Abs(route.Stats.DistanceM - course.DistanceM)
		distOK := distDelta <= 100 || distDelta <= route.Stats.DistanceM*0.02
		latOK := math.Abs(route.Stats.StartLat-course.StartLat) <= startPointToleranceDeg
		lngOK := math.Abs(route.Stats.StartLng-course.StartLng) <= startPointToleranceDeg
		if distOK && latOK && lngOK {
			return fmt.Sprintf("looks like %q already in the library (similar distance and start point)", route.Name)
		}
	}
	return ""
}

type garminDuplicateGroupDTO struct {
	Name    string            `json:"name"`
	Courses []garminCourseDTO `json:"courses"`
}

// handleGarminCourseDuplicates groups the caller's own Garmin courses that
// look like repeated copies of each other — distinct from
// handleGarminCourseList's PossibleDuplicate, which compares a Garmin course
// against the library. This compares Garmin's course list against itself:
// the shape a rider actually hits after the same route gets pushed more than
// once (an identity split re-creating an account Garmin already had a course
// from, for instance) — nothing here reads the library at all.
func (s *Server) handleGarminCourseDuplicates(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermGarminSync) {
		return
	}

	rider, session, ok := s.garminSessionFor(r)
	if !ok {
		writeJSON(w, http.StatusOK, []garminDuplicateGroupDTO{})
		return
	}

	consumer, _ := s.garminConsumer()
	courses, err := s.Garmin.ListCourses(r.Context(), consumer, session)
	if err != nil {
		s.logger().Warn("garmin course list failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Garmin would not list the courses on this account just now.",
		})
		return
	}

	writeJSON(w, http.StatusOK, groupDuplicateCourses(s.dropPacingCourses(r.Context(), rider, courses)))
}

// groupDuplicateCourses groups courses that are very likely repeated copies
// of the same real course — the same name (case-insensitive, trimmed) and a
// distance within tolerance of each other (distanceWithinTolerance, shared
// with routeduplicates.go's identical problem on the library side) — and
// returns only the groups with more than one course. A course with no
// repeats is not a duplicate of anything, so it is left out entirely rather
// than returned as a group of one.
//
// Name and distance, not the start-point-and-distance heuristic
// possibleDuplicateOf uses against the library: two Garmin courses created
// from the same GPX at different times can carry slightly different ascent
// (Garmin recomputes elevation gain per upload, not stored with the file),
// but the same name and the same distance, since this deployment names a
// pushed course after the route it came from and a GPX's own distance does
// not change between uploads.
func groupDuplicateCourses(courses []garmin.Course) []garminDuplicateGroupDTO {
	type group struct {
		name string
		// anchor is the first course's distance seen for this group — every
		// later candidate is compared against it directly, not against an
		// independently-rounded bucket of its own. Two courses 50m apart can
		// round to different buckets if they straddle a rounding boundary
		// (42000 and 42050 do, at a 100m tolerance) even though 50m is well
		// inside the tolerance either one of them would pass on its own —
		// comparing directly against the group's own anchor has no boundary
		// to straddle.
		anchor  float64
		members []garmin.Course
	}

	var groups []*group
	for _, c := range courses {
		name := strings.ToLower(strings.TrimSpace(c.Name))
		var target *group
		for _, g := range groups {
			if g.name == name && distanceWithinTolerance(g.anchor, c.DistanceM) {
				target = g
				break
			}
		}
		if target == nil {
			target = &group{name: name, anchor: c.DistanceM}
			groups = append(groups, target)
		}
		target.members = append(target.members, c)
	}

	out := make([]garminDuplicateGroupDTO, 0)
	for _, g := range groups {
		if len(g.members) < 2 {
			continue
		}
		dto := garminDuplicateGroupDTO{Name: g.members[0].Name}
		for _, c := range g.members {
			dto.Courses = append(dto.Courses, garminCourseDTO{
				ID: c.ID, Name: c.Name, DistanceM: c.DistanceM, AscentM: c.AscentM,
				ActivityType: c.ActivityType,
				CreatedAt:    formatTime(c.CreatedAt),
			})
		}
		out = append(out, dto)
	}
	return out
}

// handleGarminCourseDelete removes one course from the caller's own Garmin
// account — the other half of duplicate cleanup: handleGarminCourseDuplicates
// finds the groups, this removes whichever copies were picked to go. Nothing
// here checks that the id actually came from a duplicate group: the rider is
// deleting from their own real Garmin account, using their own Garmin
// session, the same trust already given to unlinking an account or importing
// a course.
func (s *Server) handleGarminCourseDelete(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermGarminSync) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no course id"})
		return
	}

	rider, session, ok := s.garminSessionFor(r)
	if !ok {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "Not connected to Garmin — connect your account in Settings",
		})
		return
	}
	consumer, _ := s.garminConsumer()
	courses, err := s.Garmin.Courses(consumer, session)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if err := courses.DeleteCourse(r.Context(), id); err != nil {
		s.logger().Warn("garmin course delete failed", "rider", rider, "course", id, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("garmin course deleted", "rider", rider, "course", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleGarminCourseImport pulls selected Garmin courses into the library.
func (s *Server) handleGarminCourseImport(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermGarminSync) {
		return
	}

	var body struct {
		CourseIDs []string `json:"courseIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(body.CourseIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no courseIds given"})
		return
	}

	identity := auth.FromContext(r.Context())
	rider, session, ok := s.garminSessionFor(r)
	if !ok {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "Not connected to Garmin — connect your account in Settings",
		})
		return
	}

	result, err := s.importGarminCourses(r.Context(), identity.User, rider, session, body.CourseIDs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("garmin course sync-back finished",
		"user", identity.User, "rider", rider, "imported", len(result.Imported), "skipped", len(result.Skipped))
	writeJSON(w, http.StatusOK, result)
}

// importGarminCourses pulls the given Garmin course ids into the library for
// rider, attributing the created routes to uploader (the caller, for a
// manual import; rider itself, for the unattended one autoImportGarmin
// runs). Split out of handleGarminCourseImport so both paths run exactly
// the same create-and-record sequence rather than a second, drifting copy
// of it.
func (s *Server) importGarminCourses(ctx context.Context, uploader, rider string, session garmin.Session, courseIDs []string) (garminCourseImportResult, error) {
	consumer, _ := s.garminConsumer()

	// Re-listed rather than trusting names the caller already had: the
	// courses' own names are the authoritative ones, the same reason
	// handleKomootImport re-fetches tours rather than trusting the client.
	courses, err := s.Garmin.ListCourses(ctx, consumer, session)
	if err != nil {
		return garminCourseImportResult{}, fmt.Errorf("garmin would not list the courses on this account just now: %w", err)
	}
	courses = s.dropPacingCourses(ctx, rider, courses)
	byID := map[string]garmin.Course{}
	for _, c := range courses {
		byID[c.ID] = c
	}

	result := garminCourseImportResult{Imported: []string{}, Skipped: map[string]string{}}

	wanted := make([]string, 0, len(courseIDs))
	for _, id := range courseIDs {
		if _, ok := byID[id]; !ok {
			result.Skipped[id] = "not on this Garmin account"
			continue
		}
		wanted = append(wanted, id)
	}

	// Already recorded as synced from these courses — the frontend normally
	// will not even offer them for selection (the checkbox is disabled), but
	// re-selecting one on purpose is exactly how a rider heals a route that
	// somehow ended up without its "garmin" tag: see ensureGarminTags. Split
	// out up front so the download step below only fetches GPX for courses
	// that are actually going to be created.
	tracked, err := s.Store.ForAccount(ctx, accounts.ID(model.ProviderGarmin, rider))
	if err != nil {
		s.logger().Warn("could not read garmin sync state for healing", "rider", rider, "err", err)
	}
	trackedByRemoteID := map[string]state.Entry{}
	for _, e := range tracked {
		if e.RemoteID != "" {
			trackedByRemoteID[e.RemoteID] = e
		}
	}
	routes, _, err := s.Source.List(ctx)
	if err != nil {
		return garminCourseImportResult{}, err
	}
	routesBySlug := map[string]model.Route{}
	for _, rt := range routes {
		routesBySlug[rt.Slug] = rt
	}

	var toDownload []string
	for _, id := range wanted {
		if _, already := trackedByRemoteID[id]; !already {
			toDownload = append(toDownload, id)
		}
	}

	// Downloaded a few at a time, same reasoning and the same bound as
	// Komoot's import: sequential would mean thirty waits end to end with
	// no response byte written until the last one finished, and this is
	// somebody's personal account on an undocumented API, not a service to
	// saturate.
	const parallel = 4
	downloads := fetchGPX(ctx, s.Garmin, consumer, session, toDownload, parallel)

	for _, id := range wanted {
		course := byID[id]

		if entry, already := trackedByRemoteID[id]; already {
			route, found := routesBySlug[entry.Slug]
			if !found {
				result.Skipped[id] = "already tracked, but the route it points to no longer exists"
				continue
			}
			if err := s.ensureGarminTags(ctx, route, id); err != nil {
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

		route, err := s.Source.Create(ctx, source.CreateRequest{
			Filename: course.Name + ".gpx",
			Name:     course.Name,
			// No Descript: the "garmin" tag below already says where this
			// came from — a redundant "Synced back from Garmin (course
			// ...)" sentence in the description field just crowds the card.
			Tags:       []string{"garmin", garminTag(id)},
			UploadedBy: uploader,
			GPX:        got.gpx,
		})
		if err != nil {
			result.Skipped[id] = err.Error()
			continue
		}

		// The route just came FROM this Garmin account — record that as
		// already synced, or the very next plan sees "targets this account,
		// no sync state" and offers to push right back what was just pulled
		// down. RemoteID is the course id it came from, so a later push
		// recognises it as already there instead of creating a second copy.
		if err := s.Store.Record(ctx, state.Entry{
			AccountID:   accounts.ID(model.ProviderGarmin, rider),
			Slug:        route.Slug,
			RemoteID:    id,
			ContentHash: route.ContentHash,
			Name:        route.Name,
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			// The import itself already succeeded — losing this record is
			// not worth undoing that over. Worst case is one extra push
			// offered next time, recoverable, unlike losing the route.
			s.logger().Warn("recording garmin sync state after import failed",
				"rider", rider, "course", id, "err", err)
		}

		result.Imported = append(result.Imported, id)
	}

	return result, nil
}

// ensureGarminTags backfills the "garmin"/"garmin:<id>" tags onto a route
// that sync_state already records as coming from this exact course, but
// which is somehow missing them — the self-healing half of re-selecting an
// already-imported course, rather than creating a duplicate route. Every
// current write path sets both tags at creation (see this file's own
// tests), so this only ever has anything to do for a route whose tags
// predate that, or lost them some other way outside this app's own code.
// A no-op when both are already present.
func (s *Server) ensureGarminTags(ctx context.Context, route model.Route, courseID string) error {
	want := []string{"garmin", garminTag(courseID)}
	have := make(map[string]bool, len(route.Tags))
	for _, t := range route.Tags {
		have[t] = true
	}

	// Copied rather than appended-to-in-place: route.Tags may share its
	// backing array with a cached copy elsewhere (routesBySlug, in the
	// handler above), and append can grow into unused capacity in that
	// same array rather than always allocating a fresh one.
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

type gpxDownload struct {
	gpx []byte
	err error
}

// fetchGPX downloads several courses at once, bounded by parallel — same
// shape as komoot.go's fetchTours.
func fetchGPX(ctx context.Context, connector GarminConnector, consumer GarminConsumer, session garmin.Session, ids []string, parallel int) map[string]gpxDownload {
	out := make(map[string]gpxDownload, len(ids))
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

			gpx, err := connector.DownloadGPX(ctx, consumer, session, id)

			mu.Lock()
			out[id] = gpxDownload{gpx: gpx, err: err}
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return out
}
