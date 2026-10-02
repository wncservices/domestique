package api

import (
	"context"
	"net/http"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/model"
)

// Bounds a rider may set on the profile and on a goal's pacing override.
const (
	minWeightKG  = 30.0
	maxWeightKG  = 250.0
	minPacingIF  = 0.60
	maxPacingIF  = 1.05
	routeRefused = "that route is not available for this goal"
)

// validWeightKG: 0 clears the weight; otherwise a plausible body weight.
func validWeightKG(kg float64) bool {
	return kg == 0 || (kg >= minWeightKG && kg <= maxWeightKG)
}

// validPacingIF: 0 means "derived from the event's duration".
func validPacingIF(v float64) bool {
	return v == 0 || (v >= minPacingIF && v <= maxPacingIF)
}

// routeVisibleTo finds slug in the library and reports whether rider may see
// it (config.VisibleTo). A route that is missing, or that exists but is not
// visible, are the same answer: false with no route, so nothing a caller says
// about it can confirm that someone else's route exists. An error means the
// library or the crews could not be read, not "no".
//
// Deliberately strict: no admin bypass. Training is about the rider's own
// season, and background planning has no role to consult, so a goal sees
// exactly what config.VisibleTo gives that rider.
func (s *Server) routeVisibleTo(ctx context.Context, rider, slug string) (model.Route, bool, error) {
	if s.Source == nil || slug == "" {
		return model.Route{}, false, nil
	}
	routes, _, err := s.Source.List(ctx)
	if err != nil {
		return model.Route{}, false, err
	}
	var snap crew.Snapshot
	if s.Crew != nil {
		if snap, err = s.Crew.Snapshot(ctx); err != nil {
			return model.Route{}, false, err
		}
	}
	for _, rt := range routes {
		if rt.Slug == slug && config.VisibleTo(rt, rider, snap) {
			return rt, true, nil
		}
	}
	return model.Route{}, false, nil
}

type goalRouteDTO struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// handleGoalRoutes answers GET /api/training/goal-routes: the cycling routes a
// rider may link to a goal. Strictly config.VisibleTo, with no admin bypass, so
// the picker offers exactly what handleCreateGoal and handleUpdateGoal accept
// (the library list shows an admin every route).
func (s *Server) handleGoalRoutes(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	out := []goalRouteDTO{}
	if s.Source != nil {
		routes, _, err := s.Source.List(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		var snap crew.Snapshot
		if s.Crew != nil {
			if snap, err = s.Crew.Snapshot(r.Context()); err != nil {
				s.fail(w, err)
				return
			}
		}
		for _, rt := range routes {
			if rt.EffectiveSport() == model.SportCycling && config.VisibleTo(rt, rider, snap) {
				out = append(out, goalRouteDTO{Slug: rt.Slug, Name: rt.Name})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// checkGoalRoute validates the route a goal is about to carry: the rider must
// be able to see it, and only a cycling goal may have one (a route is a ride).
// It returns the HTTP status and message to refuse with, or 0 when fine. The
// message never says why a route was refused, so it cannot be used to probe
// for other riders' routes.
func (s *Server) checkGoalRoute(ctx context.Context, rider string, sport model.Sport, slug string) (int, string) {
	if slug == "" {
		return 0, ""
	}
	if sport != "" && sport != model.SportCycling {
		return http.StatusUnprocessableEntity, "a route can only be linked to a cycling goal"
	}
	rt, ok, err := s.routeVisibleTo(ctx, rider, slug)
	if err != nil {
		s.logger().Warn("checking a goal's route failed", "rider", rider, "err", err)
		return http.StatusBadGateway, "could not check the route"
	}
	if !ok || rt.EffectiveSport() != model.SportCycling {
		return http.StatusUnprocessableEntity, routeRefused
	}
	return 0, ""
}
