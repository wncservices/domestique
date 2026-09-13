package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/fitworkout"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
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
	FTPEstimated          bool     `json:"ftpEstimated,omitempty"`
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
		FTPWatts: p.FTPWatts, FTPEstimated: p.FTPEstimated, ThresholdPaceSecPerKM: p.ThresholdPaceSecPerKM,
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
	Adjusted    bool    `json:"adjusted,omitempty"`
}

type periodizationPlanDTO struct {
	GoalID     string                 `json:"goalId"`
	Weeks      []periodizationWeekDTO `json:"weeks"`
	Adjustment float64                `json:"adjustment,omitempty"`
}

// handleGoalPeriodization computes — on the fly, nothing persisted — the
// periodized phase structure (base/build/peak/taper, a weekly hours
// target) between today and a goal's event date, then reconciles it
// against the rider's own completed_sessions (internal/adapter.Reconcile —
// Phase D's adaptive-replanning loop): a rider who has been training under
// or over what recent weeks asked for gets upcoming weeks nudged to match,
// not a plan that keeps repeating a target already proven unrealistic.
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

	plan, _, err := s.reconciledPeriodizationPlan(r, g, identity.User)
	if err != nil {
		if err == periodization.ErrNoEventDate || err == periodization.ErrEventInThePast {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.fail(w, err)
		return
	}

	dto := periodizationPlanDTO{GoalID: plan.GoalID, Weeks: make([]periodizationWeekDTO, 0, len(plan.Weeks)), Adjustment: plan.Adjustment}
	for _, wk := range plan.Weeks {
		dto.Weeks = append(dto.Weeks, periodizationWeekDTO{
			Number: wk.Number, StartDate: wk.StartDate, Phase: string(wk.Phase),
			Recovery: wk.Recovery, TargetHours: wk.TargetHours, Adjusted: wk.Adjusted,
		})
	}
	writeJSON(w, http.StatusOK, dto)
}

// reconciledPeriodizationPlan is the one place both handleGoalPeriodization
// (display) and handleGoalSchedule (turning this week into real workouts)
// build a plan, so the hours a rider sees and the hours a schedule actually
// asks for can never drift apart from computing this two different ways.
// Also returns the profile it loaded along the way, since every caller
// needs it again right after (scheduler.NextWorkouts, or the DTO's own
// "fill in your profile" hint).
func (s *Server) reconciledPeriodizationPlan(r *http.Request, g workout.Goal, rider string) (periodization.Plan, workout.RiderProfile, error) {
	profile, _, err := s.Training.GetProfile(r.Context(), rider)
	if err != nil {
		return periodization.Plan{}, workout.RiderProfile{}, err
	}

	plan, err := periodization.BuildPlan(g, profile, time.Now())
	if err != nil {
		// ErrNoEventDate/ErrEventInThePast are the only errors BuildPlan
		// returns — both are the rider's own data being unsuitable to plan
		// from (no event date set, or it has already passed), not a server
		// problem. Returned as-is; the caller decides the status code.
		return periodization.Plan{}, profile, err
	}

	sessions, err := s.Training.ListSessions(r.Context(), rider)
	if err != nil {
		return periodization.Plan{}, profile, err
	}
	return adapter.Reconcile(g, profile, plan, sessions, time.Now()), profile, nil
}

type scheduledWorkoutsDTO struct {
	GoalID  string       `json:"goalId"`
	Created []workoutDTO `json:"created"`
	Skipped int          `json:"skipped,omitempty"`
}

// handleGoalSchedule turns the plan week containing today into concrete,
// dated workouts and persists them — internal/scheduler.NextWorkouts wired
// to storage, on top of the same reconciled plan handleGoalPeriodization
// shows a rider (see reconciledPeriodizationPlan's own comment on why one
// helper builds it for both). Safe to call more than once for the same
// week: a date that already has a workout tagged with this goal is left
// alone rather than duplicated — reconciliation only ever changes weeks
// that have not been scheduled yet (see adapter.Reconcile's own doc
// comment), so there is no already-scheduled week here whose workouts it
// would need to revise.
func (s *Server) handleGoalSchedule(w http.ResponseWriter, r *http.Request) {
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

	plan, profile, err := s.reconciledPeriodizationPlan(r, g, identity.User)
	if err != nil {
		if err == periodization.ErrNoEventDate || err == periodization.ErrEventInThePast {
			// ErrNoEventDate/ErrEventInThePast — see handleGoalPeriodization's
			// own comment on why these are the rider's own data, not a server
			// problem.
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.fail(w, err)
		return
	}

	requests, err := scheduler.NextWorkouts(plan, profile, identity.User, g.ID, g.Sport, time.Now())
	if err != nil {
		s.fail(w, err)
		return
	}

	existing, err := s.Training.ListWorkouts(r.Context(), identity.User)
	if err != nil {
		s.fail(w, err)
		return
	}
	alreadyScheduled := make(map[string]bool, len(existing))
	for _, wk := range existing {
		if wk.GoalID == g.ID {
			alreadyScheduled[wk.Date] = true
		}
	}

	created := make([]workoutDTO, 0, len(requests))
	skipped := 0
	for _, req := range requests {
		if alreadyScheduled[req.Date] {
			skipped++
			continue
		}
		wk, err := s.Training.CreateWorkout(r.Context(), req)
		if err != nil {
			s.fail(w, err)
			return
		}
		created = append(created, workoutDTOFrom(wk))
	}

	s.logger().Info("workouts scheduled", "goal", g.ID, "rider", identity.User, "created", len(created), "skipped", skipped)
	writeJSON(w, http.StatusOK, scheduledWorkoutsDTO{GoalID: g.ID, Created: created, Skipped: skipped})
}

// handleBuildFTPTest and handleBuildMaxHRTest each create one ordinary,
// plannable workout from internal/fitnesstest's fixed protocol — the "set
// up a test" answer for a rider whose profile has no number to estimate
// from (FTP) or that this app never attempts to estimate at all (max HR;
// see fitnesstest's own doc comment for why). Deliberately no
// already-exists check the way handleGoalSchedule has for a scheduled
// date: this is an explicit, one-off rider action with no natural key to
// dedupe against, the same as clicking "Build a workout" by hand — asking
// for a second one is not a mistake to guard against.
func (s *Server) handleBuildFTPTest(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	req := fitnesstest.FTPTestWorkout()
	req.Rider = rider
	wk, err := s.Training.CreateWorkout(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("ftp test workout built", "rider", rider, "id", wk.ID)
	writeJSON(w, http.StatusCreated, workoutDTOFrom(wk))
}

func (s *Server) handleBuildMaxHRTest(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	var body struct {
		Sport string `json:"sport"`
	}
	// A missing or invalid body just means "no sport stated" — this
	// endpoint takes no other input, so decode errors fall back to the
	// zero value rather than rejecting the request. model.Sport("")
	// defaults sensibly wherever it flows next (see model.Sport's own
	// zero-value handling elsewhere in this codebase).
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body)
	sport := model.SportCycling
	if body.Sport == string(model.SportRunning) {
		sport = model.SportRunning
	}

	rider := auth.FromContext(r.Context()).User
	req := fitnesstest.MaxHRTestWorkout(sport)
	req.Rider = rider
	wk, err := s.Training.CreateWorkout(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("max hr test workout built", "rider", rider, "id", wk.ID)
	writeJSON(w, http.StatusCreated, workoutDTOFrom(wk))
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
		// FTPEstimated is deliberately not read from body: this is the
		// manual save form, and a rider willing to click Save owns
		// whatever number sits in the field, estimated or not — see
		// RiderProfile.FTPEstimated's own doc comment on why that flag
		// only ever means "not yet looked at and confirmed."
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

// handlePushWorkoutToGarmin pushes a structured workout to the rider's own
// connected Garmin account — the automatic counterpart to
// handleDownloadWorkoutFIT's manual USB-copy path, now that
// internal/garmin.CreateWorkout exists (Connect's own JSON schema, not the
// FIT bytes the download endpoint produces — see that client's own doc
// comment). One-shot: this always creates a new Garmin workout, it does not
// yet track and update the one from a previous push for the same workout —
// pushing again makes a second entry on the account rather than replacing
// the first. Good enough for a first version; keeping them in sync on
// every edit is real design work (where does the remote id live, what
// happens if the rider deleted it on the Garmin side) left for later.
func (s *Server) handlePushWorkoutToGarmin(w http.ResponseWriter, r *http.Request) {
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

	if s.Garmin == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment has no Garmin connector configured",
		})
		return
	}
	_, session, ok := s.garminSessionFor(r)
	if !ok {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "connect your Garmin account in Settings first",
		})
		return
	}
	consumer, _ := s.garminConsumer()

	remoteID, err := s.Garmin.PushWorkout(r.Context(), consumer, session, wk.Name, string(wk.Sport), workout.FITSteps(wk.Steps))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("workout pushed to garmin", "workout", id, "garminWorkoutId", remoteID, "rider", identity.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "pushed", "garminWorkoutId": remoteID})
}

// ---------- Metrics ingestion (docs/training-plan.md Phase B1) ----------

type syncMetricsResultDTO struct {
	Synced int `json:"synced"`
	// Warnings names each provider that failed and why, without failing
	// the whole sync — the same "one bad thing never aborts the rest"
	// contract handleGarminCourseImport's own skipped map already keeps
	// for course imports, applied here across providers instead of across
	// individual items: a rider whose Wahoo token expired should still get
	// their Garmin activities recorded.
	Warnings []string `json:"warnings,omitempty"`
	// EstimatedFTPWatts is set only when this sync just produced a fresh
	// FTP estimate that got saved to the rider's profile — see
	// internal/fitnesstest.EstimateFTP and the call site below for exactly
	// when that happens.
	EstimatedFTPWatts float64 `json:"estimatedFtpWatts,omitempty"`
}

// handleSyncTrainingMetrics pulls recently completed activities from
// whichever of the rider's own Garmin/Wahoo accounts are connected,
// records them as completed_sessions, and recomputes the rider's whole
// CTL/ATL/TSB history from the result. A rider triggers this by hand today
// (a "Sync now" button) — an automatic background version is exactly the
// same scheduling need docs/plan.md's own still-open "scheduled reconcile"
// item already flags for route push, worth building once rather than
// twice when either gets picked up.
func (s *Server) handleSyncTrainingMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no rider in the session"})
		return
	}

	profile, _, err := s.Training.GetProfile(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}

	var warnings []string
	synced := 0

	if s.Garmin != nil {
		if session, ok := s.garminSessionForRider(rider); ok {
			consumer, _ := s.garminConsumer()
			activities, err := s.Garmin.ListActivities(r.Context(), consumer, session)
			if err != nil {
				warnings = append(warnings, "garmin: "+err.Error())
			} else {
				for _, a := range activities {
					if a.ID == "" || a.StartTime.IsZero() {
						continue
					}
					load := workout.TrainingLoad(a.DurationSeconds, a.AvgPowerWatts, a.AvgHR, profile)
					if _, err := s.Training.UpsertSession(r.Context(), workout.UpsertSessionRequest{
						Rider: rider, Provider: "garmin", ExternalID: a.ID, Sport: a.Sport,
						Date: a.StartTime.Format("2006-01-02"), DurationSeconds: a.DurationSeconds,
						DistanceM: a.DistanceM, AvgHR: a.AvgHR, AvgPowerWatts: a.AvgPowerWatts, TrainingLoad: load,
					}); err != nil {
						warnings = append(warnings, "garmin: recording a session: "+err.Error())
						continue
					}
					synced++
				}
			}
		}
	}

	if s.Wahoo != nil {
		token, err := s.wahooAccessToken(r.Context(), rider)
		if err != nil {
			warnings = append(warnings, "wahoo: "+err.Error())
		} else {
			workouts, err := s.Wahoo.ListWorkouts(r.Context(), token, 1, 30)
			if err != nil {
				warnings = append(warnings, "wahoo: "+err.Error())
			} else {
				for _, wk := range workouts {
					if wk.ID == "" || wk.Starts.IsZero() {
						continue
					}
					load := workout.TrainingLoad(wk.DurationSeconds, wk.AvgPowerWatts, wk.AvgHR, profile)
					if _, err := s.Training.UpsertSession(r.Context(), workout.UpsertSessionRequest{
						// Wahoo's completed-workout list does not carry a
						// sport this pass decodes (see internal/wahoo's own
						// doc comment) — cycling, the same "this is a
						// cycling library" default AGENTS.md already states
						// for Wahoo route push.
						Rider: rider, Provider: "wahoo", ExternalID: wk.ID, Sport: "cycling",
						Date: wk.Starts.Format("2006-01-02"), DurationSeconds: wk.DurationSeconds,
						DistanceM: wk.DistanceM, AvgHR: wk.AvgHR, AvgPowerWatts: wk.AvgPowerWatts, TrainingLoad: load,
					}); err != nil {
						warnings = append(warnings, "wahoo: recording a session: "+err.Error())
						continue
					}
					synced++
				}
			}
		}
	}

	if synced > 0 {
		if err := s.Training.RecomputeFitnessSnapshots(r.Context(), rider); err != nil {
			s.fail(w, err)
			return
		}
	}

	// Refresh the FTP estimate from whatever sessions are now on file —
	// but only into a field the rider has never confirmed by hand: 0 (never
	// set) or FTPEstimated (a previous estimate, safe to keep refining).
	// See RiderProfile.FTPEstimated's own doc comment for why a
	// rider-entered value is never touched here, synced or not.
	var estimatedFTP float64
	if synced > 0 && (profile.FTPWatts == 0 || profile.FTPEstimated) {
		sessions, err := s.Training.ListSessions(r.Context(), rider)
		if err != nil {
			s.fail(w, err)
			return
		}
		if watts, ok := fitnesstest.EstimateFTP(sessions); ok && watts != profile.FTPWatts {
			// GetProfile returns a zero-value RiderProfile (Rider == "")
			// when the rider has never saved one yet — has to be set back
			// before this save, or SaveProfile refuses it as ownerless.
			profile.Rider = rider
			profile.FTPWatts = watts
			profile.FTPEstimated = true
			if _, err := s.Training.SaveProfile(r.Context(), profile); err != nil {
				s.fail(w, err)
				return
			}
			estimatedFTP = watts
			s.logger().Info("ftp estimated from synced sessions", "rider", rider, "watts", watts)
		}
	}

	s.logger().Info("training metrics synced", "rider", rider, "synced", synced, "warnings", len(warnings))
	writeJSON(w, http.StatusOK, syncMetricsResultDTO{Synced: synced, Warnings: warnings, EstimatedFTPWatts: estimatedFTP})
}

type completedSessionDTO struct {
	ID              string  `json:"id"`
	Provider        string  `json:"provider"`
	Sport           string  `json:"sport"`
	Date            string  `json:"date"`
	DurationSeconds float64 `json:"durationSeconds"`
	DistanceM       float64 `json:"distanceM,omitempty"`
	AvgHR           int     `json:"avgHr,omitempty"`
	AvgPowerWatts   float64 `json:"avgPowerWatts,omitempty"`
	TrainingLoad    float64 `json:"trainingLoad"`
}

type fitnessSnapshotDTO struct {
	Date string  `json:"date"`
	CTL  float64 `json:"ctl"`
	ATL  float64 `json:"atl"`
	TSB  float64 `json:"tsb"`
}

type fitnessResponseDTO struct {
	// Snapshots is the whole CTL/ATL/TSB history, oldest first — what a
	// fitness chart plots directly.
	Snapshots []fitnessSnapshotDTO `json:"snapshots"`
	// Sessions is the underlying completed sessions the snapshots were
	// computed from, most recent first — what a "recent activity" list
	// shows below the chart.
	Sessions []completedSessionDTO `json:"sessions"`
}

// handleGetFitness returns a rider's own recorded fitness history — reads
// only, computed at sync time (handleSyncTrainingMetrics), not on every
// request.
func (s *Server) handleGetFitness(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User

	snapshots, err := s.Training.ListFitnessSnapshots(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	sessions, err := s.Training.ListSessions(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}

	dto := fitnessResponseDTO{
		Snapshots: make([]fitnessSnapshotDTO, 0, len(snapshots)),
		Sessions:  make([]completedSessionDTO, 0, len(sessions)),
	}
	for _, snap := range snapshots {
		dto.Snapshots = append(dto.Snapshots, fitnessSnapshotDTO{Date: snap.Date, CTL: snap.CTL, ATL: snap.ATL, TSB: snap.TSB})
	}
	for _, sess := range sessions {
		dto.Sessions = append(dto.Sessions, completedSessionDTO{
			ID: sess.ID, Provider: sess.Provider, Sport: sess.Sport, Date: sess.Date,
			DurationSeconds: sess.DurationSeconds, DistanceM: sess.DistanceM,
			AvgHR: sess.AvgHR, AvgPowerWatts: sess.AvgPowerWatts, TrainingLoad: sess.TrainingLoad,
		})
	}
	writeJSON(w, http.StatusOK, dto)
}
