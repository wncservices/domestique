package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/alternates"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/indoor"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/progression"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// This file is the rider-facing half of workout alternates: list the easier,
// harder, shorter and longer versions of a plan-made session, swap to one, and
// go back. Every one of them is a rider's own request; nothing here runs on
// its own, and GET never writes.
//
// A swap edits the session in place (same id, date and goal) so a copy already
// on a head unit updates instead of duplicating. It appends
// scheduler.SwappedMarker, which is what makes the session rider-touched for
// adaptation, replan and the season refresh, and it never moves a progression
// level: the level moves only from the ride's outcome.

const (
	alternatesGoneMessage    = "That alternate is not available any more. Reload the options and pick again."
	alternatesNoneMessage    = "This session has no alternates."
	alternatesWarning        = "Readiness is low today"
	revertPlannedNote        = "Back to the planned version."
	maxAlternatesBodyBytes   = 1 << 10
	swapNoteDurationFallback = "unknown length"
)

type alternateOptionDTO struct {
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	Zone       string  `json:"zone"`
	Level      float64 `json:"level,omitempty"`
	Minutes    int     `json:"minutes"`
	TSS        float64 `json:"tss"`
	Difficulty string  `json:"difficulty"`
	Warning    string  `json:"warning,omitempty"`
}

type alternatesDTO struct {
	Options     []alternateOptionDTO `json:"options"`
	HasSnapshot bool                 `json:"hasSnapshot"`
}

// alternateSet is what alternatesFor worked out. refusal is non-empty when the
// session cannot be swapped at all (past, ridden, not plan-made, a test), with
// the sentence to give the rider; options is then nil.
type alternateSet struct {
	options []alternates.Option
	profile workout.RiderProfile
	refusal string
}

// alternatesFor computes the options for wk as of today. It reads and writes
// nothing beyond what the reads need: in particular it does not seed a missing
// level, which levelsFor would.
func (s *Server) alternatesFor(ctx context.Context, wk workout.Workout, today time.Time) (alternateSet, error) {
	todayStr := today.Format(dateLayout)
	ridden, err := s.riddenToday(ctx, wk.Rider, todayStr, []workout.Workout{wk})
	if err != nil {
		return alternateSet{}, err
	}
	switch {
	// A crew ride is the crew's: it has no alternates, so the menu is empty.
	case wk.CrewRideID != "":
		return alternateSet{refusal: crewRideOutdoorMessage}, nil
	case wk.Date != "" && wk.Date < todayStr:
		return alternateSet{refusal: indoorPastMessage}, nil
	case ridden[wk.ID]:
		return alternateSet{refusal: indoorRiddenMessage}, nil
	case !alternates.Available(wk, todayStr, false):
		return alternateSet{refusal: alternatesNoneMessage}, nil
	}

	profile, _, err := s.Training.GetProfile(ctx, wk.Rider)
	if err != nil {
		return alternateSet{}, err
	}
	level, err := s.riderLevelReadOnly(ctx, wk.Rider, profile, wk.Sport, wk.Zone)
	if err != nil {
		return alternateSet{}, err
	}
	reduced := s.inReducedWeek(ctx, wk, profile)
	options := alternates.Options(wk, level, reduced, profile)
	return alternateSet{options: dropUnchangedOnTrainer(wk, options, profile), profile: profile}, nil
}

// dropUnchangedOnTrainer hides a Shorter or Longer that would leave an indoor
// session exactly as long as it is: the conversion caps an easy or long ride's
// main work, so 5 and 6 outdoor hours are the same 2h20 on the trainer, and an
// option that changes nothing there is not an option. Harder and Easier change
// the work itself and always show.
func dropUnchangedOnTrainer(wk workout.Workout, opts []alternates.Option, profile workout.RiderProfile) []alternates.Option {
	if !wk.Indoor {
		return opts
	}
	current := int(math.Round(workout.PlannedSeconds(wk.Steps) / 60))
	kept := opts[:0:0]
	for _, o := range opts {
		if o.Kind == alternates.Shorter || o.Kind == alternates.Longer {
			if int(math.Round(workout.PlannedSeconds(shownSteps(wk, o, profile))/60)) == current {
				continue
			}
		}
		kept = append(kept, o)
	}
	return kept
}

// riderLevelReadOnly is the rider's level in zone: the saved one, else the
// starting level their experience gives, which is what levelsFor would save.
// Unlike levelsFor it never writes, because a GET must not.
func (s *Server) riderLevelReadOnly(ctx context.Context, rider string, profile workout.RiderProfile, sport model.Sport, zone workout.Zone) (float64, error) {
	saved, err := s.Training.ListLevels(ctx, rider)
	if err != nil {
		return 0, err
	}
	for _, l := range saved {
		if l.Sport == sport && l.Zone == zone {
			return l.Level, nil
		}
	}
	for _, l := range progression.Initial(profile.ExperienceLevel, sport) {
		if l.Zone == string(zone) {
			return l.Value, nil
		}
	}
	return 3, nil
}

// inReducedWeek reports whether the session's date falls in a recovery or
// taper week of its goal's plan, where the plan is shedding load on purpose. A
// session with no goal plan (no such goal, or a goal the plan cannot be built
// for) is an ordinary week.
func (s *Server) inReducedWeek(ctx context.Context, wk workout.Workout, profile workout.RiderProfile) bool {
	if wk.GoalID == "" {
		return false
	}
	g, err := s.Training.GetGoal(ctx, wk.GoalID)
	if err != nil {
		if !errors.Is(err, workout.ErrGoalNotFound) {
			s.logger().Warn("alternates: could not read the goal for the week type", "workout", wk.ID, "err", err)
		}
		return false
	}
	plan, err := periodization.Build(g, profile, s.now())
	if err != nil {
		return false
	}
	return weekIsReduced(plan, wk.Date)
}

func weekIsReduced(plan periodization.Plan, date string) bool {
	day, err := time.Parse(dateLayout, date)
	if err != nil {
		return false
	}
	for _, week := range plan.Weeks {
		start, err := time.Parse(dateLayout, week.StartDate)
		if err != nil {
			continue
		}
		if !day.Before(start) && day.Before(start.AddDate(0, 0, 7)) {
			return week.Recovery || week.Phase == periodization.PhaseTaper
		}
	}
	return false
}

// readinessIsLow is today's verdict being caution or rest, for the warning on a
// harder option. A failure reading what it needs is "no warning": the swap is
// still the rider's call.
func (s *Server) readinessIsLow(ctx context.Context, rider string, today time.Time) bool {
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.logger().Warn("alternates: could not read sessions for the readiness warning", "rider", rider, "err", err)
		return false
	}
	latest, err := s.latestFitness(ctx, rider)
	if err != nil {
		s.logger().Warn("alternates: could not read fitness for the readiness warning", "rider", rider, "err", err)
		return false
	}
	return s.assessReadinessAt(ctx, rider, sessions, latest, today).Verdict != readiness.Ready
}

// shownSteps is the form of an option the rider will get: the outdoor steps
// the alternates package built, run through the indoor conversion when the
// session is indoor, so the minutes and TSS on the menu are the session's own.
func shownSteps(wk workout.Workout, opt alternates.Option, profile workout.RiderProfile) []workout.WorkoutStep {
	if !wk.Indoor {
		return opt.Steps
	}
	res, err := indoor.Convert(workout.Workout{Sport: wk.Sport, Name: opt.Name, Zone: opt.Zone, Steps: opt.Steps}, profile)
	if err != nil {
		return opt.Steps
	}
	return res.Steps
}

func (s *Server) handleAlternates(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	wk, ok := s.ownWorkoutOrNotFound(w, r)
	if !ok {
		return
	}
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return
	}
	set, err := s.alternatesFor(r.Context(), wk, today)
	if err != nil {
		s.fail(w, err)
		return
	}

	dto := alternatesDTO{Options: []alternateOptionDTO{}, HasSnapshot: wk.PlannedSnapshot != nil}
	// Readiness only matters for a harder option on today's or tomorrow's
	// session, so it is read only then.
	lowReadiness := false
	if _, hasHarder := findAlternate(set.options, alternates.Harder); hasHarder &&
		(wk.Date == today.Format(dateLayout) || wk.Date == today.AddDate(0, 0, 1).Format(dateLayout)) {
		lowReadiness = s.readinessIsLow(r.Context(), wk.Rider, today)
	}
	for _, opt := range set.options {
		steps := shownSteps(wk, opt, set.profile)
		out := alternateOptionDTO{
			Kind: string(opt.Kind), Name: opt.Name, Zone: string(opt.Zone), Level: opt.Level,
			Minutes:    int(math.Round(workout.PlannedSeconds(steps) / 60)),
			TSS:        alternates.TSS(wk.Sport, steps, set.profile.FTPWatts),
			Difficulty: opt.Difficulty,
		}
		if opt.Kind == alternates.Harder && lowReadiness {
			out.Warning = alternatesWarning
		}
		dto.Options = append(dto.Options, out)
	}
	writeJSON(w, http.StatusOK, dto)
}

func findAlternate(opts []alternates.Option, kind alternates.Kind) (alternates.Option, bool) {
	for _, o := range opts {
		if o.Kind == kind {
			return o, true
		}
	}
	return alternates.Option{}, false
}

func (s *Server) handleAlternateApply(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	wk, ok := s.ownWorkoutOrNotFound(w, r)
	if !ok {
		return
	}
	var body struct {
		Kind alternates.Kind `json:"kind"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAlternatesBodyBytes)).Decode(&body); err != nil || !body.Kind.Valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be easier, harder, shorter or longer"})
		return
	}
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return
	}

	// Recomputed from scratch, never trusting what the menu showed when it
	// opened: the session or the rider's level may have moved since.
	set, err := s.alternatesFor(r.Context(), wk, today)
	if err != nil {
		s.fail(w, err)
		return
	}
	if set.refusal != "" {
		s.logger().Info("alternate refused", "rider", wk.Rider, "workout", wk.ID, "kind", string(body.Kind))
		writeJSON(w, http.StatusConflict, map[string]string{"error": set.refusal})
		return
	}
	opt, offered := findAlternate(set.options, body.Kind)
	if !offered {
		s.logger().Info("alternate refused", "rider", wk.Rider, "workout", wk.ID, "kind", string(body.Kind))
		writeJSON(w, http.StatusConflict, map[string]string{"error": alternatesGoneMessage})
		return
	}

	name, zone, level, steps := opt.Name, opt.Zone, opt.Level, opt.Steps
	description := addNote(wk.Description, fmt.Sprintf("%s %s, was %s (%s).",
		scheduler.SwappedMarker, opt.Kind, wk.Name, swapDuration(workout.PlannedSeconds(wk.Steps))))
	req := workout.UpdateWorkoutRequest{
		Name: &name, Zone: &zone, Level: &level, Steps: &steps, Description: &description,
		// Kept by the store when the session already has one, so it is always
		// the plan's version, never the previous swap's.
		PlannedSnapshot: plannedSnapshotOf(wk),
	}
	// A swap builds the usual outdoor session and re-runs the indoor
	// conversion on it when the original is indoor, the same hook adaptation
	// uses, so an indoor session stays indoor and the rebuilt outdoor version
	// becomes its outdoor_steps.
	s.keepIndoor(&req, wk, set.profile, opt.Zone)

	updated, err := s.Training.UpdateWorkout(r.Context(), wk.ID, req)
	if err != nil {
		s.logger().Error("could not swap a workout", "rider", wk.Rider, "workout", wk.ID, "kind", string(body.Kind), "err", err)
		s.fail(w, err)
		return
	}
	s.logger().Info("workout swapped", "rider", wk.Rider, "workout", wk.ID, "kind", string(body.Kind))
	s.repushToday(r.Context(), updated)
	writeJSON(w, http.StatusOK, s.workoutDTOWithWhy(r.Context(), updated))
}

// handleAlternateRevert puts the session back as the plan made it. Idempotent:
// with no snapshot it answers 200 and changes nothing. The swap marker stays in
// the description (and a note says so): the rider has expressed a preference and
// the plan must not silently redo it.
func (s *Server) handleAlternateRevert(w http.ResponseWriter, r *http.Request) {
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
	if wk.PlannedSnapshot == nil {
		writeJSON(w, http.StatusOK, s.workoutDTOWithWhy(r.Context(), wk))
		return
	}

	snap := wk.PlannedSnapshot
	name, zone, level := snap.Name, snap.Zone, snap.Level
	steps := append([]workout.WorkoutStep{}, snap.Steps...)
	indoorFlag := snap.Indoor
	// A pointer to a nil slice clears the outdoor original; anything else
	// stores it, so both indoor states come back exactly.
	var outdoor []workout.WorkoutStep
	if snap.OutdoorSteps != nil {
		outdoor = append([]workout.WorkoutStep{}, *snap.OutdoorSteps...)
	}
	description := addNote(snap.Description, scheduler.SwappedMarker+" "+revertPlannedNote)
	req := workout.UpdateWorkoutRequest{
		Name: &name, Zone: &zone, Level: &level, Steps: &steps, Description: &description,
		Indoor: &indoorFlag, OutdoorSteps: &outdoor, ClearPlannedSnapshot: true,
	}
	if snap.Sport != "" {
		req.Sport = &snap.Sport
	}
	updated, err := s.Training.UpdateWorkout(r.Context(), wk.ID, req)
	if err != nil {
		s.logger().Error("could not revert a swapped workout", "rider", wk.Rider, "workout", wk.ID, "err", err)
		s.fail(w, err)
		return
	}
	s.logger().Info("workout reverted to the planned version", "rider", wk.Rider, "workout", wk.ID)
	s.repushToday(r.Context(), updated)
	writeJSON(w, http.StatusOK, s.workoutDTOWithWhy(r.Context(), updated))
}

// plannedSnapshotOf is wk as it stands, for the store to keep if it has no
// snapshot yet.
func plannedSnapshotOf(wk workout.Workout) *workout.PlannedSnapshot {
	snap := &workout.PlannedSnapshot{
		Sport: wk.Sport, Name: wk.Name, Zone: wk.Zone, Level: wk.Level, Description: wk.Description,
		Steps: append([]workout.WorkoutStep{}, wk.Steps...), Indoor: wk.Indoor,
	}
	if wk.OutdoorSteps != nil {
		outdoor := append([]workout.WorkoutStep{}, *wk.OutdoorSteps...)
		snap.OutdoorSteps = &outdoor
	}
	return snap
}

// swapDuration formats a length the way the indoor note does: "1h30", or
// "45 min" under an hour.
func swapDuration(seconds float64) string {
	if seconds <= 0 {
		return swapNoteDurationFallback
	}
	mins := int(math.Round(seconds / 60))
	if mins < 60 {
		return fmt.Sprintf("%d min", mins)
	}
	return fmt.Sprintf("%dh%02d", mins/60, mins%60)
}
