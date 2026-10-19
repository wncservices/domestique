package api

import (
	"encoding/json"
	"net/http"
	"unicode/utf8"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// maxCheckInNote is the longest note, in characters.
const maxCheckInNote = 500

// checkInDTO is the morning check-in on the wire; scores are 1-5 and 0 means
// not answered. See apps/web/src/api/types.ts's CheckIn, which must change
// alongside this one. The note is the rider's own words: it is never logged
// and never emailed.
type checkInDTO struct {
	Date         string `json:"date"`
	Legs         int    `json:"legs"`
	Mood         int    `json:"mood"`
	SleepQuality int    `json:"sleepQuality"`
	Note         string `json:"note"`
}

// handleGetCheckIn returns today's answers (training timezone), zeros when
// the rider has not answered. The rider is the session's.
func (s *Server) handleGetCheckIn(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	today := s.localToday()
	c, _, err := s.Training.GetCheckIn(r.Context(), rider, today)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkInDTO{Date: today, Legs: c.Legs, Mood: c.Mood, SleepQuality: c.SleepQuality, Note: c.Note})
}

// handlePutCheckIn replaces today's answers. Rider and date come from the
// session and the training timezone; a rider or date in the body is ignored.
func (s *Server) handlePutCheckIn(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	var body checkInDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	for _, v := range []int{body.Legs, body.Mood, body.SleepQuality} {
		if v < 0 || v > 5 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scores are 1 to 5, or 0 for not answered"})
			return
		}
	}
	if utf8.RuneCountInString(body.Note) > maxCheckInNote {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the note is at most 500 characters"})
		return
	}
	rider := auth.FromContext(r.Context()).User
	today := s.localToday()
	if err := s.Training.SaveCheckIn(r.Context(), workout.CheckIn{
		Rider: rider, Date: today, Legs: body.Legs, Mood: body.Mood, SleepQuality: body.SleepQuality, Note: body.Note,
	}); err != nil {
		s.fail(w, err)
		return
	}
	body.Date = today
	writeJSON(w, http.StatusOK, body)
}
