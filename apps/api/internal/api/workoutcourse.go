package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/crew"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A course and a workout are separate Garmin objects: the workout sits on the
// calendar, the course in Courses, and the head unit starts them
// independently. Whether an Edge follows a course while it runs a workout has
// not been checked on a device, so nothing here or in the UI claims it.
//
// The course is an ordinary library route and goes the ordinary way:
// through applyPush, selected to one (account, slug) pair per own account,
// idempotent on the route's content hash. Nothing is ever deleted by it.

// What pushing a ride's course did, for a response or a log line.
const (
	courseNone      = "none"
	coursePushed    = "pushed"
	courseUnchanged = "unchanged"
	courseFailed    = "failed"
)

// pushWorkoutCourse sends the route linked to wk to the rider's own accounts:
// Garmin only when garminOnly (the standing permission behind the automatic
// push names Garmin), every own account when the rider asked for it. A ride
// with no route, an indoor ride, or no account to send to is courseNone. It
// reports failure as a value and an error only for what stopped it asking;
// callers decide how loud that is.
func (s *Server) pushWorkoutCourse(ctx context.Context, wk workout.Workout, garminOnly bool) (string, error) {
	if wk.RouteSlug == "" || wk.Indoor || s.Source == nil || s.Accounts == nil || s.Store == nil {
		return courseNone, nil
	}
	routes, _, err := s.Source.List(ctx)
	if err != nil {
		return courseFailed, err
	}
	var route *model.Route
	for i := range routes {
		if routes[i].Slug == wk.RouteSlug {
			route = &routes[i]
		}
	}
	// Only the rider's own route goes to the rider's own accounts, however the
	// link got there: a slug alone never moves someone else's route.
	if route == nil || !strings.EqualFold(route.Owner, wk.Rider) {
		return courseNone, nil
	}
	linked, err := s.Accounts.List(ctx)
	if err != nil {
		return courseFailed, err
	}
	var own []model.Account
	selected := map[model.PlanKey]bool{}
	for _, a := range linked {
		if !strings.EqualFold(a.Rider, wk.Rider) {
			continue
		}
		if garminOnly && a.Provider != model.ProviderGarmin {
			continue
		}
		own = append(own, a)
		selected[model.PlanKey{AccountID: a.ID, Slug: route.Slug}] = true
	}
	if len(own) == 0 {
		return courseNone, nil
	}

	res, err := s.applyPush(ctx, []model.Route{*route}, own, crew.Snapshot{}, false, selected)
	switch {
	case err != nil:
		return courseFailed, err
	case len(res.Failures) > 0:
		return courseFailed, nil
	case res.Applied > 0:
		return coursePushed, nil
	default:
		return courseUnchanged, nil
	}
}

// handlePushWorkoutCourse is "Send to devices" on a routed ride: the course
// to every one of the rider's own accounts, since the rider asked for it.
func (s *Server) handlePushWorkoutCourse(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	wk, ok := s.ownWorkoutOrNotFound(w, r)
	if !ok {
		return
	}
	if wk.RouteSlug == "" || wk.Indoor {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this ride has no route to send"})
		return
	}
	outcome, err := s.pushWorkoutCourse(r.Context(), wk, false)
	if err != nil || outcome == courseFailed {
		// The rider asked for this one, so it answers 502; the log is a Warn
		// either way because nothing else about the ride is affected. The
		// error text is not repeated: an engine or provider message can echo
		// a coordinate.
		s.logger().Warn("course not sent to the rider's devices", "workout", wk.ID, "by", wk.Rider)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the route could not be sent to your devices", "course": courseFailed})
		return
	}
	s.logger().Info("course sent to the rider's devices", "workout", wk.ID, "by", wk.Rider, "outcome", outcome)
	writeJSON(w, http.StatusOK, map[string]string{"course": outcome})
}
