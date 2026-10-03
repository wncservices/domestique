package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/gpx"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// wrouteTagPrefix tags a route generated for one ride: "wroute:<workoutId>".
// The tag is the privacy guard. Such a route starts at the rider's saved
// start point, so while it carries the tag it is owner-only: a share link or
// a crew target on it is refused. Removing the tag is the rider's own,
// deliberate way to make it an ordinary route.
const wrouteTagPrefix = "wroute:"

func hasWrouteTag(tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, wrouteTagPrefix) {
			return true
		}
	}
	return false
}

// refuseTaggedRoute answers 409 for an attempt to share a generated route.
// It reports whether it refused.
func refuseTaggedRoute(w http.ResponseWriter, tags []string) bool {
	if !hasWrouteTag(tags) {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]string{
		"error": "generated for a ride from your start point; remove the tag to share it",
	})
	return true
}

// workoutRouteDTO is the route linked to a workout. No coordinates.
type workoutRouteDTO struct {
	Slug             string  `json:"slug"`
	Name             string  `json:"name"`
	DistanceM        float64 `json:"distanceM"`
	AscentM          float64 `json:"ascentM"`
	EstimatedSeconds float64 `json:"estimatedSeconds"`
	// Generated is true for a loop made for this ride (tagged wroute:), false
	// for a library route the rider scheduled onto it.
	Generated bool `json:"generated"`
	// Inactive is true while the ride is indoor: the link is kept, so going
	// back outdoors restores it, but the day card and the push ignore it.
	Inactive bool `json:"inactive,omitempty"`
}

// attachRoutes fills Route on every linked workout in lists with one read of
// the library. A route the rider cannot see (deleted, or no longer theirs to
// see) is left off, so a slug alone never confirms a route exists. A failure
// is a Warn and leaves the routes off: a missing route line must not fail a
// page of sessions.
func (s *Server) attachRoutes(ctx context.Context, rider string, lists ...[]workoutDTO) {
	need := false
	for _, l := range lists {
		for _, d := range l {
			if d.routeSlug != "" {
				need = true
			}
		}
	}
	if !need || s.Source == nil {
		return
	}
	routes, _, err := s.Source.List(ctx)
	if err != nil {
		s.logger().Warn("could not read routes for workouts", "by", rider, "err", err)
		return
	}
	var snap crew.Snapshot
	if s.Crew != nil {
		if snap, err = s.Crew.Snapshot(ctx); err != nil {
			s.logger().Warn("could not read crews for workouts", "by", rider, "err", err)
			return
		}
	}
	bySlug := map[string]model.Route{}
	for _, rt := range routes {
		if config.VisibleTo(rt, rider, snap) {
			bySlug[rt.Slug] = rt
		}
	}
	for _, l := range lists {
		for i := range l {
			rt, ok := bySlug[l[i].routeSlug]
			if !ok {
				continue
			}
			l[i].Route = &workoutRouteDTO{
				Slug: rt.Slug, Name: rt.Name, DistanceM: rt.Stats.DistanceM, AscentM: rt.Stats.AscentM,
				EstimatedSeconds: l[i].routeSeconds, Generated: hasWrouteTag(rt.Tags), Inactive: l[i].Indoor,
			}
		}
	}
}

// routeTags is a route's current tags.
func (s *Server) routeTags(ctx context.Context, slug string) ([]string, error) {
	routes, _, err := s.Source.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, rt := range routes {
		if rt.Slug == slug {
			return rt.Tags, nil
		}
	}
	return nil, source.ErrNotFound
}

// routeNameFor names a generated loop for its ride.
func routeNameFor(workoutName string, distanceM float64) string {
	return fmt.Sprintf("%s loop, %.0f km", workoutName, distanceM/1000)
}

// otherWorkoutLinks is how many of the rider's workouts besides skip link
// slug.
func (s *Server) otherWorkoutLinks(ctx context.Context, rider, slug, skip string) (int, error) {
	all, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, wk := range all {
		if wk.ID != skip && wk.RouteSlug == slug {
			n++
		}
	}
	return n, nil
}

// dropGeneratedRoute deletes the route linked from wk if it is a loop
// generated for exactly this workout and nothing else links it. A library
// route (one the rider scheduled) is never deleted here. Best effort: a
// route left behind is the rider's own to delete from the library.
func (s *Server) dropGeneratedRoute(ctx context.Context, wk workout.Workout, slug string) {
	if slug == "" || s.Source == nil {
		return
	}
	routes, _, err := s.Source.List(ctx)
	if err != nil {
		s.logger().Warn("could not read routes to clean up a replaced loop", "workout", wk.ID, "err", err)
		return
	}
	for _, rt := range routes {
		if rt.Slug != slug {
			continue
		}
		tagged := false
		for _, t := range rt.Tags {
			if t == wrouteTagPrefix+wk.ID {
				tagged = true
			}
		}
		if !tagged {
			return
		}
		others, err := s.otherWorkoutLinks(ctx, wk.Rider, slug, wk.ID)
		if err != nil || others > 0 {
			return
		}
		if err := s.Source.Delete(ctx, slug); err != nil {
			s.logger().Warn("could not delete a replaced loop", "workout", wk.ID, "err", err)
		}
		return
	}
}

// handleSaveWorkoutRoute turns a held candidate into a library route and
// links it to the workout. The GPX is built from the server's own held path,
// with its elevation; coordinates in the request are never read. The route is
// created straight through Source.Create with no targets, so it is owner-only
// and no crew auto-share applies.
func (s *Server) handleSaveWorkoutRoute(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	wk, ok := s.ownWorkoutOrNotFound(w, r)
	if !ok {
		return
	}
	var body struct {
		CandidateID string `json:"candidateId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.CandidateID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "candidateId is required"})
		return
	}
	if s.workoutRouteRefusal(w, r, wk) {
		return
	}
	held, found := s.candidateStore().Get(rider, body.CandidateID)
	if !found || held.WorkoutID != wk.ID {
		s.logger().Info("workout route candidate gone", "by", rider, "workout", wk.ID)
		writeJSON(w, http.StatusGone, map[string]string{"error": "that route is no longer available; generate again"})
		return
	}

	raw, err := gpx.Render(routeNameFor(wk.Name, held.DistanceM), held.Path.Points, nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	created, err := s.Source.Create(r.Context(), source.CreateRequest{
		Name:       routeNameFor(wk.Name, held.DistanceM),
		Tags:       []string{wrouteTagPrefix + wk.ID},
		Targets:    nil,
		GPX:        raw,
		UploadedBy: rider,
		Sport:      model.SportCycling,
	})
	if err != nil {
		s.fail(w, err)
		return
	}

	previous := wk.RouteSlug
	slug, secs := created.Slug, held.EstimatedSeconds
	updated, err := s.Training.UpdateWorkout(r.Context(), wk.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs})
	if err != nil {
		_ = s.Source.Delete(r.Context(), created.Slug)
		s.fail(w, err)
		return
	}
	if previous != "" && previous != slug {
		s.dropGeneratedRoute(r.Context(), wk, previous)
	}
	s.candidateStore().DropWorkout(rider, wk.ID)

	s.logger().Info("workout route saved", "by", rider, "workout", wk.ID, "replaced", previous != "")
	writeJSON(w, http.StatusOK, s.workoutDTOWithWhy(r.Context(), updated))
}

// handleRemoveWorkoutRoute unlinks the route; a loop generated for the ride is
// deleted with it, a library route only unlinked. Idempotent.
func (s *Server) handleRemoveWorkoutRoute(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	wk, ok := s.ownWorkoutOrNotFound(w, r)
	if !ok {
		return
	}
	if wk.RouteSlug == "" {
		writeJSON(w, http.StatusOK, s.workoutDTOWithWhy(r.Context(), wk))
		return
	}
	previous := wk.RouteSlug
	empty, zero := "", 0.0
	updated, err := s.Training.UpdateWorkout(r.Context(), wk.ID, workout.UpdateWorkoutRequest{RouteSlug: &empty, RouteSeconds: &zero})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.dropGeneratedRoute(r.Context(), wk, previous)
	s.logger().Info("workout route removed", "by", wk.Rider, "workout", wk.ID)
	writeJSON(w, http.StatusOK, s.workoutDTOWithWhy(r.Context(), updated))
}
