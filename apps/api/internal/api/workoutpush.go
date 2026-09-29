package api

import (
	"context"
	"errors"
	"fmt"

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
//
// origin says who is asking, and is recorded on the copy: the rider pressing
// "Send to Garmin" (PushOriginManual) or the unattended pass
// (PushOriginAuto). Manual is sticky: an automatic pass keeping a copy in step
// never turns the rider's own push into one it may withdraw later.
func (s *Server) syncWorkoutToGarmin(ctx context.Context, session garmin.Session, wk workout.Workout, origin string) (workoutPushResult, error) {
	consumer, _ := s.garminConsumer()
	steps := workout.FITSteps(wk.Steps)
	hash := workout.ContentHash(wk)
	res := workoutPushResult{Outcome: pushUnchanged}

	push, have, err := s.Training.GetPush(ctx, wk.ID, garminProvider)
	if err != nil {
		return res, err
	}
	stamp := origin
	if have && push.Origin == workout.PushOriginManual {
		stamp = workout.PushOriginManual
	}
	if have && push.Origin != stamp {
		// A manual push over a copy the automatic pass made: take ownership
		// now, even when nothing else about it changes.
		push.Origin = stamp
		if err := s.Training.SavePush(ctx, push); err != nil {
			return res, err
		}
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
			push.Origin = stamp
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
		push = workout.Push{WorkoutID: wk.ID, Provider: garminProvider, RemoteID: id, ContentHash: hash, Origin: stamp}
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

// withdrawFromGarmin takes a workout's copy off the rider's account and forgets
// the push record — the copy and its calendar entry, the same two calls a
// deleted workout's removal makes. Unlike removeWorkoutFromGarmin it reports
// failure, and keeps the record when it fails: the workout still exists, so
// the record is what lets the next pass find the copy and try again, where
// forgetting it would strand the copy on the calendar for good. A copy the
// rider already deleted in Connect counts as removed.
func (s *Server) withdrawFromGarmin(ctx context.Context, session garmin.Session, push workout.Push) error {
	consumer, _ := s.garminConsumer()
	if push.ScheduleID != "" {
		if err := s.Garmin.UnscheduleWorkout(ctx, consumer, session, push.ScheduleID); err != nil {
			return fmt.Errorf("removing calendar entry: %w", err)
		}
	}
	if push.RemoteID != "" {
		if err := s.Garmin.DeleteWorkout(ctx, consumer, session, push.RemoteID); err != nil && !errors.Is(err, garmin.ErrWorkoutGone) {
			return fmt.Errorf("deleting the copy: %w", err)
		}
	}
	return s.Training.DeletePush(ctx, push.WorkoutID, garminProvider)
}

// localToday is today's date, "YYYY-MM-DD", in the deployment's zone
// (training.timezone, Europe/Brussels unless set) — the same zone the
// fixed-time sync runs in. A rider's own zone is not stored, and the pod runs
// in UTC, where the day changes hours before a rider's does. Falls back to the
// server's own zone only when the configured one cannot be loaded, which
// config validation already refuses.
func (s *Server) localToday() string {
	now := s.now()
	if sched, err := s.syncSchedule(); err == nil {
		if loc := sched.Location(); loc != nil {
			now = now.In(loc)
		}
	}
	return now.Format(dateLayout)
}

// autoPushWorkouts keeps every opted-in rider's head unit showing today: the
// day's planned session is placed on their Garmin calendar, kept in step if it
// changes, and nothing else is left ahead of it. Runs at the end of
// AutoScheduleTick — every 30 minutes, so the first pass of the day delivers
// the session — and so only when auto-schedule is on, and only for riders who
// set AutoPushWorkouts themselves.
func (s *Server) autoPushWorkouts(ctx context.Context) {
	if s.Training == nil || s.Garmin == nil || s.Links == nil {
		return
	}
	riders, err := s.Training.ListAutoPushRiders(ctx)
	if err != nil {
		s.logger().Warn("auto-push: listing riders failed", "err", err)
		return
	}

	for _, rider := range riders {
		if ctx.Err() != nil {
			return
		}
		if pushed := s.pushWorkoutsForRider(ctx, rider); pushed > 0 {
			s.logger().Info("auto-pushed workouts to garmin", "rider", rider, "changed", pushed)
		}
	}
}

// pushWorkoutsForRider is autoPushWorkouts' own per-rider body, factored
// out so handleReplan and the FTP test scheduler can run it for one rider
// right after they change the plan — the same idempotent syncWorkoutToGarmin,
// the same rules, without a second copy of this loop.
//
// The rules:
//
//   - Only workouts dated today (local, see localToday) are sent, and only
//     ones that came from a goal or were already pushed: a workout the rider
//     built by hand and never sent is left for them to send.
//   - A copy this pass made earlier for a day that is now in the future is
//     withdrawn (withdrawFromGarmin): a plan that used to be pushed a fortnight
//     ahead is cleared down to today, and a session moved off today by an
//     adjustment leaves no copy behind on today's calendar. Only copies
//     recorded as automatic; what the rider sent by hand, whatever its day, is
//     theirs.
//   - Past days' copies are left. They are history the head unit may already
//     have ridden, removing them would only erase it, and they cost nothing.
//
// Every failure is logged and swallowed, never returned: a lapsed Garmin
// session or a provider outage must not fail the caller. Returns how many
// copies changed on the account, for the caller's own logging.
func (s *Server) pushWorkoutsForRider(ctx context.Context, rider string) int {
	session, ok := s.garminSessionForRider(rider)
	if !ok {
		return 0
	}
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.logger().Warn("auto-push: listing workouts failed", "rider", rider, "err", err)
		return 0
	}
	pushes, err := s.Training.ListPushes(ctx, rider, garminProvider)
	if err != nil {
		s.logger().Warn("auto-push: reading push state failed", "rider", rider, "err", err)
		return 0
	}

	today := s.localToday()
	changed := 0
	for _, wk := range workouts {
		if wk.Date > today {
			// Withdraw before anything is sent, so today's session goes on a
			// calendar that is already clear of the days ahead.
			push, have := pushes[wk.ID]
			if !have || push.Origin != workout.PushOriginAuto {
				continue
			}
			if err := s.withdrawFromGarmin(ctx, session, push); err != nil {
				s.logger().Warn("auto-push: could not take a future workout off garmin", "rider", rider, "workout", wk.ID, "err", err)
				break
			}
			changed++
		}
	}
	for _, wk := range workouts {
		if wk.Date != today {
			continue
		}
		_, have := pushes[wk.ID]
		if !have && wk.GoalID == "" {
			continue
		}
		res, err := s.syncWorkoutToGarmin(ctx, session, wk, workout.PushOriginAuto)
		if err != nil {
			// One failure ends this rider's pass, not the whole tick:
			// the likely causes (a lapsed session, Garmin being down)
			// hit every remaining workout the same way, and retrying
			// each would only hammer an account that is refusing us.
			// The next tick (or replan) tries again.
			s.logger().Warn("auto-push to garmin failed", "rider", rider, "workout", wk.ID, "err", err)
			break
		}
		if res.Outcome != pushUnchanged {
			changed++
		}
	}
	return changed
}
