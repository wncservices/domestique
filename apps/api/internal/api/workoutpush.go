package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// What a sync of one workout onto Garmin did — for the log and the response,
// not for control flow.
const (
	pushCreated   = "created"
	pushUpdated   = "updated"
	pushScheduled = "scheduled"
	pushUnchanged = "unchanged"
)

type workoutPushResult struct {
	Outcome  string
	RemoteID string
}

// syncWorkoutToGarmin makes the rider's Garmin account agree with a workout:
// a copy of it exists, matches its current steps, and sits on its date. It is
// idempotent, which is the point — the old push always created a new copy, so
// pressing the button twice, or an automatic pass running every half hour,
// would have filled the account with duplicates.
//
// State lives in workout_pushes. What was last sent is fingerprinted
// (workout.ContentHash), so an unchanged workout costs no Garmin call at all;
// an edited one updates its existing copy in place; a workout the rider
// deleted in Connect (garmin.ErrWorkoutGone) is simply sent again; a workout
// moved to another day has its old calendar entry removed and a new one made.
//
// Progress is saved as it is made, not at the end: if creating succeeded and
// scheduling failed, the next pass must schedule the copy that exists, not
// create a second one.
func (s *Server) syncWorkoutToGarmin(ctx context.Context, session garmin.Session, wk workout.Workout) (workoutPushResult, error) {
	consumer, _ := s.garminConsumer()
	steps := workout.FITSteps(wk.Steps)
	hash := workout.ContentHash(wk)
	res := workoutPushResult{Outcome: pushUnchanged}

	push, have, err := s.Training.GetPush(ctx, wk.ID, garminProvider)
	if err != nil {
		return res, err
	}

	create := !have || push.RemoteID == ""
	if !create && push.ContentHash != hash {
		err := s.Garmin.UpdateWorkout(ctx, consumer, session, push.RemoteID, wk.Name, string(wk.Sport), steps)
		switch {
		case errors.Is(err, garmin.ErrWorkoutGone):
			create = true
		case err != nil:
			return res, err
		default:
			push.ContentHash = hash
			res.Outcome = pushUpdated
			if err := s.Training.SavePush(ctx, push); err != nil {
				return res, err
			}
		}
	}

	if create {
		id, err := s.Garmin.PushWorkout(ctx, consumer, session, wk.Name, string(wk.Sport), steps)
		if err != nil {
			return res, err
		}
		// Whatever calendar entry the old copy had went with it.
		push = workout.Push{WorkoutID: wk.ID, Provider: garminProvider, RemoteID: id, ContentHash: hash}
		res.Outcome = pushCreated
		if err := s.Training.SavePush(ctx, push); err != nil {
			return res, err
		}
	}
	res.RemoteID = push.RemoteID

	if wk.Date == push.ScheduledDate {
		return res, nil
	}

	// The date changed (or was never placed): take the old calendar entry
	// off first, best-effort — failing to remove it must not stop the new
	// one going on, only cost a stale entry on the calendar.
	if push.ScheduleID != "" {
		if err := s.Garmin.UnscheduleWorkout(ctx, consumer, session, push.ScheduleID); err != nil {
			s.logger().Warn("garmin: could not remove a workout's old calendar entry", "workout", wk.ID, "err", err)
		}
		push.ScheduleID = ""
	}
	if wk.Date == "" {
		push.ScheduledDate = ""
		return res, s.Training.SavePush(ctx, push)
	}

	entry, err := s.Garmin.ScheduleWorkout(ctx, consumer, session, push.RemoteID, wk.Date)
	if err != nil {
		if errors.Is(err, garmin.ErrWorkoutGone) {
			// Deleted between the update and now: forget the copy so the
			// next pass makes a fresh one.
			_ = s.Training.DeletePush(ctx, wk.ID, garminProvider)
		}
		return res, fmt.Errorf("scheduling on %s: %w", wk.Date, err)
	}
	push.ScheduleID, push.ScheduledDate = entry, wk.Date
	if res.Outcome == pushUnchanged {
		res.Outcome = pushScheduled
	}
	return res, s.Training.SavePush(ctx, push)
}

// removeWorkoutFromGarmin takes a workout's copy off the rider's account
// when the workout is deleted here — otherwise deleting a planned session
// leaves it on their watch for a day they will still try to ride. Best-effort
// by design: the workout is being deleted regardless, and a rider whose
// Garmin session has lapsed must still be able to delete it.
func (s *Server) removeWorkoutFromGarmin(ctx context.Context, wk workout.Workout) {
	if s.Training == nil {
		return
	}
	push, have, err := s.Training.GetPush(ctx, wk.ID, garminProvider)
	if err != nil || !have || push.RemoteID == "" {
		return
	}
	session, ok := s.garminSessionForRider(wk.Rider)
	if !ok {
		s.logger().Warn("garmin: a deleted workout's copy was left on the account — no connected session", "workout", wk.ID, "rider", wk.Rider)
		return
	}
	consumer, _ := s.garminConsumer()
	if push.ScheduleID != "" {
		if err := s.Garmin.UnscheduleWorkout(ctx, consumer, session, push.ScheduleID); err != nil {
			s.logger().Warn("garmin: could not remove a deleted workout's calendar entry", "workout", wk.ID, "err", err)
		}
	}
	if err := s.Garmin.DeleteWorkout(ctx, consumer, session, push.RemoteID); err != nil && !errors.Is(err, garmin.ErrWorkoutGone) {
		s.logger().Warn("garmin: could not remove a deleted workout's copy", "workout", wk.ID, "err", err)
	}
}

// autoPushWindow is how far ahead the unattended pass places workouts: enough
// that a week is on the watch before it starts and next week's arrives before
// the weekend, not so far that an edit to the plan leaves a month of stale
// entries to chase.
const autoPushWindow = 14 * 24 * time.Hour

// autoPushWorkouts keeps every opted-in rider's Garmin account in step with
// their planned workouts: what the scheduler just created goes on, what was
// edited updates, what moved moves. Runs at the end of AutoScheduleTick, so
// it only ever runs when auto-schedule is on, and only for riders who set
// AutoPushWorkouts themselves.
//
// Which workouts: those inside autoPushWindow that came from a goal (what
// the scheduler made), plus anything already pushed, so an edit to a copy
// that exists keeps flowing. A workout the rider built by hand and never
// pushed is left for them to push — auto-push places the plan on the watch,
// it does not decide which of their own experiments deserve to be there.
func (s *Server) autoPushWorkouts(ctx context.Context) {
	if s.Training == nil || s.Garmin == nil || s.Links == nil {
		return
	}
	riders, err := s.Training.ListAutoPushRiders(ctx)
	if err != nil {
		s.logger().Warn("auto-push: listing riders failed", "err", err)
		return
	}

	today := time.Now().Format("2006-01-02")
	horizon := time.Now().Add(autoPushWindow).Format("2006-01-02")

	for _, rider := range riders {
		if ctx.Err() != nil {
			return
		}
		session, ok := s.garminSessionForRider(rider)
		if !ok {
			continue
		}
		workouts, err := s.Training.ListWorkouts(ctx, rider)
		if err != nil {
			s.logger().Warn("auto-push: listing workouts failed", "rider", rider, "err", err)
			continue
		}

		pushed := 0
		for _, wk := range workouts {
			if wk.Date == "" || wk.Date < today || wk.Date > horizon {
				continue
			}
			_, have, err := s.Training.GetPush(ctx, wk.ID, garminProvider)
			if err != nil {
				s.logger().Warn("auto-push: reading push state failed", "workout", wk.ID, "err", err)
				continue
			}
			if !have && wk.GoalID == "" {
				continue
			}
			res, err := s.syncWorkoutToGarmin(ctx, session, wk)
			if err != nil {
				// One failure ends this rider's pass, not the whole tick:
				// the likely causes (a lapsed session, Garmin being down)
				// hit every remaining workout the same way, and retrying
				// each would only hammer an account that is refusing us.
				// The next tick tries again.
				s.logger().Warn("auto-push to garmin failed", "rider", rider, "workout", wk.ID, "err", err)
				break
			}
			if res.Outcome != pushUnchanged {
				pushed++
			}
		}
		if pushed > 0 {
			s.logger().Info("auto-pushed workouts to garmin", "rider", rider, "changed", pushed)
		}
	}
}
