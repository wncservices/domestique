package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/accounts"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/fitcourse"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/pacing"
	"github.com/wncservices/domestique/apps/api/internal/targets"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The pacing course: the route as a FIT course whose course points carry the
// climb targets ("C3 250-265W", "Top C3"), downloadable and pushable to the
// rider's own Garmin or Wahoo account on request. It is a separate course from
// the library route's own: its own name, its own remote id (kept in
// pacing_pushes, never in sync state), and a re-push replaces it instead of
// piling up copies. The route's coordinates leave the app only in this file,
// to the rider's own consented account, exactly as the library push already
// sends them: no new destination.

// pacingCourse is a built pacing FIT and what it was built from.
type pacingCourse struct {
	FIT   []byte
	Name  string
	Route model.Route
	// Key is what the push is recorded under: "pacing:<goal id>" when the plan
	// is a goal's, "pacing:<route slug>" when it is the route's own.
	Key string
}

// pacingFailure is a request that cannot be answered, with the status and the
// sentence to answer it with.
type pacingFailure struct {
	status int
	msg    string
}

func (f *pacingFailure) Error() string { return f.msg }

func notFound(msg string) *pacingFailure {
	return &pacingFailure{status: http.StatusNotFound, msg: msg}
}

// buildPacingCourse builds the pacing course for a rider's view of a route:
// 404 for a route they cannot see or a goal that is not theirs (the same
// answer as missing), 422 when the plan cannot be built (no FTP, no elevation,
// not a cycling route, or heart-rate targets asked for without heart-rate data).
func (s *Server) buildPacingCourse(ctx context.Context, rider string, identity auth.Identity, slug, goalID string, hr bool) (*pacingCourse, error) {
	var goal *workout.Goal
	var profile workout.RiderProfile
	if s.Training != nil {
		if goalID != "" {
			g, err := s.Training.GetGoal(ctx, goalID)
			if err != nil && !errors.Is(err, workout.ErrGoalNotFound) {
				return nil, err
			}
			if err != nil || !isOwnTraining(identity, g.Rider) {
				return nil, notFound(workout.ErrGoalNotFound.Error())
			}
			goal = &g
		}
		p, _, err := s.Training.GetProfile(ctx, rider)
		if err != nil {
			return nil, err
		}
		profile = p
	}

	rp, code, err := s.loadRouteProfile(ctx, rider, slug)
	if err != nil {
		return nil, err
	}
	switch code {
	case "":
	case reasonRouteUnavailable:
		return nil, notFound("no such route")
	default:
		return nil, &pacingFailure{status: http.StatusUnprocessableEntity, msg: pacingReasons[code]}
	}
	pr, dto, code := s.pacingFor(rp, profile, goal)
	if code != "" {
		return nil, &pacingFailure{status: http.StatusUnprocessableEntity, msg: pacingReasons[code]}
	}
	if hr && dto.HRNote == "" {
		return nil, &pacingFailure{status: http.StatusUnprocessableEntity, msg: "Add your max or threshold heart rate to use heart-rate targets."}
	}

	points, err := s.Source.Track(ctx, slug)
	if err != nil {
		return nil, err
	}
	name := rp.Route.Name + " pacing"
	raw, err := fitcourse.Encode(points, fitcourse.Options{
		Name:         name,
		Sport:        fitcourse.SportFromString(string(model.SportCycling)),
		CoursePoints: pacing.CoursePoints(pr.plan, rp.DeviceClimbs, hr),
	})
	if err != nil {
		return nil, err
	}
	key := "pacing:" + slug
	if goal != nil {
		key = "pacing:" + goal.ID
	}
	return &pacingCourse{FIT: raw, Name: name, Route: rp.Route, Key: key}, nil
}

// failPacing writes a pacingFailure with its status, or hands anything else to
// the ordinary error path.
func (s *Server) failPacing(w http.ResponseWriter, err error) {
	var pf *pacingFailure
	if errors.As(err, &pf) {
		writeJSON(w, pf.status, map[string]string{"error": pf.msg})
		return
	}
	s.fail(w, err)
}

// handlePacingFIT answers GET /api/routes/{slug}/pacing.fit?goal=&target=: the
// same file a push would send, as a download for a head unit on a cable.
func (s *Server) handlePacingFIT(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermReadRoutes) {
		return
	}
	slug := cleanSlug(r.PathValue("slug"))
	identity := auth.FromContext(r.Context())
	q := r.URL.Query()
	c, err := s.buildPacingCourse(r.Context(), identity.User, identity, slug, q.Get("goal"), q.Get("target") == "hr")
	if err != nil {
		s.failPacing(w, err)
		return
	}
	s.logger().Info("pacing course downloaded", "rider", identity.User, "route", slug, "outcome", "ok")
	writeFITAttachment(s.logger(), w, slug+"-pacing", c.FIT)
}

type pacingPushRequest struct {
	Provider string `json:"provider"`
	Goal     string `json:"goal"`
	// Target is "hr" for heart-rate course points, anything else for watts.
	Target string `json:"target"`
}

// handlePacingPush answers POST /api/routes/{slug}/pacing/push: it sends the
// pacing course to the signed-in rider's own Garmin or Wahoo account. The rider
// is the session's, never the body's, so there is no way to name someone else's
// account. A second push replaces the first.
func (s *Server) handlePacingPush(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermPush) {
		return
	}
	slug := cleanSlug(r.PathValue("slug"))
	identity := auth.FromContext(r.Context())
	rider := identity.User

	var body pacingPushRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	provider := model.Provider(body.Provider)
	if provider != model.ProviderGarmin && provider != model.ProviderWahoo {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `provider must be "garmin" or "wahoo"`})
		return
	}

	c, err := s.buildPacingCourse(r.Context(), rider, identity, slug, body.Goal, body.Target == "hr")
	if err != nil {
		s.failPacing(w, err)
		return
	}

	// Everything that can stop a push for lack of a connected account answers
	// 412 with a warn line naming only the rider, route and provider.
	noAccount := func(why string) {
		s.logger().Warn("pacing push: no usable account", "rider", rider, "route", slug, "provider", string(provider), "outcome", why)
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": fmt.Sprintf("Connect %s in Settings before sending a pacing course to it.", providerName(provider))})
	}
	if s.Accounts == nil {
		noAccount("accounts not configured")
		return
	}
	if _, err := s.Accounts.Get(r.Context(), fmt.Sprintf("%s:%s", provider, strings.ToLower(rider))); err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			noAccount("no linked account")
			return
		}
		s.fail(w, err)
		return
	}

	var previous string
	if s.PacingPushes != nil {
		previous, _, err = s.PacingPushes.Get(r.Context(), rider, string(provider), c.Key)
		if err != nil {
			s.fail(w, err)
			return
		}
	}

	var remoteID string
	switch provider {
	case model.ProviderGarmin:
		courses, cerr := s.garminCourses(rider)
		if cerr != nil {
			noAccount("not connected")
			return
		}
		remoteID, err = courses.ImportCourse(r.Context(), slug+"-pacing.fit", c.FIT)
		if err == nil && previous != "" && previous != remoteID {
			// Import first, delete after: a failed import leaves the course the
			// rider already has. A failed delete leaves one stale copy, worth a
			// line, not a failed push.
			if derr := courses.DeleteCourse(r.Context(), previous); derr != nil {
				s.logger().Warn("pacing push: the replaced Garmin course could not be removed", "rider", rider, "route", slug, "err", derr)
			}
		}
	case model.ProviderWahoo:
		client, cerr := s.wahooRoutes(r.Context(), rider)
		if cerr != nil {
			noAccount("not connected")
			return
		}
		req := targets.WahooRoute{
			ExternalID: c.Key, Name: c.Name, Description: "Pacing plan for " + c.Route.Name, UpdatedAt: time.Now().UTC(),
			DistanceM: c.Route.Stats.DistanceM, AscentM: c.Route.Stats.AscentM,
			StartLat: c.Route.Stats.StartLat, StartLng: c.Route.Stats.StartLng,
			Filename: slug + "-pacing.fit", FIT: c.FIT, Sport: string(model.SportCycling),
		}
		if previous != "" {
			remoteID, err = client.UpdateRoute(r.Context(), previous, req)
			if err != nil {
				// The route may have been deleted on the account since: make a new
				// one rather than fail, and replace the record.
				s.logger().Warn("pacing push: updating the Wahoo route failed, creating a new one", "rider", rider, "route", slug, "err", err)
				remoteID, err = client.CreateRoute(r.Context(), req)
			}
		} else {
			remoteID, err = client.CreateRoute(r.Context(), req)
		}
	}
	if err != nil {
		s.logger().Error("pacing push failed", "rider", rider, "route", slug, "provider", string(provider), "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("%s did not accept the course: %v", providerName(provider), err)})
		return
	}

	if s.PacingPushes != nil {
		if err := s.PacingPushes.Put(r.Context(), rider, string(provider), c.Key, remoteID); err != nil {
			// The course is on the device; the record is what stops the next push
			// duplicating it, so say so loudly.
			s.logger().Error("pacing push: recording the remote id failed", "rider", rider, "route", slug, "provider", string(provider), "err", err)
		}
	}
	s.logger().Info("pacing course pushed", "rider", rider, "route", slug, "provider", string(provider), "outcome", map[bool]string{true: "replaced", false: "created"}[previous != ""])
	writeJSON(w, http.StatusOK, map[string]any{"provider": string(provider), "name": c.Name, "replaced": previous != ""})
}

func providerName(p model.Provider) string {
	if p == model.ProviderWahoo {
		return "Wahoo"
	}
	return "Garmin"
}
