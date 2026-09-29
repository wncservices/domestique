package api

import (
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/indoor"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// This file is the rider-facing half of the indoor version: convert a session
// to its trainer form, and go back. Both are rider-initiated (a click, or the
// weather banner's button later) and neither adds scheduler.AdjustedMarker,
// which stays the guard for *automatic* adaptation: an indoor session is still
// eligible for readiness easing, and keepIndoor makes sure easing does not
// turn it back into a road session.

const (
	revertNote           = "Back to the outdoor version."
	indoorPastMessage    = "This session is in the past, so it can no longer be changed."
	indoorRiddenMessage  = "This session has already been ridden, so it can no longer be changed."
	indoorRunningMessage = "Only cycling sessions have an indoor version."
)

// indoorPreviewDTO is what a preview says the conversion would do. Nothing is
// stored.
type indoorPreviewDTO struct {
	Note string `json:"note"`
	// ERG is true when the trainer can drive every step ("Trainer control
	// (ERG)"), false for "Ride by feel".
	ERG     bool `json:"erg"`
	Changed bool `json:"changed"`
	// PlannedSeconds is the indoor version's length; OriginalSeconds the
	// session's own, 0 when it is not time-based.
	PlannedSeconds  float64 `json:"plannedSeconds"`
	OriginalSeconds float64 `json:"originalSeconds,omitempty"`
}

// ownWorkoutOrNotFound reads the workout named in the path and answers 404
// for one that is not the caller's, exactly as for one that does not exist:
// another rider's session is not something to confirm the existence of.
func (s *Server) ownWorkoutOrNotFound(w http.ResponseWriter, r *http.Request) (workout.Workout, bool) {
	wk, err := s.Training.GetWorkout(r.Context(), r.PathValue("id"))
	if err != nil {
		s.failTrainingLookup(w, err)
		return workout.Workout{}, false
	}
	identity := auth.FromContext(r.Context())
	if !isOwnTraining(identity, wk.Rider) {
		s.logger().Info("training ownership denied", "user", identity.User, "role", identity.Role, "path", r.URL.Path)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": workout.ErrWorkoutNotFound.Error()})
		return workout.Workout{}, false
	}
	return wk, true
}

// refuseIfSettled answers 409 for a session that can no longer change: dated
// before the caller's today, or today's and already ridden. It reports whether
// it refused.
func (s *Server) refuseIfSettled(w http.ResponseWriter, r *http.Request, wk workout.Workout) bool {
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return true
	}
	todayStr := today.Format(dateFormat)
	if wk.Date != "" && wk.Date < todayStr {
		writeJSON(w, http.StatusConflict, map[string]string{"error": indoorPastMessage})
		return true
	}
	if wk.Date == todayStr {
		ridden, err := s.riddenToday(r.Context(), wk.Rider, todayStr, []workout.Workout{wk})
		if err != nil {
			s.fail(w, err)
			return true
		}
		if ridden[wk.ID] {
			writeJSON(w, http.StatusConflict, map[string]string{"error": indoorRiddenMessage})
			return true
		}
	}
	return false
}

// handleIndoorConvert edits the workout in place into its indoor version, or
// with ?preview=1 says what that would be and changes nothing. In place, not a
// copy: a copy would leave two sessions on one day, break the scheduler's "day
// is taken" logic and orphan the Garmin copy; the push path already updates a
// changed workout under its remote id.
//
// Idempotent: an already-indoor workout comes back unchanged, so a double
// click or a retried request cannot shorten a ride twice, and the first
// original stays in outdoor_steps.
func (s *Server) handleIndoorConvert(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	wk, ok := s.ownWorkoutOrNotFound(w, r)
	if !ok {
		return
	}
	if wk.Sport != model.SportCycling {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": indoorRunningMessage})
		return
	}
	if s.refuseIfSettled(w, r, wk) {
		return
	}
	profile, _, err := s.Training.GetProfile(r.Context(), wk.Rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := indoor.Convert(wk, profile)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": indoorRunningMessage})
		return
	}

	if r.URL.Query().Get("preview") == "1" {
		writeJSON(w, http.StatusOK, indoorPreviewDTO{
			Note: res.Note, ERG: res.ERG, Changed: res.Changed,
			PlannedSeconds: workout.PlannedSeconds(res.Steps), OriginalSeconds: workout.PlannedSeconds(wk.Steps),
		})
		return
	}
	if wk.Indoor {
		writeJSON(w, http.StatusOK, workoutDTOFrom(wk))
		return
	}

	// A non-nil slice even for a workout with no steps, so "nothing stored"
	// and "a stored original" stay different things.
	original := append([]workout.WorkoutStep{}, wk.Steps...)
	yes := true
	description := addNote(wk.Description, res.Note)
	updated, err := s.Training.UpdateWorkout(r.Context(), wk.ID, workout.UpdateWorkoutRequest{
		Steps: &res.Steps, Indoor: &yes, OutdoorSteps: &original, Description: &description,
	})
	if err != nil {
		s.logger().Error("could not convert a workout to indoor", "workout", wk.ID, "err", err)
		s.fail(w, err)
		return
	}
	s.logger().Info("workout converted to indoor", "workout", wk.ID)
	writeJSON(w, http.StatusOK, workoutDTOFrom(updated))
}

// handleIndoorRevert puts the pre-conversion steps back. Idempotent: a workout
// that is not indoor, or has nothing stored to restore, comes back unchanged
// with 200. Only the steps changed on conversion, so steps are all it restores.
func (s *Server) handleIndoorRevert(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	wk, ok := s.ownWorkoutOrNotFound(w, r)
	if !ok {
		return
	}
	if s.refuseIfSettled(w, r, wk) {
		return
	}
	if !wk.Indoor || wk.OutdoorSteps == nil {
		writeJSON(w, http.StatusOK, workoutDTOFrom(wk))
		return
	}

	restored := *wk.OutdoorSteps
	no := false
	var none []workout.WorkoutStep
	description := addNote(wk.Description, revertNote)
	updated, err := s.Training.UpdateWorkout(r.Context(), wk.ID, workout.UpdateWorkoutRequest{
		Steps: &restored, Indoor: &no, OutdoorSteps: &none, Description: &description,
	})
	if err != nil {
		s.logger().Error("could not revert a workout to outdoor", "workout", wk.ID, "err", err)
		s.fail(w, err)
		return
	}
	s.logger().Info("workout reverted to outdoor", "workout", wk.ID)
	writeJSON(w, http.StatusOK, workoutDTOFrom(updated))
}

// addNote appends note to a description. When the workout carries an
// automatic-adjustment marker the note goes in front of it instead: the UI
// shows everything after the marker verbatim as the reason for the
// adjustment, and an indoor note is not that.
func addNote(description, note string) string {
	if at := strings.Index(description, scheduler.AdjustedMarker); at >= 0 {
		return description[:at] + note + " " + description[at:]
	}
	if description == "" {
		return note
	}
	return strings.TrimRight(description, " ") + " " + note
}

// keepIndoor is what stops adaptation turning an indoor session back into a
// road one. Every place that replaces a workout's content (the step-down, the
// downgrade to an easy variant, the tomorrow-apply and FTP-test easing paths,
// which all end up in one of those two) builds the replacement as the usual
// outdoor session first and calls this with the update it is about to write.
// When the workout being replaced is indoor, the replacement is converted; the
// outdoor replacement becomes the new outdoor_steps, so "Back to outdoor"
// after an automatic easing gives the eased outdoor session, not the
// pre-easing one (the easing was decided on today's recovery and still applies
// outdoors).
//
// zone is the replacement's own zone, which is not always what the workout
// carries yet: a downgrade swaps the steps for an endurance session but leaves
// the stored zone alone, and the conversion's shortening and HR-to-power rules
// key off the zone of the steps they are given.
//
// It adds no marker: one automatic change per workout stays guaranteed by
// scheduler.AdjustedMarker, which the caller owns. A workout that is not indoor
// is left exactly as it was.
func (s *Server) keepIndoor(req *workout.UpdateWorkoutRequest, wk workout.Workout, profile workout.RiderProfile, zone workout.Zone) {
	if !wk.Indoor || req.Steps == nil {
		return
	}
	name := wk.Name
	if req.Name != nil {
		name = *req.Name
	}
	outdoor := append([]workout.WorkoutStep{}, *req.Steps...)
	res, err := indoor.Convert(workout.Workout{Sport: wk.Sport, Name: name, Zone: zone, Steps: outdoor}, profile)
	if err != nil {
		// Cannot happen for a cycling workout, and only cycling ones are made
		// indoor. If it ever does, the replacement is a plain outdoor session
		// and the flag must say so, not claim an indoor one it is not.
		s.logger().Warn("could not keep a workout indoor through an automatic change", "workout", wk.ID, "err", err)
		no := false
		var none []workout.WorkoutStep
		req.Indoor, req.OutdoorSteps = &no, &none
		return
	}
	yes := true
	req.Steps, req.Indoor, req.OutdoorSteps = &res.Steps, &yes, &outdoor
	s.logger().Info("indoor session kept through an automatic change", "workout", wk.ID)
}
