package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// replanResultDTO is handleReplan's response — the counts a toast reads,
// plus the same shape GET /api/training/week already returns so the
// frontend can repaint the strip from one response instead of a second
// round trip.
type replanResultDTO struct {
	Removed  int             `json:"removed"`
	Created  int             `json:"created"`
	Adjusted int             `json:"adjusted"`
	Week     trainingWeekDTO `json:"week"`
}

// replanLockedMessage is what a rider sees when their own replan click
// lands the same moment AutoScheduleTick's half-hourly pass already holds
// autoScheduleLockKey — plain enough to explain why nothing happened
// without naming an internal mechanism the rider has no reason to know
// about.
const replanLockedMessage = "Your plan is being updated right now — try again in a moment."

// handleReplan rebuilds the rest of the rider's current week — today
// through Sunday — from their current progression levels, availability,
// goal phase and readiness. Owner-only, the rider from the session like
// every other training route (AGENTS.md's "the rider comes from the
// session, never the request body"): there is no rider in the request body
// to trust or distrust.
//
// Runs under the same advisory lock AutoScheduleTick holds, so a rider
// clicking Replan and the half-hourly tick can never interleave and read
// each other's half-finished work. withDBLock reports whether its closure
// actually ran — unlike every other caller of withDBLock (autosync,
// autoimport, autoschedule), which are background ticks that can just try
// again next time, this one has a rider on the other end who clicked a
// button. Returning 200 with all-zero counts here would read as "replanned,
// and there was nothing to do," which is a lie — nothing ran at all. See
// writeReplanLocked, split out so it can be tested without a real Postgres
// lock to contend for (see replan_test.go's own comment on why the
// contended-lock path itself can't be exercised on SQLite).
func (s *Server) handleReplan(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User

	var (
		result replanResultDTO
		opErr  error
	)
	ran := withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
		result, opErr = s.replanRider(ctx, rider)
	})
	if !ran {
		s.writeReplanLocked(w, rider)
		return
	}
	if opErr != nil {
		// A failure that stops the rebuild partway (a DB error, not a
		// Garmin hiccup — those are swallowed inside pushWorkoutsForRider
		// and removeWorkoutFromGarmin) is the AGENTS.md Error case: something
		// that should have happened did not, and the request has to say so.
		s.logger().Error("replan failed", "rider", rider, "err", opErr)
		s.fail(w, opErr)
		return
	}
	s.logger().Info("training replanned", "rider", rider,
		"removed", result.Removed, "created", result.Created, "adjusted", result.Adjusted)
	writeJSON(w, http.StatusOK, result)
}

// writeReplanLocked is handleReplan's own response when a concurrent
// AutoScheduleTick already holds the lock — 409, not 500: nothing is
// broken, the rider's own plan is mid-update by the very system this
// button also drives, and trying again in a few seconds is the correct
// next step, not a retry-with-backoff situation. Warn, not Error, per
// AGENTS.md's own rule ("does the request still succeed, and does
// everything past this line still run?" — here nothing ran, but nothing is
// broken either; this is the rider's own transient collision with a
// half-hourly background job, not a bug) — logged with the rider only, no
// stack of counts that were never computed.
func (s *Server) writeReplanLocked(w http.ResponseWriter, rider string) {
	s.logger().Warn("replan skipped: scheduling already running", "rider", rider)
	writeJSON(w, http.StatusConflict, map[string]string{"error": replanLockedMessage})
}

// replanRider does the actual work, separate from the handler so it runs
// entirely inside withDBLock's closure with nothing left to do outside it.
func (s *Server) replanRider(ctx context.Context, rider string) (replanResultDTO, error) {
	now := s.now()
	today := now.Format(dateLayout)
	weekStart := periodization.MondayOf(now)
	weekEnd := weekStart.AddDate(0, 0, 6).Format(dateLayout)

	removed, err := s.removePlanMadeWorkouts(ctx, rider, today, weekEnd)
	if err != nil {
		return replanResultDTO{}, err
	}

	// ListAllGoals, not ListGoals: it orders dated goals first (see its own
	// doc comment), the same order AutoScheduleTick relies on so a race on
	// the calendar claims a day before a rolling general-fitness goal can.
	// ListGoals(rider) does not give that ordering — undated goals sort
	// first there — so replanning would double-book a day differently from
	// how the unattended tick would have scheduled it.
	goals, err := s.Training.ListAllGoals(ctx)
	if err != nil {
		return replanResultDTO{}, err
	}
	var createdIDs []string
	for _, g := range goals {
		if g.Rider != rider {
			continue
		}
		made, _, err := s.scheduleGoal(ctx, g, today)
		if err != nil {
			if err == periodization.ErrNoEventDate || err == periodization.ErrEventInThePast {
				continue
			}
			return replanResultDTO{}, err
		}
		for _, wk := range made {
			createdIDs = append(createdIDs, wk.ID)
		}
	}
	created := len(createdIDs)

	// Readiness, missed make-up, fatigue swap, step-down — the exact same
	// per-rider pass AutoScheduleTick runs after scheduling, so a replanned
	// week gets the identical guarantees a ticked one would have.
	s.adaptRider(ctx, rider)

	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return replanResultDTO{}, err
	}
	if profile.AutoPushWorkouts {
		s.pushWorkoutsForRider(ctx, rider)
	}

	adjusted, err := s.countAdjustedAmong(ctx, rider, createdIDs)
	if err != nil {
		return replanResultDTO{}, err
	}

	week, err := s.buildTrainingWeekDTO(ctx, rider, weekStart, now)
	if err != nil {
		return replanResultDTO{}, err
	}

	return replanResultDTO{Removed: removed, Created: created, Adjusted: adjusted, Week: week}, nil
}

// isPlanMade reports whether a workout was built by scheduleGoal rather
// than by the rider's own hand — GoalID set and a description starting
// with scheduler.GeneratedDescription, which also matches one carrying
// scheduler.AdjustedMarker (adaptRider appends to the description, it never
// replaces its own prefix). A session the rider swapped for an alternate
// (scheduler.SwappedMarker) is not plan-made any more: the rider chose it, so
// "Re-plan this week" must not delete it. It still counts for scheduling,
// because its GoalID is kept.
func isPlanMade(wk workout.Workout) bool {
	return wk.GoalID != "" &&
		strings.HasPrefix(wk.Description, scheduler.GeneratedDescription) &&
		!strings.Contains(wk.Description, scheduler.SwappedMarker)
}

// removePlanMadeWorkouts deletes every plan-made workout dated today
// through weekEnd, except one dated today that the rider has already
// ridden — a session that happened must survive a replan run five minutes
// later just because the plan is being rebuilt. Rider-built workouts (no
// GoalID, or a description that isn't the scheduler's own) and anything
// dated before today are never touched.
func (s *Server) removePlanMadeWorkouts(ctx context.Context, rider, today, weekEnd string) (int, error) {
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return 0, err
	}
	riddenToday, err := s.riddenToday(ctx, rider, today, workouts)
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, wk := range workouts {
		if wk.Date < today || wk.Date > weekEnd {
			continue
		}
		if !isPlanMade(wk) {
			continue
		}
		// An indoor version is the rider's own choice, made by hand. Rebuilding
		// the day would put the road session back and lose it silently.
		if wk.Indoor {
			continue
		}
		// A session a life event moved or eased, or one the rider chose to keep
		// through it, stays: the blackout would keep the plan from rebuilding
		// what the event took away, so deleting it here would lose it for good.
		if lifeevents.Touched(wk.Description) {
			continue
		}
		// A routed session is the rider's own too: rebuilding the day would
		// drop the link and strand the loop.
		if wk.RouteSlug != "" {
			continue
		}
		if wk.Date == today && riddenToday[wk.ID] {
			continue
		}
		// Best-effort, like every other deletion path that touches Garmin
		// (removeWorkoutFromGarmin's own doc comment): the workout is coming
		// out of the plan regardless of whether a lapsed session or a
		// provider outage stops its copy coming off the watch too.
		s.removeWorkoutFromGarmin(ctx, wk)
		if err := s.Training.DeleteWorkout(ctx, wk.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// riddenToday is which of today's workouts the rider has already completed
// — a completed session today in the workout's own sport, or a ride
// analysis that names the workout directly. Either is enough: the analysis
// is the precise link when it exists (set the moment a ride is matched to
// its planned session), the same-sport-same-day check is the fallback for a
// session synced but not yet analysed.
func (s *Server) riddenToday(ctx context.Context, rider, today string, workouts []workout.Workout) (map[string]bool, error) {
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return nil, err
	}
	sportRiddenToday := map[string]bool{}
	for _, sess := range sessions {
		if sess.Date == today {
			sportRiddenToday[sess.Sport] = true
		}
	}

	analyses, err := s.Training.ListAnalyses(ctx, rider, today)
	if err != nil {
		return nil, err
	}
	analysedWorkout := map[string]bool{}
	for _, a := range analyses {
		if a.WorkoutID != "" {
			analysedWorkout[a.WorkoutID] = true
		}
	}

	ridden := map[string]bool{}
	for _, wk := range workouts {
		if wk.Date != today {
			continue
		}
		if analysedWorkout[wk.ID] || sportRiddenToday[string(wk.Sport)] {
			ridden[wk.ID] = true
		}
	}
	return ridden, nil
}

// countAdjustedAmong is how handleReplan reports "adjusted" — of the
// workouts *this replan itself just created* (ids), how many now carry
// scheduler.AdjustedMarker after adaptRider has run. Deliberately scoped to
// ids rather than "every generated workout dated today..weekEnd": a
// survivor this replan left alone (today's already-ridden session, say)
// may already carry an adjustment an *earlier* AutoScheduleTick made,
// sometimes days ago — counting that would tell a rider "N eased for
// readiness" for an easing this replan had nothing to do with. Re-reads
// from storage rather than inspecting adaptRider's own in-memory changes,
// since adaptRider (shared with AutoScheduleTick) reports nothing back to
// its caller today and duplicating its logic here would be exactly the
// second source of truth AGENTS.md warns against.
func (s *Server) countAdjustedAmong(ctx context.Context, rider string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return 0, err
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	n := 0
	for _, wk := range workouts {
		if want[wk.ID] && strings.Contains(wk.Description, scheduler.AdjustedMarker) {
			n++
		}
	}
	return n, nil
}
