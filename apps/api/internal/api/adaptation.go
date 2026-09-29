package api

import (
	"context"
	"fmt"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
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

	assessment := s.assessReadiness(ctx, rider, sessions, latest)

	// AdaptSessions already keeps each workout to at most one Change per pass
	// (stepDownTarget skips ids its own per-session loop has claimed — see
	// its doc comment); this is a second, independent guard here so a bug in
	// that bookkeeping cannot silently apply two Changes to the same workout
	// — first one wins, and a second is loud rather than a quiet clobber.
	appliedFor := map[string]bool{}
	for _, c := range adapter.AdaptSessions(workouts, sessions, profile, s.now(), byWorkout, assessment) {
		if appliedFor[c.WorkoutID] {
			s.logger().Warn("adapt: a second change was produced for the same workout in one pass; ignoring it", "workout", c.WorkoutID, "rider", rider)
			continue
		}
		appliedFor[c.WorkoutID] = true
		wk := byID[c.WorkoutID]

		what, err := s.applyChange(ctx, rider, wk, byID, profile, c)
		if err != nil {
			// applyChange has already logged which step failed.
			continue
		}
		s.logger().Info("workout adapted automatically", "workout", wk.ID, "rider", rider, "change", what, "reason", c.Reason)
	}

	s.easeBeforeFTPTests(ctx, rider, workouts, profile, appliedFor)
	s.logTomorrowAdvisory(ctx, rider, sessions, latest, profile)
}

// applyChange writes one adapter.Change to the store and returns what kind
// of change it was, for the caller's log line. It is the one place a Change
// (reschedule, downgrade or step-down) becomes a stored workout, shared by
// adaptRider and the "ease tomorrow" action, so both append
// scheduler.AdjustedMarker through the same code. That marker is what makes
// a workout no longer scheduler.IsGenerated, which is the whole
// one-change-per-workout guard: a second implementation of it would be a
// second way to get it wrong.
//
// On failure it logs which step failed (Warn: the rest of an adaptation
// pass still runs) and returns the error, so an HTTP caller can answer with
// it as well.
func (s *Server) applyChange(ctx context.Context, rider string, wk workout.Workout, byID map[string]workout.Workout, profile workout.RiderProfile, c adapter.Change) (string, error) {
	// A step-down replaces the workout's own content (name, steps, level)
	// rather than moving or downgrading it wholesale — see applyStepDown.
	if c.StepDown {
		if err := s.applyStepDown(ctx, wk, profile, c); err != nil {
			s.logger().Warn("adapt: could not step a workout down", "workout", wk.ID, "rider", rider, "err", err)
			return "", err
		}
		return "stepped down", nil
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
			return "", err
		}
	}
	if _, err := s.Training.UpdateWorkout(ctx, wk.ID, req); err != nil {
		s.logger().Warn("adapt: could not update a workout", "workout", wk.ID, "rider", rider, "err", err)
		return "", err
	}
	return what, nil
}

// stepDownRung finds the rung one level below wk's own on its zone's ladder.
// Shared by applyStepDown and the tomorrow forecast's eligibility check, so
// a banner is only ever offered for a workout the click can actually step
// down.
func stepDownRung(wk workout.Workout) (workoutlib.Ladder, workoutlib.Rung, error) {
	ladder, ok := workoutlib.LadderFor(wk.Sport, string(wk.Zone))
	if !ok {
		return workoutlib.Ladder{}, workoutlib.Rung{}, fmt.Errorf("no workout ladder for %s/%s", wk.Sport, wk.Zone)
	}
	targetLevel := int(wk.Level) - 1
	if targetLevel < 1 {
		targetLevel = 1
	}
	for _, r := range ladder.Rungs {
		if r.Level == targetLevel {
			return ladder, r, nil
		}
	}
	return workoutlib.Ladder{}, workoutlib.Rung{}, fmt.Errorf("no rung at level %d for %s/%s", targetLevel, wk.Sport, wk.Zone)
}

// applyStepDown replaces wk with the rung one level below its own on the
// same zone's ladder — same date, same goal — the spec's response to a
// struggled key session in that zone (adapter.AdaptSessions' own
// stepDownTarget picks which workout this is; this is the one place that
// actually builds the lower rung, via workoutlib, the same library
// scheduleGoal uses to build a workout in the first place).
func (s *Server) applyStepDown(ctx context.Context, wk workout.Workout, profile workout.RiderProfile, c adapter.Change) error {
	ladder, rung, err := stepDownRung(wk)
	if err != nil {
		return err
	}

	req := workoutlib.Instantiate(ladder, rung, profile)
	// The source marker goes *before* scheduler.AdjustedMarker, not after
	// adapter.Note(c) — the frontend's adjustmentNote() (workoutMath.ts)
	// shows a rider everything past "Adjusted automatically:" verbatim as
	// the reason, so a marker appended after it ("...was under target
	// step-down source: <id>") would leak straight into that text. Placing
	// it here keeps it out of what a rider ever reads, while
	// hasStepDownSource can still find it — it just searches the whole
	// description, position included.
	description := wk.Description + " " + adapter.StepDownSourceNote(c.StepDownSourceID) + " " + adapter.Note(c)
	_, err = s.Training.UpdateWorkout(ctx, wk.ID, workout.UpdateWorkoutRequest{
		Name: &req.Name, Steps: &req.Steps, Level: &req.Level, Description: &description,
	})
	return err
}

// readinessHistoryDays bounds how far back assessReadiness reads
// daily_wellness for — readiness.Assess's own widest lookback is the
// resting-HR baseline's 28 days before the reference date (see
// restingHRBaseline), so 29 days back from today covers that plus today
// itself.
const readinessHistoryDays = 29

// assessReadiness builds today's readiness.Day, its wellness history, the
// latest form (TSB) snapshot and the rider's own daily training loads, and
// turns them into an Assessment via readiness.Assess — the one call site
// that knows how to gather what that pure function needs. A failure reading
// wellness history degrades to "no history" (readiness.Assess still runs on
// whatever else is available) rather than aborting the whole adaptation pass
// for a rider whose only problem is an unreadable wellness table.
func (s *Server) assessReadiness(ctx context.Context, rider string, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot) readiness.Assessment {
	return s.assessReadinessAt(ctx, rider, sessions, latest, s.now())
}

// assessReadinessAt is assessReadiness for a caller-chosen "now": the
// tomorrow forecast anchors to the browser's own day (?today=), and the
// verdict feeding it has to describe that same day.
func (s *Server) assessReadinessAt(ctx context.Context, rider string, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot, now time.Time) readiness.Assessment {
	todayStr := now.Format("2006-01-02")
	sinceDate := now.AddDate(0, 0, -readinessHistoryDays).Format("2006-01-02")

	rows, err := s.Training.ListWellness(ctx, rider, sinceDate)
	if err != nil {
		s.logger().Warn("adapt: reading wellness history failed", "rider", rider, "err", err)
		rows = nil
	}

	var today readiness.Day
	var history []readiness.Day
	for _, row := range rows {
		d := readiness.Day{
			Date: row.Date, HRVStatus: row.HRVStatus,
			SleepSeconds: row.SleepSeconds, SleepScore: row.SleepScore,
			ReadinessScore: row.ReadinessScore, ReadinessLevel: row.ReadinessLevel,
			RestingHR: row.RestingHR, Present: true,
		}
		if row.Date == todayStr {
			today = d
		} else {
			history = append(history, d)
		}
	}

	var tsb *float64
	var tsbDate string
	if latest != nil {
		v := latest.TSB
		tsb, tsbDate = &v, latest.Date
	}

	return readiness.Assess(today, history, tsb, tsbDate, dailyLoadsForReadiness(sessions), now)
}

// dailyLoadsForReadiness sums each completed session's own TrainingLoad by
// date — the same per-day aggregation workout.RecomputeFitnessSnapshots'
// own fillDailyLoads performs before ComputeFitness, without that function's
// gap-filling: readiness.Assess's own acwr divides by a fixed 7/28-day
// window regardless of which days are present, so a day with no session
// simply never appearing here already reads as zero load, exactly as a
// filled-in DailyLoad{Load: 0} would.
func dailyLoadsForReadiness(sessions []workout.CompletedSession) []readiness.Load {
	byDate := map[string]float64{}
	for _, s := range sessions {
		byDate[s.Date] += s.TrainingLoad
	}
	loads := make([]readiness.Load, 0, len(byDate))
	for date, load := range byDate {
		loads = append(loads, readiness.Load{Date: date, Load: load})
	}
	return loads
}
