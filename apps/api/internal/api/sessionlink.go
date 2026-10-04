package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/progression"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// sessionLinkRequestDTO is PUT /api/training/sessions/{id}/workout's body.
type sessionLinkRequestDTO struct {
	// WorkoutID is the planned session this ride was; "" says it was not a
	// planned session at all.
	WorkoutID string `json:"workoutId"`
	// Auto drops a link made by hand, so the automatic match decides again.
	// WorkoutID is ignored when it is set.
	Auto bool `json:"auto,omitempty"`
}

// handleLinkSession is PUT /api/training/sessions/{id}/workout: the rider
// says which planned session a ride was, for when the automatic match (same
// date, same sport, closest planned duration) got it wrong or found nothing —
// a test ridden a day early, a session done the day after it was planned.
//
// The link is stored (it outranks the automatic match on every later pass),
// the ride's analysis is forgotten, and a sync runs straight away so the ride
// is scored against the session it really was: steps, level move, and for an
// FTP test the test result. The response is that sync's result, the same
// shape "Sync now" returns, so the UI can report a test result the same way.
//
// A workout on another day is moved to the ride's day, so the week reads the
// way it was ridden and the day it came from is not refilled.
func (s *Server) handleLinkSession(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	ctx := r.Context()
	identity := auth.FromContext(ctx)

	var body sessionLinkRequestDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Auto {
		body.WorkoutID = ""
	}

	// 404 rather than 403 for another rider's ride or workout, like the feel
	// endpoint next to this one: the answer does not confirm it exists.
	sess, err := s.Training.GetSession(ctx, r.PathValue("id"))
	if errors.Is(err, workout.ErrSessionNotFound) || (err == nil && !isOwnTraining(identity, sess.Rider)) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	rider := sess.Rider

	// Only a ride the analysis pass still looks at can be re-scored: an older
	// one would lose its analysis here and never get it back.
	if sess.Date < s.now().Add(-rideAnalysisWindow).Format(dateLayout) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "That ride is too old to link to a planned session."})
		return
	}

	var target workout.Workout
	if body.WorkoutID != "" {
		target, err = s.Training.GetWorkout(ctx, body.WorkoutID)
		if errors.Is(err, workout.ErrWorkoutNotFound) || (err == nil && !isOwnTraining(identity, target.Rider)) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such workout"})
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		if string(target.Sport) != sess.Sport {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That session is " + string(target.Sport) + " and this ride is " + sess.Sport + "."})
			return
		}
		// Linking moves the session to the day it was ridden; a crew ride's day is
		// the crew's, so it is not moved by the back door either.
		if target.CrewRideID != "" && target.Date != sess.Date {
			writeJSON(w, http.StatusConflict, map[string]string{"error": crewRideMoveMessage})
			return
		}
		if target.TestProtocol != "" && target.TestResultWatts != 0 {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "That FTP test already has its result, from another ride."})
			return
		}
		// One ride per planned session: silently re-pointing another ride's
		// match would change that ride's verdict behind the rider's back.
		other, err := s.rideLinkedTo(ctx, rider, sess.ID, target.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if other != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Your ride on " + other.Date + " is already linked to that session. Unlink that ride first."})
			return
		}
	}

	current, analysed, err := s.Training.GetAnalysis(ctx, sess.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if analysed && current.WorkoutID != "" && current.WorkoutID != body.WorkoutID {
		if prev, err := s.Training.GetWorkout(ctx, current.WorkoutID); err == nil {
			// One result per test, ever: the FTP it measured may already have
			// been applied, and nothing here could take that back.
			if prev.TestProtocol != "" && prev.TestResultWatts != 0 {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "This ride already gave its FTP test a result, so it stays linked to that test."})
				return
			}
		} else if !errors.Is(err, workout.ErrWorkoutNotFound) {
			s.fail(w, err)
			return
		}
	}

	if analysed {
		if err := s.undoLevelMove(ctx, rider, current); err != nil {
			s.fail(w, err)
			return
		}
	}

	if body.Auto {
		err = s.Training.ClearSessionLink(ctx, sess.ID)
	} else {
		err = s.Training.SetSessionLink(ctx, rider, sess.ID, body.WorkoutID)
	}
	if err != nil {
		s.fail(w, err)
		return
	}

	if body.WorkoutID != "" && target.Date != sess.Date {
		date := sess.Date
		req := workout.UpdateWorkoutRequest{Date: &date}
		req.Description = recordManualMove(target, req)
		if _, err := s.Training.UpdateWorkout(ctx, target.ID, req); err != nil {
			s.fail(w, err)
			return
		}
	}

	// The rider's answers about how the ride felt belong to the ride, not to
	// the analysis being thrown away: carried onto the fresh one below.
	var carried *feelRequestDTO
	if analysed && current.Feel >= 1 {
		carried = &feelRequestDTO{Feel: current.Feel, Legs: current.Legs, Stress: current.Stress}
	}
	if err := s.Training.DeleteAnalysis(ctx, sess.ID); err != nil {
		s.fail(w, err)
		return
	}

	linked := "workout"
	switch {
	case body.Auto:
		linked = "auto"
	case body.WorkoutID == "":
		linked = "none"
	}
	s.logger().Info("ride linked to a planned session", "rider", rider, "session", sess.ID, "link", linked)

	result, err := s.syncRiderMetrics(ctx, rider, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	if carried != nil {
		s.carrySurvey(ctx, sess.ID, *carried)
	}
	writeJSON(w, http.StatusOK, result)
}

// carrySurvey puts a re-linked ride's survey back on its fresh analysis, through
// the same path a rating takes, so the level move is worked out for the session
// the ride is now linked to (an all-out rating still counts as a struggle there)
// and a no-power, no-HR ride keeps its effort load instead of reverting to the
// flat guess. If the sync did not manage to analyse the ride again there is
// nothing to put it on; that is a Warn, and the rider can rate it again.
func (s *Server) carrySurvey(ctx context.Context, sessionID string, survey feelRequestDTO) {
	fresh, ok, err := s.Training.GetAnalysis(ctx, sessionID)
	if err != nil || !ok {
		s.logger().Warn("re-link: the ride was not analysed again, so its survey could not be carried over", "session", sessionID, "analysed", ok, "err", err)
		return
	}
	if _, err := s.applySurvey(ctx, fresh, survey); err != nil {
		s.logger().Warn("re-link: carrying the survey over failed", "session", sessionID, "err", err)
	}
}

// rideLinkedTo returns another of the rider's rides that is already scored
// against workoutID, by hand or by the automatic match, or nil.
func (s *Server) rideLinkedTo(ctx context.Context, rider, sessionID, workoutID string) (*workout.CompletedSession, error) {
	links, err := s.Training.SessionLinks(ctx, rider)
	if err != nil {
		return nil, err
	}
	since := s.now().Add(-2 * rideAnalysisWindow).Format(dateLayout)
	analyses, err := s.Training.ListAnalyses(ctx, rider, since)
	if err != nil {
		return nil, err
	}
	otherID := ""
	for id, wid := range links {
		if id != sessionID && wid == workoutID {
			otherID = id
			break
		}
	}
	if otherID == "" {
		for _, a := range analyses {
			if a.SessionID == sessionID || a.WorkoutID != workoutID {
				continue
			}
			// A ride linked by hand elsewhere keeps its analysis until the
			// next pass; its link, not that stale analysis, is what counts.
			if wid, ok := links[a.SessionID]; ok && wid != workoutID {
				continue
			}
			otherID = a.SessionID
			break
		}
	}
	if otherID == "" {
		return nil, nil
	}
	other, err := s.Training.GetSession(ctx, otherID)
	if errors.Is(err, workout.ErrSessionNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &other, nil
}

// undoLevelMove takes back the progression-level change a ride's analysis
// applied, before the ride is scored against a different session — the same
// subtract-the-old-delta step a feel re-rate does, so a re-link never stacks
// a second move on the first.
func (s *Server) undoLevelMove(ctx context.Context, rider string, a workout.SessionAnalysis) error {
	if a.WorkoutID == "" || a.LevelDelta == 0 {
		return nil
	}
	wk, err := s.Training.GetWorkout(ctx, a.WorkoutID)
	if errors.Is(err, workout.ErrWorkoutNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !workout.IsStructuredZone(wk.Zone) {
		return nil
	}
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return err
	}
	levels, err := s.levelsFor(ctx, rider, profile, wk.Sport)
	if err != nil {
		return err
	}
	return s.Training.SaveLevel(ctx, workout.ProgressionLevel{
		Rider: rider, Sport: wk.Sport, Zone: wk.Zone,
		Level:  progression.Apply(levels[string(wk.Zone)], -a.LevelDelta),
		Reason: "Undone: the ride behind the last change was linked to a different session.",
	})
}
