package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/komoot"
	"github.com/wncservices/domestique/apps/api/internal/secrets"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// KomootImporter is the slice of the Komoot client this package needs. An
// interface so tests can substitute a fake, and so a broken third-party API
// stays contained behind one seam.
type KomootImporter interface {
	Tours(ctx context.Context, includeRecorded bool) ([]komoot.Tour, error)
	GPX(ctx context.Context, tourID string) ([]byte, error)
	DeleteTour(ctx context.Context, tourID string) error
}

type komootTourDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Sport     string  `json:"sport"`
	DistanceM float64 `json:"distanceM"`
	AscentM   float64 `json:"ascentM"`
	ChangedAt string  `json:"changedAt,omitempty"`
	// Imported reports whether a route with this Komoot id is already here.
	Imported bool `json:"imported"`
}

type komootImportResult struct {
	Imported []string          `json:"imported"`
	Skipped  map[string]string `json:"skipped"`
}

// handleKomootTours lists what could be imported.
func (s *Server) handleKomootTours(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermKomootSync) {
		return
	}
	client := s.komootFor(r)
	if client == nil {
		s.komootDisabled(w)
		return
	}

	tours, err := client.Tours(r.Context(), s.Config.Komoot.IncludeRecorded)
	if err != nil {
		// Komoot's API is undocumented and moves; surface it as an upstream
		// problem rather than a fault in this app.
		s.logger().Warn("komoot tour listing failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	existing := s.komootTagIndex(r.Context())
	out := make([]komootTourDTO, 0, len(tours))
	for _, t := range tours {
		out = append(out, komootTourDTO{
			ID:        t.ID,
			Name:      t.Name,
			Sport:     t.Sport,
			DistanceM: t.DistanceM,
			AscentM:   t.AscentM,
			ChangedAt: formatTime(t.ChangedAt),
			Imported:  existing[t.ID],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type komootDuplicateGroupDTO struct {
	Name  string          `json:"name"`
	Tours []komootTourDTO `json:"tours"`
}

// handleKomootDuplicates groups the caller's own planned Komoot tours that
// look like repeated copies of each other — the shape a rider hits after
// planning the same route twice on Komoot itself, before either copy ever
// reaches this app. Distinct from any check against the library: this
// compares Komoot's own tour list against itself, the same relationship
// garmincourses.go's handleGarminCourseDuplicates has to handleGarminCourseList.
//
// Recorded rides are never considered: Tours(ctx, false) already filters
// them out, so groupDuplicateTours never sees one, and
// handleKomootTourDelete re-checks that same filtered list before deleting
// anything — a recorded ride can never be offered or removed here.
func (s *Server) handleKomootDuplicates(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermKomootSync) {
		return
	}
	client := s.komootFor(r)
	if client == nil {
		s.komootDisabled(w)
		return
	}

	tours, err := client.Tours(r.Context(), false)
	if err != nil {
		s.logger().Warn("komoot tour listing failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, groupDuplicateTours(tours))
}

// groupDuplicateTours groups tours sharing a name (case-insensitive,
// trimmed) and a distance within tolerance of each other
// (distanceWithinTolerance, shared with routeduplicates.go's and
// garmincourses.go's identical problem), returning only groups with more
// than one member — same shape and same reasoning as garmincourses.go's
// groupDuplicateCourses.
func groupDuplicateTours(tours []komoot.Tour) []komootDuplicateGroupDTO {
	type group struct {
		name    string
		anchor  float64
		members []komoot.Tour
	}

	var groups []*group
	for _, t := range tours {
		name := strings.ToLower(strings.TrimSpace(t.Name))
		var target *group
		for _, g := range groups {
			if g.name == name && distanceWithinTolerance(g.anchor, t.DistanceM) {
				target = g
				break
			}
		}
		if target == nil {
			target = &group{name: name, anchor: t.DistanceM}
			groups = append(groups, target)
		}
		target.members = append(target.members, t)
	}

	out := make([]komootDuplicateGroupDTO, 0)
	for _, g := range groups {
		if len(g.members) < 2 {
			continue
		}
		dto := komootDuplicateGroupDTO{Name: g.members[0].Name}
		for _, t := range g.members {
			dto.Tours = append(dto.Tours, komootTourDTO{
				ID: t.ID, Name: t.Name, Sport: t.Sport,
				DistanceM: t.DistanceM, AscentM: t.AscentM,
				ChangedAt: formatTime(t.ChangedAt),
			})
		}
		out = append(out, dto)
	}
	return out
}

// handleKomootTourDelete removes one tour from the caller's own Komoot
// account — the other half of duplicate cleanup: handleKomootDuplicates
// finds the groups, this removes whichever copies were picked to go.
//
// Re-lists the account's planned tours first and refuses an id that is not
// on that list, rather than trusting the URL — the same "re-fetch, don't
// trust the client" rule handleKomootImport already follows, and the thing
// that keeps a recorded ride's id from ever reaching DeleteTour: Tours(ctx,
// false) never returns one, so it can never pass this check.
func (s *Server) handleKomootTourDelete(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermKomootSync) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no tour id"})
		return
	}

	// Deliberately the caller's own connection only, never the
	// deployment-wide shared account komootFor would fall back to — see
	// komootOwnConnectionFor's own doc comment for why delete is different
	// from listing and importing here.
	client := s.komootOwnConnectionFor(r)
	if client == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "Not signed in to Komoot — connect your account in Settings",
		})
		return
	}

	tours, err := client.Tours(r.Context(), false)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	found := false
	for _, t := range tours {
		if t.ID == id {
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "not a planned tour on this Komoot account",
		})
		return
	}

	if err := client.DeleteTour(r.Context(), id); err != nil {
		s.logger().Warn("komoot tour delete failed", "tour", id, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("komoot tour deleted", "user", auth.FromContext(r.Context()).User, "tour", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleKomootImport pulls selected tours into the library.
func (s *Server) handleKomootImport(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermKomootSync) {
		return
	}
	client := s.komootFor(r)
	if client == nil {
		s.komootDisabled(w)
		return
	}

	var body struct {
		TourIDs []string `json:"tourIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(body.TourIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "no tourIds given",
		})
		return
	}

	identity := auth.FromContext(r.Context())
	result, err := s.importKomootTours(r.Context(), identity.User, client, body.TourIDs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("komoot import finished",
		"user", identity.User, "imported", len(result.Imported), "skipped", len(result.Skipped))
	writeJSON(w, http.StatusOK, result)
}

// importKomootTours pulls the given Komoot tour ids into the library,
// attributing the created routes to uploader (the caller, for a manual
// import; the connected rider, for the unattended one autoImportKomoot
// runs). Split out of handleKomootImport so both paths run exactly the same
// create sequence rather than a second, drifting copy of it.
func (s *Server) importKomootTours(ctx context.Context, uploader string, client KomootImporter, tourIDs []string) (komootImportResult, error) {
	tours, err := client.Tours(ctx, s.Config.Komoot.IncludeRecorded)
	if err != nil {
		return komootImportResult{}, err
	}
	byID := map[string]komoot.Tour{}
	for _, t := range tours {
		byID[t.ID] = t
	}

	existing := s.komootTagIndex(ctx)
	result := komootImportResult{Imported: []string{}, Skipped: map[string]string{}}

	// Decide what to fetch before fetching anything, so the slow part is a
	// single pass with nothing else interleaved.
	wanted := make([]string, 0, len(tourIDs))
	for _, id := range tourIDs {
		switch {
		case !contains(byID, id):
			result.Skipped[id] = "not in this Komoot account"
		case existing[id]:
			// Re-importing would create a duplicate route, and the rider
			// would have to work out which their device is following.
			result.Skipped[id] = "already imported"
		default:
			wanted = append(wanted, id)
		}
	}

	// One round trip to Komoot per tour, and they were sequential: thirty
	// tours meant thirty waits end to end, with no response byte written
	// until the last one finished. Browsers give up on that. Fetching a few
	// at a time turns it into a handful of waits.
	//
	// Small on purpose — this is somebody's personal account on an
	// undocumented API, not a service to saturate.
	const parallel = 4
	downloads := fetchTours(ctx, client, wanted, parallel)

	for _, id := range wanted {
		got := downloads[id]
		if got.err != nil {
			// One bad tour must not abandon the rest of the batch.
			result.Skipped[id] = got.err.Error()
			continue
		}
		tour := byID[id]
		raw := got.gpx

		if _, err := s.Source.Create(ctx, source.CreateRequest{
			Filename: tour.Name + ".gpx",
			Name:     tour.Name,
			// No Descript: the "komoot" tag below already says where this
			// came from — a redundant "Imported from Komoot (tour ...)"
			// sentence in the description field just crowds the card.
			Tags:       []string{"komoot", komootTag(id)},
			UploadedBy: uploader,
			GPX:        raw,
		}); err != nil {
			result.Skipped[id] = err.Error()
			continue
		}
		result.Imported = append(result.Imported, id)
	}

	return result, nil
}

func contains(byID map[string]komoot.Tour, id string) bool {
	_, ok := byID[id]
	return ok
}

type tourDownload struct {
	gpx []byte
	err error
}

// fetchTours downloads several tours at once, bounded by parallel.
//
// Order is irrelevant here — the caller walks its own list afterwards — but
// the results map must be complete, so every id gets an entry even when the
// fetch failed.
func fetchTours(ctx context.Context, client KomootImporter, ids []string, parallel int) map[string]tourDownload {
	out := make(map[string]tourDownload, len(ids))
	if len(ids) == 0 {
		return out
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)

	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			gpx, err := client.GPX(ctx, id)

			mu.Lock()
			out[id] = tourDownload{gpx: gpx, err: err}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// komootTagIndex maps Komoot tour ids already in the library.
//
// The id is carried as a tag rather than a column so the fs source works the
// same way — a route.yaml can carry `komoot:12345` just as well.
func (s *Server) komootTagIndex(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	routes, _, err := s.Source.List(ctx)
	if err != nil {
		s.logger().Warn("could not index existing komoot imports", "err", err)
		return out
	}
	for _, route := range routes {
		for _, tag := range route.Tags {
			if id, ok := parseKomootTag(tag); ok {
				out[id] = true
			}
		}
	}
	return out
}

const komootTagPrefix = "komoot:"

func komootTag(id string) string { return komootTagPrefix + id }

func parseKomootTag(tag string) (string, bool) {
	if len(tag) > len(komootTagPrefix) && tag[:len(komootTagPrefix)] == komootTagPrefix {
		return tag[len(komootTagPrefix):], true
	}
	return "", false
}

// komootDisabled explains why there is no client for this rider.
//
// Three different situations reach here and they need different answers. The
// message used to name KOMOOT_EMAIL and KOMOOT_PASSWORD in all of them, which
// was true when one deployment-wide account did every import. Riders now sign
// in from Settings, so that advice sends someone to edit a Deployment for a
// problem they can fix in the UI in ten seconds — and on a multi-rider
// deployment there is no environment answer at all, because the credentials
// are per rider.
func (s *Server) komootDisabled(w http.ResponseWriter) {
	switch {
	case !s.KomootEnabled:
		s.logger().Warn("komoot requested but not enabled for this deployment")
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "Komoot import is not enabled for this deployment — set komoot.enabled",
		})
	case s.Links.CanStore():
		// The rider can fix this themselves, so this is not a server-side
		// gap: nothing is missing except a sign-in that has not happened yet.
		// Not logged for the same reason: it is a routine per-rider state,
		// not a deployment health signal.
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "Not signed in to Komoot — connect your account in Settings",
		})
	default:
		// No encryption key, so the store refuses to hold a sign-in and the
		// UI route is closed. The environment is genuinely the only way in.
		s.logger().Warn("komoot requested but this deployment has no encryption key configured")
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "Komoot is enabled but this deployment cannot store sign-ins — set " +
				secrets.EnvKey + ", or provide KOMOOT_EMAIL and KOMOOT_PASSWORD",
		})
	}
}
