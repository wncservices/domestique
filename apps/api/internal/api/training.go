package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/fitworkout"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// trainingAvailable reports whether this deployment has a training store at
// all. Always true in practice — Server.Training is wired unconditionally in
// runServe, the same as Crew and Schedule — but every handler checks it
// rather than assuming, the same defensiveness crewAvailable's own doc
// comment argues for: a nil Server is a valid configuration in tests.
func (s *Server) trainingAvailable(w http.ResponseWriter) bool {
	if s.Training != nil {
		return true
	}
	s.logger().Error("training store unavailable — this should never happen outside tests")
	writeJSON(w, http.StatusPreconditionFailed, map[string]string{
		"error": "this deployment has no training store configured",
	})
	return false
}

// isOwnTraining reports whether identity may act on a goal or workout
// belonging to rider.
//
// Deliberately no admin bypass, unlike auth.Identity.CanEditRoute: a route
// is a shared library item an admin may need to fix on someone's behalf, but
// a goal, fitness profile or workout is a rider's own training and health-
// adjacent data — docs/training-plan.md's own "Security and privacy"
// section is explicit that admin's existing edit-anyone's-route authority
// should not silently extend to anyone's training data without a separate,
// deliberate decision to build a coach/crew visibility feature. There is no
// such feature yet, so for now: exactly the rider it belongs to, full stop.
func isOwnTraining(identity auth.Identity, rider string) bool {
	return rider != "" && strings.EqualFold(identity.User, rider)
}

func (s *Server) forbidTraining(w http.ResponseWriter, r *http.Request) {
	identity := auth.FromContext(r.Context())
	s.logger().Info("training ownership denied", "user", identity.User, "role", identity.Role, "path", r.URL.Path)
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": "this belongs to a different rider",
	})
}

// ---------- DTOs ----------
//
// Mirrored by hand in apps/web/src/api/types.ts — change them together, the
// same rule every other DTO in this file already follows.

type workoutStepDTO struct {
	Name       string           `json:"name"`
	Intensity  string           `json:"intensity,omitempty"`
	Duration   string           `json:"duration"`
	Seconds    float64          `json:"seconds,omitempty"`
	Meters     float64          `json:"meters,omitempty"`
	Target     string           `json:"target"`
	TargetLow  float64          `json:"targetLow,omitempty"`
	TargetHigh float64          `json:"targetHigh,omitempty"`
	Repeat     int              `json:"repeat,omitempty"`
	Steps      []workoutStepDTO `json:"steps,omitempty"`
}

func stepDTOFrom(s workout.WorkoutStep) workoutStepDTO {
	dto := workoutStepDTO{
		Name: s.Name, Intensity: string(s.Intensity), Duration: string(s.Duration),
		Seconds: s.Seconds, Meters: s.Meters, Target: string(s.Target),
		TargetLow: s.TargetLow, TargetHigh: s.TargetHigh, Repeat: s.Repeat,
	}
	for _, child := range s.Steps {
		dto.Steps = append(dto.Steps, stepDTOFrom(child))
	}
	return dto
}

func stepFromDTO(d workoutStepDTO) workout.WorkoutStep {
	s := workout.WorkoutStep{
		Name: d.Name, Intensity: workout.Intensity(d.Intensity), Duration: workout.DurationType(d.Duration),
		Seconds: d.Seconds, Meters: d.Meters, Target: workout.TargetType(d.Target),
		TargetLow: d.TargetLow, TargetHigh: d.TargetHigh, Repeat: d.Repeat,
	}
	for _, child := range d.Steps {
		s.Steps = append(s.Steps, stepFromDTO(child))
	}
	return s
}

type goalDTO struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Sport            string  `json:"sport"`
	EventDate        string  `json:"eventDate,omitempty"`
	Priority         string  `json:"priority"`
	TargetDistanceM  float64 `json:"targetDistanceM,omitempty"`
	TargetElevationM float64 `json:"targetElevationM,omitempty"`
	Notes            string  `json:"notes,omitempty"`
	CreatedAt        string  `json:"createdAt"`
	UpdatedAt        string  `json:"updatedAt"`
}

func goalDTOFrom(g workout.Goal) goalDTO {
	return goalDTO{
		ID: g.ID, Name: g.Name, Sport: string(g.Sport), EventDate: g.EventDate,
		Priority: string(g.Priority), TargetDistanceM: g.TargetDistanceM, TargetElevationM: g.TargetElevationM,
		Notes: g.Notes, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
}

type riderProfileDTO struct {
	FTPWatts              float64  `json:"ftpWatts,omitempty"`
	ThresholdPaceSecPerKM float64  `json:"thresholdPaceSecPerKm,omitempty"`
	MaxHR                 int      `json:"maxHr,omitempty"`
	RestingHR             int      `json:"restingHr,omitempty"`
	AvailableDays         []string `json:"availableDays,omitempty"`
	HoursPerAvailableDay  float64  `json:"hoursPerAvailableDay,omitempty"`
	ExperienceLevel       string   `json:"experienceLevel,omitempty"`
	UpdatedAt             string   `json:"updatedAt,omitempty"`
}

func profileDTOFrom(p workout.RiderProfile) riderProfileDTO {
	return riderProfileDTO{
		FTPWatts: p.FTPWatts, ThresholdPaceSecPerKM: p.ThresholdPaceSecPerKM,
		MaxHR: p.MaxHR, RestingHR: p.RestingHR, AvailableDays: p.AvailableDays,
		HoursPerAvailableDay: p.HoursPerAvailableDay, ExperienceLevel: p.ExperienceLevel,
		UpdatedAt: p.UpdatedAt,
	}
}

type workoutDTO struct {
	ID          string           `json:"id"`
	Sport       string           `json:"sport"`
	Name        string           `json:"name"`
	GoalID      string           `json:"goalId,omitempty"`
	Date        string           `json:"date,omitempty"`
	Description string           `json:"description,omitempty"`
	Steps       []workoutStepDTO `json:"steps"`
	CreatedAt   string           `json:"createdAt"`
	UpdatedAt   string           `json:"updatedAt"`
}

func workoutDTOFrom(w workout.Workout) workoutDTO {
	dto := workoutDTO{
		ID: w.ID, Sport: string(w.Sport), Name: w.Name, GoalID: w.GoalID, Date: w.Date,
		Description: w.Description, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
		Steps: make([]workoutStepDTO, 0, len(w.Steps)),
	}
	for _, s := range w.Steps {
		dto.Steps = append(dto.Steps, stepDTOFrom(s))
	}
	return dto
}

// maxTrainingBodyBytes bounds a goal/profile/workout request body. A
// workout's step list is the largest of the three but still trivially
// small text — this is generous headroom, not a real workout's actual size.
const maxTrainingBodyBytes = 1 << 18

// ---------- Goals ----------

func (s *Server) handleListGoals(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	goals, err := s.Training.ListGoals(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]goalDTO, 0, len(goals))
	for _, g := range goals {
		out = append(out, goalDTOFrom(g))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateGoal(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	var body struct {
		Name             string  `json:"name"`
		Sport            string  `json:"sport"`
		EventDate        string  `json:"eventDate"`
		Priority         string  `json:"priority"`
		TargetDistanceM  float64 `json:"targetDistanceM"`
		TargetElevationM float64 `json:"targetElevationM"`
		Notes            string  `json:"notes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	rider := auth.FromContext(r.Context()).User
	g, err := s.Training.CreateGoal(r.Context(), workout.CreateGoalRequest{
		Rider: rider, Name: body.Name, Sport: model.Sport(body.Sport), EventDate: body.EventDate,
		Priority: workout.Priority(body.Priority), TargetDistanceM: body.TargetDistanceM,
		TargetElevationM: body.TargetElevationM, Notes: body.Notes,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("goal created", "id", g.ID, "rider", rider)
	writeJSON(w, http.StatusCreated, goalDTOFrom(g))
}

func (s *Server) handleUpdateGoal(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	id := r.PathValue("id")
	g, err := s.Training.GetGoal(r.Context(), id)
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if !isOwnTraining(identity, g.Rider) {
		s.forbidTraining(w, r)
		return
	}

	var body struct {
		Name             *string  `json:"name"`
		Sport            *string  `json:"sport"`
		EventDate        *string  `json:"eventDate"`
		Priority         *string  `json:"priority"`
		TargetDistanceM  *float64 `json:"targetDistanceM"`
		TargetElevationM *float64 `json:"targetElevationM"`
		Notes            *string  `json:"notes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	req := workout.UpdateGoalRequest{
		Name: body.Name, EventDate: body.EventDate,
		TargetDistanceM: body.TargetDistanceM, TargetElevationM: body.TargetElevationM, Notes: body.Notes,
	}
	if body.Sport != nil {
		sport := model.Sport(*body.Sport)
		req.Sport = &sport
	}
	if body.Priority != nil {
		priority := workout.Priority(*body.Priority)
		req.Priority = &priority
	}

	updated, err := s.Training.UpdateGoal(r.Context(), id, req)
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}

	s.logger().Info("goal updated", "id", id, "by", identity.User)
	writeJSON(w, http.StatusOK, goalDTOFrom(updated))
}

func (s *Server) handleDeleteGoal(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	id := r.PathValue("id")
	g, err := s.Training.GetGoal(r.Context(), id)
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if !isOwnTraining(identity, g.Rider) {
		s.forbidTraining(w, r)
		return
	}

	if err := s.Training.DeleteGoal(r.Context(), id); err != nil {
		s.failTrainingLookup(w, err)
		return
	}

	s.logger().Info("goal deleted", "id", id, "by", identity.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

type periodizationWeekDTO struct {
	Number      int     `json:"number"`
	StartDate   string  `json:"startDate"`
	Phase       string  `json:"phase"`
	Recovery    bool    `json:"recovery,omitempty"`
	TargetHours float64 `json:"targetHours,omitempty"`
}

type periodizationPlanDTO struct {
	GoalID string                 `json:"goalId"`
	Weeks  []periodizationWeekDTO `json:"weeks"`
}

// handleGoalPeriodization computes — on the fly, nothing persisted — the
// periodized phase structure (base/build/peak/taper, a weekly hours
// target) between today and a goal's event date. See
// internal/periodization's own doc comment for what this is and isn't:
// phase/volume structure, not concrete daily workouts yet.
func (s *Server) handleGoalPeriodization(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	id := r.PathValue("id")
	g, err := s.Training.GetGoal(r.Context(), id)
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if !isOwnTraining(identity, g.Rider) {
		s.forbidTraining(w, r)
		return
	}

	profile, _, err := s.Training.GetProfile(r.Context(), identity.User)
	if err != nil {
		s.fail(w, err)
		return
	}

	plan, err := periodization.BuildPlan(g, profile, time.Now())
	if err != nil {
		// ErrNoEventDate/ErrEventInThePast are the only errors BuildPlan
		// returns — both are the rider's own data being unsuitable to plan
		// from (no event date set, or it has already passed), not a server
		// problem.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	dto := periodizationPlanDTO{GoalID: plan.GoalID, Weeks: make([]periodizationWeekDTO, 0, len(plan.Weeks))}
	for _, wk := range plan.Weeks {
		dto.Weeks = append(dto.Weeks, periodizationWeekDTO{
			Number: wk.Number, StartDate: wk.StartDate, Phase: string(wk.Phase),
			Recovery: wk.Recovery, TargetHours: wk.TargetHours,
		})
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) failTrainingLookup(w http.ResponseWriter, err error) {
	if errors.Is(err, workout.ErrGoalNotFound) || errors.Is(err, workout.ErrWorkoutNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.fail(w, err)
}

// ---------- Rider profile ----------

func (s *Server) handleGetRiderProfile(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	profile, _, err := s.Training.GetProfile(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	// ok=false (no profile saved yet) is not an error — a rider setting one
	// up for the first time gets the zero-value DTO back, every numeric
	// field already documented as "0 means unset" on workout.RiderProfile.
	writeJSON(w, http.StatusOK, profileDTOFrom(profile))
}

func (s *Server) handleSaveRiderProfile(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	var body riderProfileDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	rider := auth.FromContext(r.Context()).User
	saved, err := s.Training.SaveProfile(r.Context(), workout.RiderProfile{
		Rider: rider, FTPWatts: body.FTPWatts, ThresholdPaceSecPerKM: body.ThresholdPaceSecPerKM,
		MaxHR: body.MaxHR, RestingHR: body.RestingHR, AvailableDays: body.AvailableDays,
		HoursPerAvailableDay: body.HoursPerAvailableDay, ExperienceLevel: body.ExperienceLevel,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("rider profile saved", "rider", rider)
	writeJSON(w, http.StatusOK, profileDTOFrom(saved))
}

// ---------- Workouts ----------

func (s *Server) handleListWorkouts(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	workouts, err := s.Training.ListWorkouts(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]workoutDTO, 0, len(workouts))
	for _, wk := range workouts {
		out = append(out, workoutDTOFrom(wk))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetWorkout(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	wk, err := s.Training.GetWorkout(r.Context(), r.PathValue("id"))
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}
	if !isOwnTraining(auth.FromContext(r.Context()), wk.Rider) {
		s.forbidTraining(w, r)
		return
	}
	writeJSON(w, http.StatusOK, workoutDTOFrom(wk))
}

type workoutRequestBody struct {
	Sport       string           `json:"sport"`
	Name        string           `json:"name"`
	GoalID      string           `json:"goalId"`
	Date        string           `json:"date"`
	Description string           `json:"description"`
	Steps       []workoutStepDTO `json:"steps"`
}

func stepsFromDTOs(dtos []workoutStepDTO) []workout.WorkoutStep {
	steps := make([]workout.WorkoutStep, 0, len(dtos))
	for _, d := range dtos {
		steps = append(steps, stepFromDTO(d))
	}
	return steps
}

func (s *Server) handleCreateWorkout(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	var body workoutRequestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	rider := auth.FromContext(r.Context()).User
	wk, err := s.Training.CreateWorkout(r.Context(), workout.CreateWorkoutRequest{
		Rider: rider, Sport: model.Sport(body.Sport), Name: body.Name, GoalID: body.GoalID,
		Date: body.Date, Description: body.Description, Steps: stepsFromDTOs(body.Steps),
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("workout created", "id", wk.ID, "rider", rider)
	writeJSON(w, http.StatusCreated, workoutDTOFrom(wk))
}

func (s *Server) handleUpdateWorkout(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	id := r.PathValue("id")
	wk, err := s.Training.GetWorkout(r.Context(), id)
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if !isOwnTraining(identity, wk.Rider) {
		s.forbidTraining(w, r)
		return
	}

	var body struct {
		Sport       *string           `json:"sport"`
		Name        *string           `json:"name"`
		GoalID      *string           `json:"goalId"`
		Date        *string           `json:"date"`
		Description *string           `json:"description"`
		Steps       *[]workoutStepDTO `json:"steps"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	req := workout.UpdateWorkoutRequest{
		Name: body.Name, GoalID: body.GoalID, Date: body.Date, Description: body.Description,
	}
	if body.Sport != nil {
		sport := model.Sport(*body.Sport)
		req.Sport = &sport
	}
	if body.Steps != nil {
		steps := stepsFromDTOs(*body.Steps)
		req.Steps = &steps
	}

	updated, err := s.Training.UpdateWorkout(r.Context(), id, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("workout updated", "id", id, "by", identity.User)
	writeJSON(w, http.StatusOK, workoutDTOFrom(updated))
}

func (s *Server) handleDeleteWorkout(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	id := r.PathValue("id")
	wk, err := s.Training.GetWorkout(r.Context(), id)
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}
	identity := auth.FromContext(r.Context())
	if !isOwnTraining(identity, wk.Rider) {
		s.forbidTraining(w, r)
		return
	}

	if err := s.Training.DeleteWorkout(r.Context(), id); err != nil {
		s.failTrainingLookup(w, err)
		return
	}

	s.logger().Info("workout deleted", "id", id, "by", identity.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleDownloadWorkoutFIT converts a workout to a FIT workout file on the
// fly, the same "download it, copy it to a device over USB" role
// handleDownloadFIT already plays for a route's FIT *course* — see
// internal/fitworkout's own doc comment for why this is the only proven
// path to a real device in this phase: neither provider's real push
// mechanism takes a client-supplied FIT workout file yet.
func (s *Server) handleDownloadWorkoutFIT(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	id := r.PathValue("id")
	wk, err := s.Training.GetWorkout(r.Context(), id)
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}
	if !isOwnTraining(auth.FromContext(r.Context()), wk.Rider) {
		s.forbidTraining(w, r)
		return
	}

	fitBytes, err := fitworkout.Encode(workout.FITSteps(wk.Steps), fitworkout.Options{
		Name:  wk.Name,
		Sport: fitworkout.SportFromString(string(wk.Sport)),
	})
	if err != nil {
		// A step combination validateSteps allowed but fitworkout.Encode
		// still refuses (e.g. a target with no low/high) is a data problem
		// the rider can fix, not a server bug.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeFITAttachment(s.logger(), w, wk.ID, fitBytes)
}
