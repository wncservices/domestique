package api

import (
	"context"
	"fmt"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// now is the clock adaptation reads. A field on Server rather than a bare
// time.Now so a test can put "today" in the middle of a week: whether a
// session counts as missed depends on which weekday it is, and a test that
// only worked on Thursdays would be worse than none.
func (s *Server) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

// AdaptWorkouts applies internal/adapter's per-session rules to every rider
// with a goal: a missed key session is moved to a free day later this week,
// and a hard session is swapped for an easy one when the rider is very
// fatigued. Exported, like AutoScheduleTick, so a test can drive one pass
// directly.
//
// It runs inside AutoScheduleTick (so only when auto-schedule is on, and
// under its lock), after the week has been scheduled and before it is pushed
// to a watch — which means an adjustment reaches the rider's device in the
// same pass that makes it.
func (s *Server) AdaptWorkouts(ctx context.Context) {
	if s.Training == nil {
		return
	}
	goals, err := s.Training.ListAllGoals(ctx)
	if err != nil {
		s.logger().Warn("adapt: listing goals failed", "err", err)
		return
	}
	seen := map[string]bool{}
	for _, g := range goals {
		if seen[g.Rider] || ctx.Err() != nil {
			continue
		}
		seen[g.Rider] = true
		s.adaptRider(ctx, g.Rider)
	}
}

func (s *Server) adaptRider(ctx context.Context, rider string) {
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.logger().Warn("adapt: listing workouts failed", "rider", rider, "err", err)
		return
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.logger().Warn("adapt: listing sessions failed", "rider", rider, "err", err)
		return
	}
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		s.logger().Warn("adapt: reading profile failed", "rider", rider, "err", err)
		return
	}
	snapshots, err := s.Training.ListFitnessSnapshots(ctx, rider)
	if err != nil {
		s.logger().Warn("adapt: reading fitness failed", "rider", rider, "err", err)
		return
	}
	var latest *workout.FitnessSnapshot
	if len(snapshots) > 0 {
		latest = &snapshots[len(snapshots)-1]
	}

	// struggleLookbackWindow covers adapter.AdaptSessions' widest lookback
	// (its 14-day struggled-key-session check) — anything analysed further
	// back cannot affect a decision made today, so there is no reason to ask
	// the store for more.
	sinceDate := s.now().AddDate(0, 0, -14).Format("2006-01-02")
	analyses, err := s.Training.ListAnalyses(ctx, rider, sinceDate)
	if err != nil {
		s.logger().Warn("adapt: reading ride analyses failed", "rider", rider, "err", err)
		analyses = nil
	}
	// Keyed by the planned workout id, never the session id — that is what
	// AdaptSessions' own done/detectFatigue look a workout up by, and only a
	// ride that actually matched a planned workout carries one.
	byWorkout := make(map[string]workout.SessionAnalysis, len(analyses))
	for _, a := range analyses {
		if a.WorkoutID != "" {
			byWorkout[a.WorkoutID] = a
		}
	}

	byID := make(map[string]workout.Workout, len(workouts))
	for _, w := range workouts {
		byID[w.ID] = w
	}

	for _, c := range adapter.AdaptSessions(workouts, sessions, profile, latest, s.now(), byWorkout) {
		wk := byID[c.WorkoutID]

		// A step-down replaces the workout's own content (name, steps,
		// level) rather than moving or downgrading it wholesale — see
		// applyStepDown.
		if c.StepDown {
			if err := s.applyStepDown(ctx, wk, profile, c); err != nil {
				s.logger().Warn("adapt: could not step a workout down", "workout", wk.ID, "rider", rider, "err", err)
				continue
			}
			s.logger().Info("workout adapted automatically", "workout", wk.ID, "rider", rider, "change", "stepped down", "reason", c.Reason)
			continue
		}

		description := wk.Description + " " + adapter.Note(c)
		req := workout.UpdateWorkoutRequest{Description: &description}

		what := "rescheduled"
		if c.NewDate != "" {
			req.Date = &c.NewDate
		}
		if c.Downgrade {
			easy := scheduler.EasyVariant(wk, profile)
			description += fmt.Sprintf(" Replaces: %s.", wk.Name)
			req.Name, req.Steps = &easy.Name, &easy.Steps
			what = "downgraded"
		}

		// The easy day the missed session is taking over is given up first,
		// off the rider's watch as well as out of the plan — and only then is
		// the missed one moved onto it, so a failure part-way never leaves two
		// sessions on one day.
		if c.ReplaceWorkoutID != "" {
			gone := byID[c.ReplaceWorkoutID]
			s.removeWorkoutFromGarmin(ctx, gone)
			if err := s.Training.DeleteWorkout(ctx, gone.ID); err != nil {
				s.logger().Warn("adapt: could not remove the easy day being replaced", "workout", gone.ID, "rider", rider, "err", err)
				continue
			}
		}
		if _, err := s.Training.UpdateWorkout(ctx, wk.ID, req); err != nil {
			s.logger().Warn("adapt: could not update a workout", "workout", wk.ID, "rider", rider, "err", err)
			continue
		}
		s.logger().Info("workout adapted automatically", "workout", wk.ID, "rider", rider, "change", what, "reason", c.Reason)
	}
}

// applyStepDown replaces wk with the rung one level below its own on the
// same zone's ladder — same date, same goal — the spec's response to a
// struggled key session in that zone (adapter.AdaptSessions' own
// stepDownTarget picks which workout this is; this is the one place that
// actually builds the lower rung, via workoutlib, the same library
// scheduleGoal uses to build a workout in the first place).
func (s *Server) applyStepDown(ctx context.Context, wk workout.Workout, profile workout.RiderProfile, c adapter.Change) error {
	ladder, ok := workoutlib.LadderFor(wk.Sport, string(wk.Zone))
	if !ok {
		return fmt.Errorf("no workout ladder for %s/%s", wk.Sport, wk.Zone)
	}

	targetLevel := int(wk.Level) - 1
	if targetLevel < 1 {
		targetLevel = 1
	}
	var rung workoutlib.Rung
	found := false
	for _, r := range ladder.Rungs {
		if r.Level == targetLevel {
			rung, found = r, true
			break
		}
	}
	if !found {
		return fmt.Errorf("no rung at level %d for %s/%s", targetLevel, wk.Sport, wk.Zone)
	}

	req := workoutlib.Instantiate(ladder, rung, profile)
	// The source marker is what stops a later adaptation pass (every 30
	// minutes) from stepping down a second same-zone workout off the exact
	// same struggle, once this one is no longer IsGenerated — see
	// adapter.StepDownSourceNote's own doc comment.
	description := wk.Description + " " + adapter.Note(c) + " " + adapter.StepDownSourceNote(c.StepDownSourceID)
	_, err := s.Training.UpdateWorkout(ctx, wk.ID, workout.UpdateWorkoutRequest{
		Name: &req.Name, Steps: &req.Steps, Level: &req.Level, Description: &description,
	})
	return err
}
