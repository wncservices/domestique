package api

import (
	"context"
	"errors"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// goalLeftoversMigration names the one-time removal of the sessions a goal
// delete left behind before deletes took a goal's future sessions with it
// (2.16.0). Those sessions kept the generated description, lost their goal and
// became the rider's own, so they held their days: a later goal planned around
// them and the calendar showed the old plan with no goal behind it.
const goalLeftoversMigration = "goal_delete_leftovers"

const (
	leftoverLockAttempts = 20
	leftoverLockWait     = 30 * time.Second
)

var errLeftoversBusy = errors.New("scheduling stayed busy")

// goalLeftover reports whether wk is such a session: no goal, the generated
// description exactly, and nothing the rider or an automatic pass changed.
func goalLeftover(wk workout.Workout) bool {
	f := wk.Flags
	return wk.GoalID == "" && wk.Description == scheduler.GeneratedDescription &&
		wk.EditedAt == "" && wk.TestProtocol == "" && wk.CrewRideID == "" &&
		wk.RouteSlug == "" && !wk.Indoor && wk.PlannedSnapshot == nil &&
		!f.Adapted() && !f.Swapped && f.Kept == workout.KeptNone && f.MovedFrom == ""
}

// RemoveGoalLeftovers removes, once per deployment, the leftover sessions dated
// today or later (never one ridden today, with their Garmin copies) and then
// rebuilds each affected rider's plan, so their goals take the freed days. The
// past stays as history. It is marked done only once every rider is through,
// so a start that met scheduling busy tries again at the next start.
func (s *Server) RemoveGoalLeftovers(ctx context.Context) error {
	if s.Training == nil {
		return nil
	}
	done, err := s.Training.MigrationDone(ctx, goalLeftoversMigration)
	if err != nil || done {
		return err
	}
	today := s.localToday()
	riders, err := s.Training.RidersWithGoallessSessions(ctx, scheduler.GeneratedDescription, today)
	if err != nil {
		return err
	}
	for _, rider := range riders {
		removed, err := s.removeRiderLeftovers(ctx, rider, today)
		if err != nil {
			return err
		}
		if removed > 0 {
			s.logger().Info("goal leftovers removed", "rider", rider, "removed", removed)
			s.rebuildRiderPlan(ctx, rider)
		}
	}
	return s.Training.MarkMigration(ctx, goalLeftoversMigration)
}

func (s *Server) removeRiderLeftovers(ctx context.Context, rider, today string) (int, error) {
	var (
		removed int
		err     error
	)
	for attempt := 0; attempt < leftoverLockAttempts; attempt++ {
		ran := withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
			s.seasonMu.Lock()
			defer s.seasonMu.Unlock()
			var existing []workout.Workout
			if existing, err = s.Training.ListWorkouts(ctx, rider); err != nil {
				return
			}
			var ridden map[string]bool
			if ridden, err = s.riddenToday(ctx, rider, today, existing); err != nil {
				return
			}
			for _, wk := range existing {
				if wk.Date < today || !goalLeftover(wk) || (wk.Date == today && ridden[wk.ID]) {
					continue
				}
				s.removeWorkoutFromGarmin(ctx, wk)
				if err = s.Training.DeleteWorkout(ctx, wk.ID); err != nil {
					return
				}
				removed++
			}
		})
		if ran {
			return removed, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(leftoverLockWait):
		}
	}
	return 0, errLeftoversBusy
}

// RunGoalLeftoversCleanup is RemoveGoalLeftovers for the start-up, with its
// failure logged: busy scheduling is a Warn (the next start tries again), any
// other failure an Error, since those riders' calendars still show a dead plan.
func (s *Server) RunGoalLeftoversCleanup(ctx context.Context) {
	defer s.guard("goal leftovers cleanup")
	err := s.RemoveGoalLeftovers(ctx)
	switch {
	case err == nil, errors.Is(err, context.Canceled):
	case errors.Is(err, errLeftoversBusy):
		s.logger().Warn("goal leftovers: not removed, scheduling stayed busy")
	default:
		s.logger().Error("goal leftovers: the cleanup failed", "err", err)
	}
}
