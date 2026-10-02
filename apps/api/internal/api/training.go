package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/fitworkout"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/progression"
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
	ThresholdHR           int      `json:"thresholdHr,omitempty"`
	RestingHR             int      `json:"restingHr,omitempty"`
	AvailableDays         []string `json:"availableDays,omitempty"`
	HoursPerAvailableDay  float64  `json:"hoursPerAvailableDay,omitempty"`
	ExperienceLevel       string   `json:"experienceLevel,omitempty"`
	// AutoPushWorkouts is the rider's standing permission to place their
	// scheduled workouts on their Garmin account automatically.
	AutoPushWorkouts bool `json:"autoPushWorkouts,omitempty"`
	// SmartTrainer is "I have a smart trainer": it gates the ERG wording and
	// the midpoint collapse of an indoor version's power ranges.
	SmartTrainer bool `json:"smartTrainer,omitempty"`
	// Estimated names the fields above (other than FTP, which has its own
	// flag) that were filled in automatically and not yet confirmed by the
	// rider. Output only — handleSaveRiderProfile never reads it back.
	Estimated []string `json:"estimated,omitempty"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
	// LevelsRecalibrated is output only, and only on the response to a save
	// that lowered the rider's progression levels for a new FTP.
	// handleSaveRiderProfile never reads it back from the request.
	LevelsRecalibrated *levelsRecalibratedDTO `json:"levelsRecalibrated,omitempty"`
}

func profileDTOFrom(p workout.RiderProfile) riderProfileDTO {
	return riderProfileDTO{
		FTPWatts: p.FTPWatts, FTPEstimated: p.FTPEstimated, ThresholdPaceSecPerKM: p.ThresholdPaceSecPerKM,
		MaxHR: p.MaxHR, ThresholdHR: p.ThresholdHR, RestingHR: p.RestingHR, AvailableDays: p.AvailableDays,
		HoursPerAvailableDay: p.HoursPerAvailableDay, ExperienceLevel: p.ExperienceLevel,
		Estimated: p.Estimated, AutoPushWorkouts: p.AutoPushWorkouts, SmartTrainer: p.SmartTrainer, UpdatedAt: p.UpdatedAt,
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
	// Zone and Level are omitted when unset — "" and 0 — matching every
	// other optional field on this DTO, and are what a workout made before
	// this column existed always carries.
	Zone  string  `json:"zone,omitempty"`
	Level float64 `json:"level,omitempty"`
	// TestProtocol is set on an FTP test workout ("ramp", "twenty_minute",
	// "two_by_eight"); TestResultWatts is the FTP its ride measured, omitted
	// until a result has been captured.
	TestProtocol    string  `json:"testProtocol,omitempty"`
	TestResultWatts float64 `json:"testResultWatts,omitempty"`
	// TestUnreadable is set when the test ride was read and gave no result, so
	// the day card can say so instead of showing nothing.
	TestUnreadable bool `json:"testUnreadable,omitempty"`
	// Indoor marks the trainer version of a session. OutdoorPlannedSeconds is
	// the length the "Back to outdoor version" action restores, omitted unless
	// there is an original to restore and it is time-based.
	Indoor                bool    `json:"indoor,omitempty"`
	OutdoorPlannedSeconds float64 `json:"outdoorPlannedSeconds,omitempty"`
	// CanRevertIndoor is whether "Back to outdoor version" would do anything:
	// an indoor workout that still has its outdoor steps. A manual edit of the
	// steps or the sport retires them (see handleUpdateWorkout).
	CanRevertIndoor bool `json:"canRevertIndoor,omitempty"`
	// Swapped is true once the rider swapped this session for an alternate
	// (scheduler.SwappedMarker in the description); HasPlannedSnapshot is
	// whether "Back to planned version" would do anything.
	Swapped            bool `json:"swapped,omitempty"`
	HasPlannedSnapshot bool `json:"hasPlannedSnapshot,omitempty"`
	// So the UI can show "1h 15m" without re-implementing repeat-block arithmetic.
	PlannedSeconds float64 `json:"plannedSeconds"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
	// Why is the structured reason behind the workout's latest automatic
	// change, when one was recorded. Filled by attachWhy, one batched read
	// per response; a workout adjusted before reasons were recorded has none
	// and the client falls back to the note in Description.
	Why *whyDTO `json:"why,omitempty"`
}

func workoutDTOFrom(w workout.Workout) workoutDTO {
	dto := workoutDTO{
		ID: w.ID, Sport: string(w.Sport), Name: w.Name, GoalID: w.GoalID, Date: w.Date,
		Description: w.Description, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
		Zone: string(w.Zone), Level: w.Level,
		TestProtocol:   w.TestProtocol,
		Indoor:         w.Indoor,
		PlannedSeconds: workout.PlannedSeconds(w.Steps),
		Steps:          make([]workoutStepDTO, 0, len(w.Steps)),
	}
	for _, s := range w.Steps {
		dto.Steps = append(dto.Steps, stepDTOFrom(s))
	}
	// The unreadable marker is bookkeeping, not a result to show.
	if w.TestResultWatts > 0 {
		dto.TestResultWatts = w.TestResultWatts
	}
	dto.TestUnreadable = w.TestResultWatts < 0
	if w.OutdoorSteps != nil {
		dto.OutdoorPlannedSeconds = workout.PlannedSeconds(*w.OutdoorSteps)
	}
	dto.CanRevertIndoor = w.Indoor && w.OutdoorSteps != nil
	dto.Swapped = strings.Contains(w.Description, scheduler.SwappedMarker)
	dto.HasPlannedSnapshot = w.PlannedSnapshot != nil
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
	// Before the response, this week; after it, the rest of the season.
	s.planGoalNow(r.Context(), g)
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
	// A new event date or sport can add weeks to the plan.
	s.planGoalNow(r.Context(), updated)
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

	// Not while a season pass is writing this goal's weeks, or the pass could
	// record one after the goal has forgotten them all.
	s.seasonMu.Lock()
	err = s.Training.DeleteGoal(r.Context(), id)
	s.seasonMu.Unlock()
	if err != nil {
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
	TotalWeeks int                    `json:"totalWeeks,omitempty"`
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

	plan, _, err := s.reconciledPeriodizationPlan(r.Context(), g, identity.User)
	if err != nil {
		if err == periodization.ErrNoEventDate || err == periodization.ErrEventInThePast {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.fail(w, err)
		return
	}

	dto := periodizationPlanDTO{GoalID: plan.GoalID, TotalWeeks: plan.TotalWeeks, Weeks: make([]periodizationWeekDTO, 0, len(plan.Weeks)), Adjustment: plan.Adjustment}
	for _, wk := range plan.Weeks {
		dto.Weeks = append(dto.Weeks, periodizationWeekDTO{
			Number: wk.Number, StartDate: wk.StartDate, Phase: string(wk.Phase),
			Recovery: wk.Recovery, TargetHours: wk.TargetHours, Adjusted: wk.Adjusted,
		})
	}
	writeJSON(w, http.StatusOK, dto)
}

// reconciledPeriodizationPlan is the one place handleGoalPeriodization
// (display), handleGoalSchedule (turning this week into real workouts, via
// scheduleGoal) and AutoScheduleTick (the unattended weekly equivalent of
// handleGoalSchedule — see autoschedule.go) all build a plan, so the hours
// a rider sees and the hours a schedule actually asks for can never drift
// apart from computing this two different ways. Takes a bare
// context.Context rather than *http.Request so the background job can call
// it too, with no request of its own to hold one. Also returns the profile
// it loaded along the way, since every caller needs it again right after
// (scheduler.NextWorkouts, or the DTO's own "fill in your profile" hint).
func (s *Server) reconciledPeriodizationPlan(ctx context.Context, g workout.Goal, rider string) (periodization.Plan, workout.RiderProfile, error) {
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return periodization.Plan{}, workout.RiderProfile{}, err
	}

	plan, err := periodization.Build(g, profile, s.now())
	if err != nil {
		// ErrNoEventDate/ErrEventInThePast are the only errors BuildPlan
		// returns — both are the rider's own data being unsuitable to plan
		// from (no event date set, or it has already passed), not a server
		// problem. Returned as-is; the caller decides the status code.
		return periodization.Plan{}, profile, err
	}

	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return periodization.Plan{}, profile, err
	}
	return adapter.Reconcile(g, profile, plan, sessions, s.now()), profile, nil
}

type scheduledWorkoutsDTO struct {
	GoalID  string       `json:"goalId"`
	Created []workoutDTO `json:"created"`
	Skipped int          `json:"skipped,omitempty"`
}

// handleGoalSchedule turns the plan week containing today into concrete,
// dated workouts and persists them — a thin HTTP wrapper around
// scheduleGoal, which does the actual work and is shared with
// AutoScheduleTick's unattended equivalent (see autoschedule.go).
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

	// The body is optional: none (or no weekStart) means the current week, as
	// this endpoint always did. A weekStart names the week the rider is
	// looking at on the Plan page; it may be this week or a later one.
	var body struct {
		WeekStart string `json:"weekStart"`
	}
	// io.EOF is an empty body (chunked, so ContentLength says nothing): the
	// current week, not an error.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	thisMonday := periodization.MondayOf(s.now())
	weekStart := thisMonday
	if body.WeekStart != "" {
		parsed, err := time.Parse(dateLayout, body.WeekStart)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "weekStart must be a date, YYYY-MM-DD"})
			return
		}
		// Normalised to that week's Monday, so any day of the week works.
		weekStart = periodization.MondayOf(parsed)
		if weekStart.Format(dateLayout) < thisMonday.Format(dateLayout) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a week that has already passed cannot be scheduled"})
			return
		}
	}

	created, skipped, err := s.scheduleGoalWeek(r.Context(), g, weekStart, "")
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

	dtos := make([]workoutDTO, 0, len(created))
	for _, wk := range created {
		dtos = append(dtos, workoutDTOFrom(wk))
	}
	s.logger().Info("workouts scheduled", "goal", g.ID, "rider", g.Rider, "created", len(dtos), "skipped", skipped)
	writeJSON(w, http.StatusOK, scheduledWorkoutsDTO{GoalID: g.ID, Created: dtos, Skipped: skipped})
}

// levelsFor returns rider's zone -> level map for sport, for
// scheduler.WeekWorkouts/NextWorkouts to pick structured sessions from. The
// first time a goal for this sport is scheduled, the rider has no levels
// saved yet — this is where they are initialised, from
// progression.Initial(profile.ExperienceLevel, sport), and persisted via
// SaveLevel, so a rider's levels start somewhere sensible and stay there
// across every subsequent schedule call rather than being recomputed (and
// silently drifting) each time.
func (s *Server) levelsFor(ctx context.Context, rider string, profile workout.RiderProfile, sport model.Sport) (map[string]float64, error) {
	existing, err := s.Training.ListLevels(ctx, rider)
	if err != nil {
		return nil, err
	}

	levels := make(map[string]float64)
	found := false
	for _, l := range existing {
		if l.Sport != sport {
			continue
		}
		levels[string(l.Zone)] = l.Level
		found = true
	}
	if found {
		return levels, nil
	}

	for _, l := range progression.Initial(profile.ExperienceLevel, sport) {
		if err := s.Training.SaveLevel(ctx, workout.ProgressionLevel{
			Rider:  rider,
			Sport:  sport,
			Zone:   workout.Zone(l.Zone),
			Level:  l.Value,
			Reason: "Starting level from your experience",
		}); err != nil {
			return nil, err
		}
		levels[l.Zone] = l.Value
	}
	return levels, nil
}

// scheduleGoal turns g's current plan week into concrete, dated workouts
// and persists them — internal/scheduler.NextWorkouts wired to storage, on
// top of the same reconciled plan handleGoalPeriodization shows a rider
// (see reconciledPeriodizationPlan's own comment on why one helper builds
// it for both). Safe to call more than once for the same week: a date
// that already has a workout tagged with this goal is left alone rather
// than duplicated — reconciliation only ever changes weeks that have not
// been scheduled yet (see adapter.Reconcile's own doc comment), so there
// is no already-scheduled week here whose workouts it would need to
// revise. Shared by handleGoalSchedule (a rider's own "Schedule this
// week's workouts" click), AutoScheduleTick (the unattended weekly
// equivalent, see autoschedule.go) and handleReplan (see replan.go) — one
// implementation, so a rider gets exactly the same workouts whichever path
// creates them.
//
// fromDate, when non-empty, drops any request dated before it before
// persisting — how replan asks for "the rest of this week" without
// resurrecting a day already in the past. handleGoalSchedule and
// AutoScheduleTick both pass "", keeping their own behaviour exactly what
// it was before this parameter existed.
func (s *Server) scheduleGoal(ctx context.Context, g workout.Goal, fromDate string) ([]workout.Workout, int, error) {
	return s.scheduleGoalWeek(ctx, g, periodization.MondayOf(s.now()), fromDate)
}

// scheduleGoalWeek is scheduleGoal for the plan week starting on weekStart (a
// Monday) — this week, or a later one. It is the explicit path: the Plan
// page's Fill button and Replan. It tops up any date that has no workout yet,
// so a rider who asks for it gets a gap filled, and it records the week as
// filled. A weekStart the plan has no week for (before this week, or past the
// event) creates nothing and is not an error.
//
// The week comes from the plan built from *today*, so a later week carries
// its own phase, recovery flag and target hours (and the compliance
// adjustment Reconcile applies to every week after the first) — never a copy
// of this week's. Levels are the rider's levels now; a week built ahead can
// therefore be one progression step behind the one built on its own Monday.
func (s *Server) scheduleGoalWeek(ctx context.Context, g workout.Goal, weekStart time.Time, fromDate string) ([]workout.Workout, int, error) {
	sc, err := s.seasonContext(ctx, g)
	if err != nil {
		return nil, 0, err
	}
	week, found := sc.week(weekStart.Format(dateLayout))
	if !found {
		return nil, 0, nil
	}
	existing, err := s.Training.ListWorkouts(ctx, g.Rider)
	if err != nil {
		return nil, 0, err
	}
	return s.fillWeek(ctx, g, sc, week, existing, fromDate)
}

// seasonContext is everything filling or refreshing any week of g's plan
// needs, read once: the reconciled plan, the rider's profile and their levels.
// A pass over a whole season builds it once instead of once per week.
type seasonContext struct {
	plan    periodization.Plan
	profile workout.RiderProfile
	levels  map[string]float64
}

func (s *Server) seasonContext(ctx context.Context, g workout.Goal) (seasonContext, error) {
	plan, profile, err := s.reconciledPeriodizationPlan(ctx, g, g.Rider)
	if err != nil {
		return seasonContext{}, err
	}
	levels, err := s.levelsFor(ctx, g.Rider, profile, g.Sport)
	if err != nil {
		return seasonContext{}, err
	}
	return seasonContext{plan: plan, profile: profile, levels: levels}, nil
}

func (sc seasonContext) week(startStr string) (periodization.Week, bool) {
	for _, w := range sc.plan.Weeks {
		if w.StartDate == startStr {
			return w, true
		}
	}
	return periodization.Week{}, false
}

// fillWeek creates the sessions of one plan week that have no workout on their
// date yet, and records the week as filled. existing is the rider's workouts
// as they stand.
//
// A week that yields no sessions at all (the rider has not said which days
// they train, or how long) is *not* recorded: nothing was filled, and
// recording it would leave the week empty for good once the profile is filled
// in, because the tick never tops up a week it has recorded. That matters far
// more now that a whole season is recorded in one go.
//
// A week that is this week or next is built from data as fresh as it will ever
// be, so it is recorded as refreshed too; a week further out is refreshed when
// it gets that close (see refreshWeek).
func (s *Server) fillWeek(ctx context.Context, g workout.Goal, sc seasonContext, week periodization.Week, existing []workout.Workout, fromDate string) ([]workout.Workout, int, error) {
	requests, err := scheduler.WeekWorkouts(week, sc.profile, sc.levels, g.Rider, g.ID, g.Sport)
	if err != nil {
		return nil, 0, err
	}
	if len(requests) == 0 {
		return nil, 0, nil
	}

	// A date is taken if *any* goal has already put a workout on it, not
	// just this one: a rider with a race on the calendar and a general
	// fitness goal must not get two sessions on the same Tuesday. Whichever
	// goal schedules a day first keeps it, and AutoScheduleTick visits dated
	// goals first (see workout.DB.ListAllGoals). A workout the rider built by
	// hand carries no goal and blocks nothing.
	alreadyScheduled := make(map[string]bool, len(existing))
	// A day a life event covers is taken too: the rider is away, and a session
	// a life event removed has to stay removed whatever builds the week.
	blackout, err := s.blackoutFor(ctx, g.Rider)
	if err != nil {
		return nil, 0, err
	}
	for d := range blackout {
		alreadyScheduled[d] = true
	}
	for _, wk := range existing {
		if wk.GoalID != "" {
			alreadyScheduled[wk.Date] = true
			// A workout moved to another day by an automatic adjustment
			// leaves its original day empty. That day is not free: it is
			// where the session was planned, and scheduling must not read the
			// gap and plan it again on top of the moved one.
			if from, ok := scheduler.MovedFrom(wk.Description); ok {
				alreadyScheduled[from] = true
			}
		}
	}

	// Never a session on a day that has already gone by. The adapter reads a
	// dated session nobody rode as missed and "makes it up" by replacing a
	// later easy day, so filling Monday and Tuesday on a Wednesday would
	// hand the rider a phantom miss and a reshuffled week. Every caller
	// funnels through here, so the floor lives here rather than in each of
	// them; a later week is entirely after today and is unaffected. The
	// skipped days' share of the week's hours is not moved onto the days
	// left: WeekWorkouts sizes each session from the whole week, so a
	// mid-week fill is a lighter week, which is what it is.
	if today := s.now().Format(dateLayout); fromDate < today {
		fromDate = today
	}

	created := make([]workout.Workout, 0, len(requests))
	skipped := 0
	for _, req := range requests {
		if req.Date < fromDate {
			continue
		}
		if alreadyScheduled[req.Date] {
			skipped++
			continue
		}
		wk, err := s.Training.CreateWorkout(ctx, req)
		if err != nil {
			return created, skipped, err
		}
		created = append(created, wk)
	}
	if err := s.Training.MarkWeekScheduled(ctx, g.ID, week.StartDate); err != nil {
		return created, skipped, err
	}
	if week.StartDate <= s.refreshHorizon() {
		if err := s.Training.MarkWeekRefreshed(ctx, g.ID, week.StartDate); err != nil {
			return created, skipped, err
		}
	}
	return created, skipped, nil
}

// refreshHorizon is the last Monday, "YYYY-MM-DD", whose week counts as close
// enough to be built from current levels and FTP: next week's.
func (s *Server) refreshHorizon() string {
	return periodization.MondayOf(s.now()).AddDate(0, 0, 7).Format(dateLayout)
}

// autoScheduleGoalWeek is what the unattended tick calls: it fills a goal's
// week only if it has never been filled — no scheduled_weeks row and no
// workout of this goal already in it — and then records it. The tick never
// tops up a week it filled before, so a session the rider deleted, moved or
// rewrote stays that way, including on the Monday the week becomes current.
// A workout already in the week counts as filled (and is recorded), which is
// how a deployment that predates scheduled_weeks needs no backfill.
func (s *Server) autoScheduleGoalWeek(ctx context.Context, g workout.Goal, weekStart time.Time) ([]workout.Workout, int, error) {
	startStr := weekStart.Format(dateLayout)
	done, err := s.Training.WeekScheduled(ctx, g.ID, startStr)
	if err != nil {
		return nil, 0, err
	}
	if done {
		return nil, 0, nil
	}

	existing, err := s.Training.ListWorkouts(ctx, g.Rider)
	if err != nil {
		return nil, 0, err
	}
	endStr := weekStart.AddDate(0, 0, 6).Format(dateLayout)
	for _, wk := range existing {
		// An FTP test is linked to the goal so its day reads as taken, but it
		// is not a plan-made session: a rider who scheduled a test into a week
		// the tick has not reached yet has not had that week filled, and
		// counting the test as "filled" would leave it holding the test alone.
		if wk.GoalID == g.ID && wk.TestProtocol == "" && wk.Date >= startStr && wk.Date <= endStr {
			return nil, 0, s.recordLegacyWeek(ctx, g, startStr)
		}
	}
	return s.scheduleGoalWeek(ctx, g, weekStart, "")
}

// handleExplainPlan asks internal/narration for a plain-language summary
// of a goal's reconciled periodization plan — Phase E of
// docs/training-plan.md, layered on top of the exact same plan
// handleGoalPeriodization already computes and shows as a table (via
// reconciledPeriodizationPlan), never a second source of truth for it.
func (s *Server) handleExplainPlan(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	if s.Narration == nil {
		s.logger().Warn("plan explanation requested but no ANTHROPIC_API_KEY is configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment has no narration configured",
		})
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
	if !s.rateLimitNarration(w, identity.User) {
		return
	}

	plan, _, err := s.reconciledPeriodizationPlan(r.Context(), g, identity.User)
	if err != nil {
		if err == periodization.ErrNoEventDate || err == periodization.ErrEventInThePast {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.fail(w, err)
		return
	}

	text, err := s.Narration.ExplainPlan(r.Context(), g, plan)
	if err != nil {
		// Contained the same way Komoot's own undocumented-API failures are
		// (see AGENTS.md's Komoot section): a third-party outage degrades
		// this one optional feature, not the rest of the page.
		s.logger().Warn("plan explanation failed", "goal", g.ID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not reach the narration service"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// handleBuildMaxHRTest creates one ordinary, plannable max-heart-rate field
// test workout from internal/fitnesstest's fixed protocol — max HR is never
// estimated from synced data at all (see fitnesstest's own doc comment for
// why), so this is the only path to a number besides the rider typing one.
// Deliberately no already-exists check: this is an explicit, one-off rider
// action with no natural key to dedupe against, the same as clicking "Build a
// workout" by hand. The FTP test's handler is in ftptests.go.
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
	if errors.Is(err, workout.ErrGoalNotFound) || errors.Is(err, workout.ErrWorkoutNotFound) ||
		errors.Is(err, workout.ErrThresholdSuggestionNotFound) {
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
	// before is what recalibrateLevelsForFTP needs; the marker is not carried
	// forward into the save below because SaveProfile never writes it.
	before, _, err := s.Training.GetProfile(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	saved, err := s.Training.SaveProfile(r.Context(), workout.RiderProfile{
		// FTPEstimated and Estimated are deliberately not read from body:
		// this is the manual save form, and a rider willing to click Save
		// owns whatever number sits in each field, estimated or not — see
		// RiderProfile.FTPEstimated's own doc comment on why those only
		// ever mean "not yet looked at and confirmed."
		Rider: rider, FTPWatts: body.FTPWatts, ThresholdPaceSecPerKM: body.ThresholdPaceSecPerKM,
		MaxHR: body.MaxHR, ThresholdHR: body.ThresholdHR, RestingHR: body.RestingHR, AvailableDays: body.AvailableDays,
		HoursPerAvailableDay: body.HoursPerAvailableDay, ExperienceLevel: body.ExperienceLevel,
		AutoPushWorkouts: body.AutoPushWorkouts, SmartTrainer: body.SmartTrainer,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	dto, changed, err := s.recalibrateLevelsForFTP(r.Context(), rider, before, "profile_saved")
	if err != nil {
		s.logger().Error("level recalibration failed", "rider", rider, "err", err)
	}

	s.logger().Info("rider profile saved", "rider", rider)
	out := profileDTOFrom(saved)
	if changed {
		out.LevelsRecalibrated = &dto
	}
	writeJSON(w, http.StatusOK, out)
}

type profileProposalDTO struct {
	AvailableDays        []string `json:"availableDays,omitempty"`
	HoursPerAvailableDay float64  `json:"hoursPerAvailableDay,omitempty"`
	Explanation          string   `json:"explanation,omitempty"`
}

// handleProposeProfileChange turns a rider's free-text note (e.g. "I'm
// traveling next week") into a suggested edit to their own profile —
// Phase E's other half. Deliberately never writes the stored profile
// itself: the response is a proposal for the frontend to fill into the
// ordinary profile form, and only an explicit save through
// handleSaveRiderProfile makes it real — the same "propose, never
// silently apply" rule internal/fitnesstest's own FTP estimate follows,
// and the same reason handleSaveRiderProfile itself never reads
// FTPEstimated from the request body.
func (s *Server) handleProposeProfileChange(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	if s.Narration == nil {
		s.logger().Warn("profile change proposal requested but no ANTHROPIC_API_KEY is configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment has no narration configured",
		})
		return
	}

	var body struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil || strings.TrimSpace(body.Note) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a note is required"})
		return
	}

	rider := auth.FromContext(r.Context()).User
	if !s.rateLimitNarration(w, rider) {
		return
	}

	profile, _, err := s.Training.GetProfile(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}

	proposal, err := s.Narration.ProposeProfileChange(r.Context(), body.Note, profile)
	if err != nil {
		s.logger().Warn("profile change proposal failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not reach the narration service"})
		return
	}

	s.logger().Info("profile change proposed", "rider", rider)
	writeJSON(w, http.StatusOK, profileProposalDTO{
		AvailableDays: proposal.AvailableDays, HoursPerAvailableDay: proposal.HoursPerAvailableDay,
		Explanation: proposal.Explanation,
	})
}

type goalProposalDTO struct {
	Name             string  `json:"name"`
	Sport            string  `json:"sport"`
	EventDate        string  `json:"eventDate,omitempty"`
	Priority         string  `json:"priority"`
	TargetDistanceM  float64 `json:"targetDistanceM,omitempty"`
	TargetElevationM float64 `json:"targetElevationM,omitempty"`
	Explanation      string  `json:"explanation,omitempty"`
}

// handleProposeGoal turns a rider's own sentence about an event ("gran fondo,
// 180km, mid June") into a suggested goal. Like handleProposeProfileChange it
// never writes anything: the response pre-fills the ordinary goal form, and
// only an explicit save through handleCreateGoal makes it real — so the
// server-side ownership and validation rules stay in exactly one place.
func (s *Server) handleProposeGoal(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	if s.Narration == nil {
		s.logger().Warn("goal proposal requested but no ANTHROPIC_API_KEY is configured")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment has no narration configured",
		})
		return
	}

	var body struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil || strings.TrimSpace(body.Note) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a note is required"})
		return
	}

	rider := auth.FromContext(r.Context()).User
	if !s.rateLimitNarration(w, rider) {
		return
	}

	proposal, err := s.Narration.ProposeGoal(r.Context(), body.Note, time.Now())
	if err != nil {
		s.logger().Warn("goal proposal failed", "rider", rider, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not turn that into a goal — try rephrasing it, or fill the form in by hand"})
		return
	}

	s.logger().Info("goal proposed", "rider", rider)
	writeJSON(w, http.StatusOK, goalProposalDTO{
		Name: proposal.Name, Sport: proposal.Sport, EventDate: proposal.EventDate, Priority: proposal.Priority,
		TargetDistanceM: proposal.TargetDistanceM, TargetElevationM: proposal.TargetElevationM,
		Explanation: proposal.Explanation,
	})
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
	s.attachWhy(r.Context(), rider, out)
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
	writeJSON(w, http.StatusOK, s.workoutDTOWithWhy(r.Context(), wk))
}

type workoutRequestBody struct {
	Sport       string           `json:"sport"`
	Name        string           `json:"name"`
	GoalID      string           `json:"goalId"`
	Date        string           `json:"date"`
	Description string           `json:"description"`
	Steps       []workoutStepDTO `json:"steps"`
	Zone        string           `json:"zone"`
	Level       float64          `json:"level"`
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
		Zone: workout.Zone(body.Zone), Level: body.Level,
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
		Zone        *string           `json:"zone"`
		Level       *float64          `json:"level"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	req := workout.UpdateWorkoutRequest{
		Name: body.Name, GoalID: body.GoalID, Date: body.Date, Description: body.Description,
		Level: body.Level,
	}
	if body.Sport != nil {
		sport := model.Sport(*body.Sport)
		req.Sport = &sport
	}
	if body.Steps != nil {
		steps := stepsFromDTOs(*body.Steps)
		req.Steps = &steps
	}
	if body.Zone != nil {
		zone := workout.Zone(*body.Zone)
		req.Zone = &zone
	}
	req.Description = recordManualMove(wk, req)
	s.retireOutdoorSteps(&req, wk)

	updated, err := s.Training.UpdateWorkout(r.Context(), id, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("workout updated", "id", id, "by", identity.User)
	writeJSON(w, http.StatusOK, s.workoutDTOWithWhy(r.Context(), updated))
}

// recordManualMove returns the description to save when an update moves a
// plan-made workout to another day: the rider's own description (or the
// stored one) with the original date noted in scheduler.MovedFrom's form.
// Without it the vacated day reads as empty to scheduleGoal, and the next
// schedule run — every 30 minutes in the background — plans the same
// session there again, so a move looked like a duplicate. An automatic move
// already records this; a manual one never did. The first original date is
// kept across repeated moves, and a workout the rider built by hand (no
// goal) needs nothing, since it never blocks or triggers scheduling.
func recordManualMove(wk workout.Workout, req workout.UpdateWorkoutRequest) *string {
	if req.Date == nil || *req.Date == wk.Date || wk.Date == "" {
		return req.Description
	}
	goalID := wk.GoalID
	if req.GoalID != nil {
		goalID = *req.GoalID
	}
	if goalID == "" {
		return req.Description
	}
	desc := wk.Description
	if req.Description != nil {
		desc = *req.Description
	}
	if _, ok := scheduler.MovedFrom(desc); ok {
		return req.Description
	}
	desc = strings.TrimRight(desc, " \n") + "\n\nRescheduled by you: moved from " + wk.Date + "."
	return &desc
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

	// Off the rider's watch first, while the push record that says where it
	// is still exists — DeleteWorkout removes that record with the workout.
	s.removeWorkoutFromGarmin(r.Context(), wk)

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
		Name:   workout.DeviceName(wk.Name, wk.Indoor),
		Sport:  fitworkout.SportFromString(string(wk.Sport)),
		Indoor: wk.Indoor,
	})
	if err != nil {
		// A step combination validateSteps allowed but fitworkout.Encode
		// still refuses (e.g. a target with no low/high) is a data problem
		// the rider can fix, not a server bug.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeFITAttachment(s.logger(), w, workout.DeviceName(wk.ID, wk.Indoor), fitBytes)
}

// handlePushWorkoutToGarmin puts a structured workout on the rider's own
// connected Garmin account and on its calendar date — the manual counterpart
// to autoPushWorkouts, and to handleDownloadWorkoutFIT's USB-copy path (this
// one sends Connect's own JSON schema, not the FIT bytes that download
// produces — see internal/garmin's own doc comment). Idempotent: see
// syncWorkoutToGarmin. Pressing it again on an unchanged workout is a no-op,
// on an edited one updates the existing copy, and never adds a second.
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
	res, err := s.syncWorkoutToGarmin(r.Context(), session, wk, workout.PushOriginManual)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	s.logger().Info("workout pushed to garmin", "workout", id, "garminWorkoutId", res.RemoteID, "outcome", res.Outcome, "rider", identity.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "pushed", "outcome": res.Outcome, "garminWorkoutId": res.RemoteID})
}

// ---------- Metrics ingestion (docs/training-plan.md Phase B1) ----------

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
	// Analysis is rideanalysis's verdict on this session — nil for a session
	// that has not been analysed yet (not yet synced with a FIT source, or
	// older than the analysis window; see analyseNewSessions). Zones and the
	// power curve are deliberately left out of this DTO: the UI this feeds
	// only needs the summary numbers and per-step results, and both of those
	// are sized for a full ride's worth of raw samples, not worth shipping
	// on every fitness/week response.
	Analysis *sessionAnalysisDTO `json:"analysis,omitempty"`
	// LinkedByHand is set when the rider said which planned session this
	// ride was (PUT /api/training/sessions/{id}/workout), so the automatic
	// match no longer decides. Filled in by the week view only.
	LinkedByHand bool `json:"linkedByHand,omitempty"`
}

// sessionAnalysisDTO mirrors workout.SessionAnalysis's summary fields —
// omitting PowerZoneSeconds/HRZoneSeconds/PowerCurve, see completedSessionDTO's
// own doc comment on why. Steps reuses workout.AnalysisStep directly: its
// json tags already match the shape the frontend wants (see
// apps/web/src/api/types.ts's AnalysisStep), so there is nothing to mirror.
type sessionAnalysisDTO struct {
	Outcome       string                 `json:"outcome"`
	LoadSource    string                 `json:"loadSource"`
	NP            float64                `json:"np,omitempty"`
	IF            float64                `json:"if,omitempty"`
	TSS           float64                `json:"tss,omitempty"`
	DurationRatio float64                `json:"durationRatio,omitempty"`
	Steps         []workout.AnalysisStep `json:"steps,omitempty"`
	// WorkoutID is the planned workout this session was matched against, if
	// any — omitted for an unplanned ride. The frontend uses it to title the
	// step-results modal with the matched workout's own name rather than
	// whatever else happens to be planned for that day.
	WorkoutID string `json:"workoutId,omitempty"`
	// Feel is the rider's optional 1-5 "how did it feel" rating, 0 (omitted)
	// when never rated — see workout.SessionAnalysis.Feel and
	// handleSetSessionFeel (progression.go).
	Feel int `json:"feel,omitempty"`
	// Legs and Stress are the survey's optional answers next to Feel,
	// omitted when unanswered.
	Legs   string `json:"legs,omitempty"`
	Stress string `json:"stress,omitempty"`
}

func sessionAnalysisDTOFrom(a workout.SessionAnalysis) sessionAnalysisDTO {
	return sessionAnalysisDTO{
		Outcome: a.Outcome, LoadSource: a.LoadSource,
		NP: a.NormalizedPower, IF: a.IntensityFactor, TSS: a.TSS, DurationRatio: a.DurationRatio,
		Steps: a.Steps, WorkoutID: a.WorkoutID, Feel: a.Feel, Legs: a.Legs, Stress: a.Stress,
	}
}

// completedSessionDTOFrom attaches sess's analysis, if any, from analyses —
// a map built once per request (see analysesSince) rather than a per-session
// store lookup, since a fitness/week response can list dozens of sessions.
func completedSessionDTOFrom(sess workout.CompletedSession, analyses map[string]workout.SessionAnalysis) completedSessionDTO {
	dto := completedSessionDTO{
		ID: sess.ID, Provider: sess.Provider, Sport: sess.Sport, Date: sess.Date,
		DurationSeconds: sess.DurationSeconds, DistanceM: sess.DistanceM,
		AvgHR: sess.AvgHR, AvgPowerWatts: sess.AvgPowerWatts, TrainingLoad: sess.TrainingLoad,
	}
	if a, ok := analyses[sess.ID]; ok {
		sa := sessionAnalysisDTOFrom(a)
		dto.Analysis = &sa
	}
	return dto
}

// analysesSince loads a rider's ride analyses once and keys them by session
// id, so handleGetFitness and handleTrainingWeek can each attach analysis to
// every session in their response with a map lookup instead of one
// GetAnalysis call per session.
//
// A ListAnalyses failure never fails the caller's request: both endpoints
// worked before analyses existed and must keep working when analysis
// storage has a bad day — the same "only a problem with our own storage
// fails the request" trade-off adaptRider's own ListAnalyses call makes
// (see adaptation.go). The rider just gets sessions back with no analysis
// attached, logged at Warn per AGENTS.md's new-guard logging rule: the
// request still succeeds, degraded rather than broken.
func (s *Server) analysesSince(ctx context.Context, rider, sinceDate string) map[string]workout.SessionAnalysis {
	list, err := s.Training.ListAnalyses(ctx, rider, sinceDate)
	if err != nil {
		s.logger().Warn("reading ride analyses failed", "rider", rider, "err", err)
		return nil
	}
	out := make(map[string]workout.SessionAnalysis, len(list))
	for _, a := range list {
		out[a.SessionID] = a
	}
	return out
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

	// One ListAnalyses call for the whole response, covering every session
	// date about to be returned — see analysesSince's own doc comment on why
	// a failure here degrades rather than failing the request.
	var analyses map[string]workout.SessionAnalysis
	if len(sessions) > 0 {
		sinceDate := sessions[0].Date
		for _, sess := range sessions[1:] {
			if sess.Date < sinceDate {
				sinceDate = sess.Date
			}
		}
		analyses = s.analysesSince(r.Context(), rider, sinceDate)
	}

	dto := fitnessResponseDTO{
		Snapshots: make([]fitnessSnapshotDTO, 0, len(snapshots)),
		Sessions:  make([]completedSessionDTO, 0, len(sessions)),
	}
	for _, snap := range snapshots {
		dto.Snapshots = append(dto.Snapshots, fitnessSnapshotDTO{Date: snap.Date, CTL: snap.CTL, ATL: snap.ATL, TSB: snap.TSB})
	}
	for _, sess := range sessions {
		dto.Sessions = append(dto.Sessions, completedSessionDTOFrom(sess, analyses))
	}
	writeJSON(w, http.StatusOK, dto)
}
