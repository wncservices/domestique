package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/projection"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// goalCheckDTO is the answer to "is this goal realistic?". Key is one of the
// four projection.FeasibilityKey values, "needs_profile" or "none" (nothing to
// say); the UI keys off it, never off the message.
type goalCheckDTO struct {
	Key     string `json:"key"`
	Message string `json:"message,omitempty"`
	Tone    string `json:"tone,omitempty"`
}

type goalCheckRequest struct {
	EventDate        string  `json:"eventDate"`
	Sport            string  `json:"sport"`
	TargetDistanceM  float64 `json:"targetDistanceM"`
	TargetElevationM float64 `json:"targetElevationM"`
	Priority         string  `json:"priority"`
}

// handleGoalCheck judges a goal that is still being typed into the Add/Edit
// panel, against the plan the rider would get if they saved it. It builds the
// same plan every other reader does (reconciledPeriodizationPlan) from an
// unsaved goal and writes nothing. The rider is the session's, never the body.
func (s *Server) handleGoalCheck(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	var body goalCheckRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	rider := auth.FromContext(r.Context()).User
	answer := func(d goalCheckDTO) {
		s.logger().Info("goal feasibility", "rider", rider, "key", d.Key)
		writeJSON(w, http.StatusOK, d)
	}
	none := goalCheckDTO{Key: "none"}

	// Cycling only for now: the load model behind the verdict is power-based.
	if body.Sport != "" && body.Sport != string(model.SportCycling) {
		answer(none)
		return
	}
	if _, err := time.Parse(dateLayout, body.EventDate); err != nil {
		answer(none)
		return
	}
	priority := workout.Priority(body.Priority)
	if priority == "" {
		priority = workout.PriorityA
	}
	g := workout.Goal{
		Sport: model.SportCycling, EventDate: body.EventDate, Priority: priority,
		TargetDistanceM: body.TargetDistanceM, TargetElevationM: body.TargetElevationM,
	}
	plan, profile, err := s.reconciledPeriodizationPlan(r.Context(), g, rider)
	if err != nil {
		if errors.Is(err, periodization.ErrNoEventDate) || errors.Is(err, periodization.ErrEventInThePast) {
			answer(none)
			return
		}
		s.logger().Error("goal feasibility failed", "rider", rider, "stage", "plan", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	if !(profile.FTPWatts > 0) || !(profile.HoursPerAvailableDay*float64(len(profile.AvailableDays)) > 0) {
		answer(goalCheckDTO{Key: "needs_profile", Message: "Add your FTP and available hours to see if this is realistic.", Tone: "neutral"})
		return
	}
	if plan.LoadBasis != "ctl" || plan.TargetCTL <= 0 || len(plan.Weeks) == 0 {
		answer(none)
		return
	}

	// The fitness reached is the CTL at the start of the first taper week: the
	// build is over by then. Without a taper, the last week stands in.
	reached := plan.Weeks[len(plan.Weeks)-1].PlannedCTL
	capped := false
	for _, wk := range plan.Weeks {
		if wk.Phase == periodization.PhaseTaper {
			reached = wk.PlannedCTL
			break
		}
		capped = capped || wk.LoadCapped
	}
	v := projection.JudgeFeasibility(projection.FeasibilityInput{
		TargetCTL: plan.TargetCTL, Shortfall: plan.Shortfall, ReachedCTL: reached,
		HoursCapped: capped, Weeks: len(plan.Weeks),
	})
	answer(goalCheckDTO{Key: string(v.Key), Message: v.Message, Tone: v.Tone})
}
