package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/routefit"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// This file is the other direction: a library route put on a day as a ride.
// The route's time is routefit.EstimateSeconds at endurance power, the same
// function that sizes a generated loop, and it is an estimate wherever it is
// shown. Three things can happen to the day, never a second outdoor ride on
// it: link the route to the ride already there, adjust that ride to the
// route's time, or make a new endurance ride when the day has none.

const (
	choiceLink   = "link"
	choiceAdjust = "adjust"
	choiceNew    = "new"

	// enduranceFraction is the share of FTP a route's ride is estimated at.
	enduranceFraction = 0.65
	// linkTolerance is how far a ride's planned time may sit from the route's
	// before the rider is offered to resize it.
	linkTolerance = 0.20
)

type scheduleSessionDTO struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	PlannedSeconds float64 `json:"plannedSeconds"`
	// KeySession is true when the session is one the plan counts on (a long
	// ride or an intensity session): adjusting it replaces it.
	KeySession bool `json:"keySession"`
	// Routed is true when it already has a route, which linking replaces.
	Routed bool `json:"routed"`
	// Close is true when its planned time is within 20 percent of the route's.
	Close bool `json:"close"`
}

type scheduleSituationDTO struct {
	Date string `json:"date"`
	// RouteSeconds is an estimate at endurance power; RouteAssumed says it
	// leans on an assumption (no FTP, or no weight).
	RouteSeconds   float64              `json:"routeSeconds"`
	RouteAssumed   bool                 `json:"routeAssumed"`
	Sessions       []scheduleSessionDTO `json:"sessions"`
	Choices        []string             `json:"choices"`
	Default        string               `json:"default,omitempty"`
	NeedsWorkoutID bool                 `json:"needsWorkoutId,omitempty"`
}

type scheduleResultDTO struct {
	Workout workoutDTO `json:"workout"`
	Choice  string     `json:"choice"`
	// ReplacedKeySession is true when adjust rewrote a session the plan
	// counted on, which the plan will not add another of this week.
	ReplacedKeySession bool `json:"replacedKeySession,omitempty"`
}

// scheduleContext is what both endpoints work out first.
type scheduleContext struct {
	rider     string
	route     model.Route
	date      string
	seconds   float64
	assumed   bool
	candidate []workout.Workout
	situation scheduleSituationDTO
}

// loadSchedule resolves the route, the day and the sessions on it, and
// answers the refusals shared by GET and POST. ok is false once it has
// written one.
func (s *Server) loadSchedule(w http.ResponseWriter, r *http.Request, date string) (scheduleContext, bool) {
	var sc scheduleContext
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return sc, false
	}
	sc.rider = auth.FromContext(r.Context()).User
	slug := cleanSlug(r.PathValue("slug"))

	rt, visible, err := s.routeVisibleTo(r.Context(), sc.rider, slug)
	if err != nil {
		s.fail(w, err)
		return sc, false
	}
	if !visible {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such route"})
		return sc, false
	}
	if rt.EffectiveSport() != model.SportCycling {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "only cycling routes can be scheduled as a ride"})
		return sc, false
	}
	sc.route = rt

	day, err := time.Parse(dateFormat, date)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date must be a day in YYYY-MM-DD form"})
		return sc, false
	}
	sc.date = day.Format(dateFormat)
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return sc, false
	}
	todayStr := today.Format(dateFormat)
	if sc.date < todayStr {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "that day has already passed", "code": codePast})
		return sc, false
	}

	points, err := s.Source.Track(r.Context(), slug)
	if err != nil {
		s.failLookup(w, err)
		return sc, false
	}
	who, err := s.riderFor(r.Context(), sc.rider)
	if err != nil {
		s.fail(w, err)
		return sc, false
	}
	sc.seconds, sc.assumed = routefit.EstimateSeconds(points, who, enduranceFraction)

	all, err := s.Training.ListWorkouts(r.Context(), sc.rider)
	if err != nil {
		s.fail(w, err)
		return sc, false
	}
	var onDay []workout.Workout
	for _, wk := range all {
		if wk.Date == sc.date && wk.Sport == model.SportCycling && !wk.Indoor && wk.TestProtocol == "" {
			onDay = append(onDay, wk)
		}
	}
	ridden := map[string]bool{}
	if sc.date == todayStr {
		if ridden, err = s.riddenToday(r.Context(), sc.rider, todayStr, onDay); err != nil {
			s.fail(w, err)
			return sc, false
		}
	}
	for _, wk := range onDay {
		if !ridden[wk.ID] {
			sc.candidate = append(sc.candidate, wk)
		}
	}
	sc.situation = situationFor(sc)
	return sc, true
}

// situationFor applies the table: what the day holds decides what is offered.
func situationFor(sc scheduleContext) scheduleSituationDTO {
	out := scheduleSituationDTO{
		Date: sc.date, RouteSeconds: sc.seconds, RouteAssumed: sc.assumed,
		Sessions: []scheduleSessionDTO{}, Choices: []string{},
	}
	for _, wk := range sc.candidate {
		planned := workout.PlannedSeconds(wk.Steps)
		out.Sessions = append(out.Sessions, scheduleSessionDTO{
			ID: wk.ID, Name: wk.Name, PlannedSeconds: planned,
			KeySession: scheduler.IsKeySession(wk), Routed: wk.RouteSlug != "",
			Close: planned > 0 && math.Abs(sc.seconds-planned) <= linkTolerance*planned,
		})
	}
	switch {
	case len(out.Sessions) == 0:
		out.Choices, out.Default = []string{choiceNew}, choiceNew
	case len(out.Sessions) == 1 && out.Sessions[0].Close:
		out.Choices, out.Default = []string{choiceLink}, choiceLink
	case len(out.Sessions) == 1:
		out.Choices, out.Default = []string{choiceLink, choiceAdjust}, choiceLink
	default:
		out.Choices, out.NeedsWorkoutID = []string{choiceLink, choiceAdjust}, true
	}
	return out
}

// handleRouteScheduleSituation says what putting the route on a day would
// offer. Writes nothing.
func (s *Server) handleRouteScheduleSituation(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.loadSchedule(w, r, r.URL.Query().Get("date"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sc.situation)
}

// handleRouteSchedule puts the route on the day: link, adjust or new.
func (s *Server) handleRouteSchedule(w http.ResponseWriter, r *http.Request) {
	// Before the body is read: a caller who may not schedule gets 403, not a
	// parse error that confirms what the endpoint expects.
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	var body struct {
		Date      string `json:"date"`
		Choice    string `json:"choice"`
		WorkoutID string `json:"workoutId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	sc, ok := s.loadSchedule(w, r, body.Date)
	if !ok {
		return
	}
	offered := false
	for _, c := range sc.situation.Choices {
		offered = offered || c == body.Choice
	}
	if !offered {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "that choice is not available for this day", "code": "choice_unavailable",
		})
		return
	}

	var target workout.Workout
	if body.Choice != choiceNew {
		switch {
		case body.WorkoutID == "" && len(sc.candidate) == 1:
			target = sc.candidate[0]
		case body.WorkoutID == "":
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workoutId is required when the day has more than one ride"})
			return
		default:
			found := false
			for _, wk := range sc.candidate {
				if wk.ID == body.WorkoutID {
					target, found = wk, true
				}
			}
			if !found {
				// Another rider's, another day's, indoor, a test or already
				// ridden: not a ride this route can go on.
				writeJSON(w, http.StatusConflict, map[string]string{"error": "that ride cannot take a route"})
				return
			}
		}
	}

	res, err := s.applySchedule(r.Context(), sc, body.Choice, target)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("route scheduled as a ride", "by", sc.rider, "workout", res.Workout.ID, "choice", body.Choice)
	status := http.StatusOK
	if body.Choice == choiceNew {
		status = http.StatusCreated
	}
	writeJSON(w, status, res)
}

func (s *Server) applySchedule(ctx context.Context, sc scheduleContext, choice string, target workout.Workout) (scheduleResultDTO, error) {
	slug, secs := sc.route.Slug, sc.seconds
	res := scheduleResultDTO{Choice: choice}
	var saved workout.Workout
	var err error

	enduranceSteps := []workout.WorkoutStep{{
		Name: "Ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
		Seconds: math.Round(secs), Target: workout.TargetOpen,
	}}
	sized := fmt.Sprintf("Sized to %s.", sc.route.Name)

	switch choice {
	case choiceLink:
		// Name, steps and description are the rider's or the plan's; only the
		// route is added. A route this ride already had is replaced.
		previous := target.RouteSlug
		saved, err = s.Training.UpdateWorkout(ctx, target.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs})
		if err == nil && previous != "" && previous != slug {
			s.dropGeneratedRoute(ctx, target, previous)
		}
	case choiceAdjust:
		res.ReplacedKeySession = scheduler.IsKeySession(target)
		name := "Endurance ride: " + sc.route.Name
		zone, level := workout.ZoneEndurance, 0.0
		// In place, so the id and any Garmin copy carry on; the generated
		// description prefix goes, so this is the rider's own session now
		// and replan and refresh leave it. The goal id stays, so the day
		// still counts as taken.
		previous := target.RouteSlug
		saved, err = s.Training.UpdateWorkout(ctx, target.ID, workout.UpdateWorkoutRequest{
			Name: &name, Description: &sized, Steps: &enduranceSteps, Zone: &zone, Level: &level,
			RouteSlug: &slug, RouteSeconds: &secs,
		})
		if err == nil && previous != "" && previous != slug {
			s.dropGeneratedRoute(ctx, target, previous)
		}
	default: // choiceNew
		goalID := ""
		if goals, gerr := s.Training.ListGoals(ctx, sc.rider); gerr == nil {
			sortGoalsForFocus(goals)
			for _, g := range goals {
				if g.Sport == model.SportCycling {
					goalID = g.ID
					break
				}
			}
		}
		saved, err = s.Training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
			Rider: sc.rider, Sport: model.SportCycling, Name: "Endurance ride: " + sc.route.Name,
			GoalID: goalID, Date: sc.date, Description: sized,
			Steps: enduranceSteps, Zone: workout.ZoneEndurance,
		})
		if err == nil {
			saved, err = s.Training.UpdateWorkout(ctx, saved.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs})
		}
	}
	if err != nil {
		return res, err
	}
	res.Workout = s.workoutDTOWithWhy(ctx, saved)
	return res, nil
}
