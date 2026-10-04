package api

import (
	"context"
	"math"
	"net/http"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/loops"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routefit"
	"github.com/wncservices/domestique/apps/api/internal/routing"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// This file is a route for a planned ride: a loop sized to the ride's time
// and shaped for what the session is for, chosen by the rider, saved as an
// ordinary library route and linked to the workout.
//
// A route is personal location data, and these start at the rider's saved
// start point. So a candidate is returned to its owner only (as
// routebuilder/suggest returns its own), is held in memory rather than
// written anywhere until the rider picks one, and nothing here logs a
// coordinate, the start's town, the rider's FTP, watts or weight.

// Why a ride cannot take a route; the UI keys off these.
const (
	codeNoStartPoint  = "no_start_point"
	codeNotCycling    = "not_cycling"
	codeIndoor        = "indoor"
	codeFTPTest       = "ftp_test"
	codeUnscheduled   = "unscheduled"
	codePast          = "past"
	codeRidden        = "ridden"
	messageNoStart    = "choose where your rides start first"
	messageNoLoop     = "could not find a loop close enough to the length of that ride"
	messageEngineDown = "the routing engine could not make a loop right now"
)

// workoutRouteRefusal answers 409 for a ride that cannot take a route:
// cycling only, outdoor, not an FTP test, dated today or later and not yet
// ridden. It reports whether it refused. An indoor session needs no route,
// and a ridden or past one has nowhere left to use it.
func (s *Server) workoutRouteRefusal(w http.ResponseWriter, r *http.Request, wk workout.Workout) bool {
	refuse := func(code, msg string) bool {
		writeJSON(w, http.StatusConflict, map[string]string{"error": msg, "code": code})
		return true
	}
	switch {
	case wk.Sport != model.SportCycling:
		return refuse(codeNotCycling, "only cycling rides take a route")
	case wk.Indoor:
		return refuse(codeIndoor, "an indoor ride needs no route")
	case wk.TestProtocol != "":
		return refuse(codeFTPTest, "an FTP test needs no route")
	case wk.Date == "":
		return refuse(codeUnscheduled, "give the ride a day first")
	}
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return true
	}
	todayStr := today.Format(dateFormat)
	if wk.Date < todayStr {
		return refuse(codePast, indoorPastMessage)
	}
	if wk.Date == todayStr {
		ridden, err := s.riddenToday(r.Context(), wk.Rider, todayStr, []workout.Workout{wk})
		if err != nil {
			s.fail(w, err)
			return true
		}
		if ridden[wk.ID] {
			return refuse(codeRidden, indoorRiddenMessage)
		}
	}
	return false
}

// riderFor is what the physics needs to know about a rider.
func (s *Server) riderFor(ctx context.Context, rider string) (routefit.Rider, error) {
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return routefit.Rider{}, err
	}
	return routefit.Rider{FTP: profile.FTPWatts, WeightKg: profile.WeightKG}, nil
}

// routeCandidateDTO is one generated loop, returned to its owner. Points are
// the owner's own generated loop, the same data routebuilder/suggest already
// returns to its caller; they appear nowhere else.
type routeCandidateDTO struct {
	ID               string              `json:"id"`
	Points           [][2]float64        `json:"points"`
	DistanceM        float64             `json:"distanceM"`
	AscentM          float64             `json:"ascentM"`
	Surface          []surfaceDTO        `json:"surface"`
	Elevation        []elevationPointDTO `json:"elevationProfile"`
	EstimatedSeconds float64             `json:"estimatedSeconds"`
	Family           string              `json:"family"`
	// Score is the loop's overall score, 0 to 1: half how close its time is to
	// the planned time, half how well its terrain suits the session.
	// TerrainFit is the second half alone, and Note says in words what is
	// missing when it is low. "Best fit" is a claim about terrain, so a client
	// shows it from TerrainFit, not from Score.
	Score      float64 `json:"score"`
	TerrainFit float64 `json:"terrainFit"`
	Note       string  `json:"note,omitempty"`
}

type routeCandidatesDTO struct {
	Candidates     []routeCandidateDTO `json:"candidates"`
	PlannedSeconds float64             `json:"plannedSeconds"`
	SpeedKph       float64             `json:"speedKph"`
	SpeedAssumed   bool                `json:"speedAssumed"`
}

// handleWorkoutRouteCandidates generates up to three loops for a planned
// ride from the rider's saved start point. The start is never read from the
// request.
func (s *Server) handleWorkoutRouteCandidates(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User

	if s.Routing == nil || s.RideStarts == nil {
		s.logger().Warn("workout route requested but no routing engine is configured", "by", rider)
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment has no routing engine configured",
		})
		return
	}

	wk, ok := s.ownWorkoutOrNotFound(w, r)
	if !ok {
		return
	}
	if s.workoutRouteRefusal(w, r, wk) {
		return
	}

	start, have, err := s.RideStarts.Get(r.Context(), wk.Rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !have {
		// Not a failure: the UI answers by asking for a start point.
		s.logger().Info("workout route needs a start point first", "by", rider, "workout", wk.ID)
		writeJSON(w, http.StatusConflict, map[string]string{"error": messageNoStart, "code": codeNoStartPoint})
		return
	}

	who, err := s.riderFor(r.Context(), wk.Rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	obj := loops.NewTimeObjective(wk, who)
	if obj.PlannedSeconds() <= 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "this ride has no planned length to fit a route to",
		})
		return
	}

	// After every refusal that costs nothing, so a refused request does not
	// spend the rider's route-builder budget.
	if !s.rateLimitWorkoutRoute(w, rider) {
		return
	}

	profile, hilliness := obj.Family().Engine()
	found, stats := loops.Generate(r.Context(), s.Routing, loops.Request{
		Start:            routing.LatLng{Lat: start.Lat, Lon: start.Lon},
		Profile:          profile,
		Hilliness:        hilliness,
		SeedBase:         loops.NewSeedBase(),
		CalibrationSeeds: loops.TimeCalibrationSeeds,
		RefinementSeeds:  loops.TimeRefinementSeeds,
		// A dead or out-of-quota engine costs the three calibration calls, not ten.
		StopWhenCalibrationFails: true,
		Objective:                obj,
	})
	if r.Context().Err() != nil {
		// The rider left; nobody is waiting for an answer, and a cancelled
		// request is not an engine failure to alarm anyone about.
		s.logger().Info("workout route request cancelled", "by", rider, "workout", wk.ID, "attempts", stats.Attempts)
		return
	}
	for range stats.Failures {
		recordRouteBuilderError(r.Context(), "workout_route")
	}

	switch {
	case len(found) > 0 && len(stats.Failures) > 0:
		// The request still succeeds with what routed.
		s.logger().Warn("workout route: some seeds failed, using the rest",
			"by", rider, "workout", wk.ID, "failed", len(stats.Failures), "attempts", stats.Attempts,
			"cause", loops.FailureClass(stats.LastErr))
	case len(found) == 0 && len(stats.Failures) == stats.Attempts:
		s.logger().Error("workout route: the routing engine failed for every seed",
			"by", rider, "workout", wk.ID, "attempts", stats.Attempts, "cause", loops.FailureClass(stats.LastErr))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": messageEngineDown})
		return
	case len(found) == 0:
		s.logger().Error("workout route: nothing within 15 percent of the planned time",
			"by", rider, "workout", wk.ID, "attempts", stats.Attempts, "failed", len(stats.Failures))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": messageNoLoop})
		return
	}

	held := make([]heldCandidate, len(found))
	dtos := make([]routeCandidateDTO, len(found))
	for i, l := range found {
		d := obj.Detail(l)
		summary := summarizePath(l.Path)
		held[i] = heldCandidate{
			Path: l.Path, EstimatedSeconds: d.EstimatedSeconds,
			DistanceM: summary.DistanceM, AscentM: summary.AscentM,
		}
		dtos[i] = routeCandidateDTO{
			Points: summary.Coords, DistanceM: summary.DistanceM, AscentM: summary.AscentM,
			Surface: summary.Surface, Elevation: summary.Elevation,
			EstimatedSeconds: d.EstimatedSeconds, Family: string(d.Family),
			Score: d.Score, TerrainFit: d.TerrainFit, Note: d.Note,
		}
	}
	for i, id := range s.candidateStore().Hold(rider, wk.ID, held) {
		dtos[i].ID = id
	}

	s.logger().Info("workout route candidates generated", "by", rider, "workout", wk.ID, "count", len(dtos))
	writeJSON(w, http.StatusOK, routeCandidatesDTO{
		Candidates:     dtos,
		PlannedSeconds: obj.PlannedSeconds(),
		SpeedKph:       math.Round(obj.SpeedMps()*3.6*10) / 10,
		SpeedAssumed:   obj.SpeedAssumed(),
	})
}
