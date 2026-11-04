package api

import (
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/weekplan"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const (
	moveNextNoFreeDayMessage = "No free day left this week. Open the plan to move it."
	moveNextNotTodayMessage  = "Only today's session can be moved with Not today."
	moveNextTestMessage      = "An FTP test keeps its day. Open the plan to move it."
	moveNextRiddenMessage    = "You have already ridden this session."
)

// handleMoveNext is the Today page's "Not today": it moves today's session to
// the first free day after today in this week, the same rule the adapter uses
// to make up a missed session (weekplan.NextFreeDay). The rider decides, so it
// is recorded as their own move, exactly as a date change through PATCH is;
// undo is that PATCH with the original date, which the response returns as
// `from`. Another rider's workout is a 404, so it does not confirm it exists.
func (s *Server) handleMoveNext(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(r.Context()).User

	wk, err := s.Training.GetWorkout(ctx, r.PathValue("id"))
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}
	if !isOwnTraining(auth.FromContext(ctx), wk.Rider) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": workout.ErrWorkoutNotFound.Error()})
		return
	}
	refuse := func(msg string) { writeJSON(w, http.StatusConflict, map[string]string{"error": msg}) }

	now := s.localNow()
	today := now.Format(dateLayout)
	switch {
	case wk.CrewRideID != "":
		refuse(crewRideMoveMessage)
		return
	case wk.TestProtocol != "":
		refuse(moveNextTestMessage)
		return
	case wk.Date != today:
		refuse(moveNextNotTodayMessage)
		return
	}

	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	links, err := s.Training.SessionLinks(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Ridden: a completed session of today that matches this workout, the way
	// ride analysis links one to its plan (a hand link wins over the automatic
	// match).
	var planned []workout.Workout
	for _, o := range workouts {
		if o.Date == today {
			planned = append(planned, o)
		}
	}
	for _, sess := range sessions {
		if sess.Date != today {
			continue
		}
		if m := plannedFor(sess, sess.DurationSeconds, planned, links); m != nil && m.ID == wk.ID {
			refuse(moveNextRiddenMessage)
			return
		}
	}

	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	blackout, err := s.blackoutFor(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	taken := map[string]bool{}
	for _, o := range workouts {
		if o.Date != "" {
			taken[o.Date] = true
		}
	}
	for d := range blackout {
		taken[d] = true
	}
	available := map[string]bool{}
	for _, d := range profile.AvailableDays {
		available[d] = true
	}
	weekEnd := periodization.MondayOf(now).AddDate(0, 0, 6).Format(dateLayout)
	tomorrow := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
	next, ok := weekplan.NextFreeDay(tomorrow, weekEnd, available, taken, func(string) bool { return false })
	if !ok {
		refuse(moveNextNoFreeDayMessage)
		return
	}

	req := workout.UpdateWorkoutRequest{Date: &next}
	req.Description = recordManualMove(wk, req)
	if req.Description != nil && *req.Description != wk.Description {
		flags := riderEditFlags(wk, req)
		req.Flags = &flags
	}
	updated, err := s.Training.UpdateWorkout(ctx, wk.ID, req)
	if err != nil {
		s.fail(w, err)
		return
	}

	s.logger().Info("workout moved to the next free day", "id", wk.ID, "by", rider)
	writeJSON(w, http.StatusOK, map[string]any{
		"workout": s.workoutDTOWithWhy(ctx, updated),
		"from":    today,
	})
}
