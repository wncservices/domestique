package api

import (
	"context"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/weekplan"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// reoptimise is the morning readiness swap: on a Caution or Rest morning,
// today's hard session trades days with an easier one later this week, so the
// hard work is kept (not just eased away) and lands on a day the rider is
// likelier to be fresh. See docs/superpowers/specs/2026-10-19-daily-reoptimiser-design.md.
//
// It runs inside AutoScheduleTick, under the scheduling lock, after the season
// is planned and before AdaptWorkouts, so the easy session that now sits on
// today is eased by readiness in the same tick and the push that follows sends
// it. It only acts in the day's first sync slot, and never fails the tick: a
// rider whose pass cannot run is a Warn naming the stage.
func (s *Server) reoptimise(ctx context.Context) {
	if s.Training == nil || !s.inMorningSlot(s.now()) {
		return
	}
	goals, err := s.Training.ListAllGoals(ctx)
	if err != nil {
		s.logger().Warn("reoptimise: listing goals failed", "err", err)
		return
	}
	seen := map[string]bool{}
	swapped := 0
	for _, g := range goals {
		if seen[g.Rider] || ctx.Err() != nil {
			continue
		}
		seen[g.Rider] = true
		if s.reoptimiseRider(ctx, g.Rider) {
			swapped++
		}
	}
	// A count only: naming who swapped would reveal whose morning was a bad one.
	if swapped > 0 {
		s.logger().Info("reoptimise: sessions swapped", "riders", swapped)
	}
}

// reoptimiseRider reports whether it swapped a pair of sessions.
func (s *Server) reoptimiseRider(ctx context.Context, rider string) bool {
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.logger().Warn("reoptimise: could not read sessions", "rider", rider)
		return false
	}
	latest, err := s.latestFitness(ctx, rider)
	if err != nil {
		s.logger().Warn("reoptimise: could not read fitness", "rider", rider)
		return false
	}
	assessment := s.assessReadiness(ctx, rider, sessions, latest)
	if assessment.Verdict != readiness.Caution && assessment.Verdict != readiness.Rest {
		return false
	}
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.logger().Warn("reoptimise: could not read workouts", "rider", rider)
		return false
	}
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		s.logger().Warn("reoptimise: could not read the profile", "rider", rider)
		return false
	}
	events, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		s.logger().Warn("reoptimise: could not read life events", "rider", rider)
		return false
	}
	pushes, err := s.Training.ListPushes(ctx, rider, garminProvider)
	if err != nil {
		s.logger().Warn("reoptimise: could not read push state", "rider", rider)
		return false
	}

	now := s.localNow()
	monday := periodization.MondayOf(now)
	sunday := monday.AddDate(0, 0, 6).Format(dateLayout)
	// Next Monday is in the week only so that the spacing rule sees a hard day
	// across the edge; PlanSwap never picks a day past Sunday.
	nextMonday := monday.AddDate(0, 0, 7).Format(dateLayout)
	var week []workout.Workout
	done := map[string]bool{}
	for _, w := range workouts {
		if w.Date < monday.Format(dateLayout) || w.Date > nextMonday {
			continue
		}
		week = append(week, w)
		if adapter.WorkoutDone(w, sessions) {
			done[w.ID] = true
		}
	}
	manual := map[string]bool{}
	for id, p := range pushes {
		if p.Origin == workout.PushOriginManual {
			manual[id] = true
		}
	}

	forecast := outlookTomorrow(now, workouts, sessions, latest, assessment, profile)
	plan, ok := weekplan.PlanSwap(weekplan.SwapInput{
		Today: now.Format(dateLayout), Sunday: sunday, Week: week, Done: done,
		ManualPushed: manual, NoTestDays: lifeevents.NoTestDays(events),
		TomorrowReady: forecast.Verdict == readiness.Ready,
	})
	if !ok {
		return false
	}

	byID := make(map[string]workout.Workout, len(week))
	for _, w := range week {
		byID[w.ID] = w
	}
	hard, easy := byID[plan.HardID], byID[plan.EasyID]
	if err := s.swapSessions(ctx, hard, easy, plan); err != nil {
		s.logger().Warn("reoptimise: could not swap the sessions", "rider", rider)
		return false
	}

	// Recorded only once both writes have landed, so a swap that was undone
	// never leaves a reason behind. The verdict lives in the stored record,
	// shown to its owner, and never in a log line.
	verdict := string(assessment.Verdict)
	s.recordAdjustment(ctx, rider, hard.ID, why.NewRecord(why.ReshuffleReadiness,
		"Today looked like a day to take it easier, so this session moved to a later day and an easier one took its place.",
		why.ReshuffleInputs{From: hard.Date, To: plan.HardTo, Verdict: verdict}), false)
	s.recordAdjustment(ctx, rider, easy.ID, why.NewRecord(why.ReshuffleReadiness,
		"Swapped with a harder session that looked too much for today.",
		why.ReshuffleInputs{From: easy.Date, To: plan.EasyTo, Verdict: verdict}), false)
	return true
}

// swapSessions moves the hard session, then the easy one. Only the date, the
// description note and the flags change on each: steps, zone, level and indoor
// state are the session's own. If the second write fails the first is put back
// as it was, so a failure never leaves two sessions on one day.
func (s *Server) swapSessions(ctx context.Context, hard, easy workout.Workout, plan weekplan.Swap) error {
	if err := s.moveForSwap(ctx, hard, plan.HardTo); err != nil {
		return err
	}
	if err := s.moveForSwap(ctx, easy, plan.EasyTo); err != nil {
		date, desc, prior := hard.Date, hard.Description, workout.CurrentFlags(hard)
		if _, rerr := s.Training.UpdateWorkout(ctx, hard.ID, workout.UpdateWorkoutRequest{Date: &date, Description: &desc, Flags: &prior}); rerr != nil {
			s.logger().Error("reoptimise: could not put a session back after a failed swap", "rider", hard.Rider)
		}
		return err
	}
	return nil
}

// moveForSwap writes one session onto its new day. The note names the day it
// left; only the first such note counts, so a session already moved keeps its
// original day and mover.
func (s *Server) moveForSwap(ctx context.Context, w workout.Workout, to string) error {
	description := w.Description + " " + scheduler.AdjustedMarker + " moved from " + w.Date + " — swapped with an easier day."
	flags := workout.CurrentFlags(w).WithAdaptation(why.ReshuffleReadiness)
	if flags.MovedFrom == "" {
		flags.MovedFrom, flags.MovedBy = w.Date, workout.MovedByAuto
	}
	_, err := s.Training.UpdateWorkout(ctx, w.ID, workout.UpdateWorkoutRequest{Date: &to, Description: &description, Flags: &flags})
	return err
}
