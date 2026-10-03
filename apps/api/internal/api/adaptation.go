package api

import (
	"context"
	"fmt"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
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

	// A life event takes days out of the plan. Adaptation must neither make a
	// session up on one nor read a session removed for one as missed, so it
	// does not run at all when the events cannot be read.
	events, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		s.logger().Warn("adapt: reading life events failed", "rider", rider, "err", err)
		return
	}
	blackout := lifeevents.Blackout(events)

	// AdaptSessions already keeps each workout to at most one Change per pass
	// (stepDownTarget skips ids its own per-session loop has claimed — see
	// its doc comment); this is a second, independent guard here so a bug in
	// that bookkeeping cannot silently apply two Changes to the same workout
	// — first one wins, and a second is loud rather than a quiet clobber.
	appliedFor := map[string]bool{}
	for _, c := range adapter.AdaptSessionsAround(workouts, sessions, profile, s.now(), byWorkout, assessment, blackout) {
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
		// The rule id, never the reason: a reason quotes sleep scores, HRV and
		// form, and a health value does not belong in a log line beside a
		// rider's name. The stored adjustment (see recordAdjustment) is where
		// the explanation lives, and it is only ever shown to its owner.
		s.logger().Info("workout adapted automatically", "workout", wk.ID, "rider", rider, "change", what, "rule", string(c.Why.Rule))
	}

	// The return ramp for weeks filled after an event was made, after the
	// rules above so one workout is never changed twice, and before the
	// day-before-a-test easing for the same reason.
	s.applyReturnRamp(ctx, rider, workouts, profile, events, appliedFor)
	// A session the rider kept on an event day is not eased for a test.
	for _, w := range workouts {
		if blackout[w.Date] {
			appliedFor[w.ID] = true
		}
	}

	s.easeBeforeFTPTests(ctx, rider, workouts, profile, appliedFor)
	s.easeAroundCrewRides(ctx, rider, workouts, profile, appliedFor)
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
		down, err := s.applyStepDown(ctx, wk, profile, c)
		if err != nil {
			s.logger().Warn("adapt: could not step a workout down", "workout", wk.ID, "rider", rider, "err", err)
			return "", err
		}
		if c.Why.Rule == why.StruggleStepDown {
			// Only the API layer knows which rung the step-down landed on.
			c.Why.Inputs["levelFrom"], c.Why.Inputs["levelTo"] = float64(wk.Level), float64(down.toLevel)
		}
		s.recordAdjustment(ctx, rider, wk.ID, c.Why, down.keptIndoor)
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
		s.keepIndoor(&req, wk, profile, easy.Zone)
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
	// Recorded only now that the change has landed, so a change that failed
	// never leaves a reason for something that did not happen.
	s.recordAdjustment(ctx, rider, wk.ID, c.Why, req.Indoor != nil && *req.Indoor)
	return what, nil
}

// recordAdjustment stores why a workout was changed. Keeping a session indoors
// through the easing is part of the easing, so it is one more input on the same
// row, never a row of its own. A failed write is a Warn and never undoes the
// change: the workout description still carries the reason as text, and the
// popover falls back to it.
func (s *Server) recordAdjustment(ctx context.Context, rider, workoutID string, rec why.Record, keptIndoor bool) {
	if rec.Rule == "" {
		return
	}
	if keptIndoor {
		if rec.Inputs == nil {
			rec.Inputs = map[string]any{}
		}
		rec.Inputs["indoor"] = true
	}
	s.recordSubjectAdjustment(ctx, rider, workout.SubjectWorkout, workoutID, rec)
}

// recordSubjectAdjustment is the one place an adjustment is written and its
// failure handled: a Warn naming the rule (never the inputs, which are health
// or fitness values) and nothing else, because the change it explains has
// already landed and stands.
func (s *Server) recordSubjectAdjustment(ctx context.Context, rider, kind, subjectID string, rec why.Record) {
	day := s.now().Format("2006-01-02")
	if err := s.Training.RecordAdjustment(ctx, rider, kind, subjectID, rec, day); err != nil {
		s.logger().Warn("could not record why a change was made", "kind", kind, "subject", subjectID, "rider", rider, "rule", string(rec.Rule), "err", err)
	}
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
func (s *Server) applyStepDown(ctx context.Context, wk workout.Workout, profile workout.RiderProfile, c adapter.Change) (stepDownResult, error) {
	ladder, rung, err := stepDownRung(wk)
	if err != nil {
		return stepDownResult{}, err
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
	update := workout.UpdateWorkoutRequest{
		Name: &req.Name, Steps: &req.Steps, Level: &req.Level, Description: &description,
	}
	// The step-down stays in the workout's own zone, so that is the zone the
	// replacement's steps are converted under.
	s.keepIndoor(&update, wk, profile, wk.Zone)
	if _, err = s.Training.UpdateWorkout(ctx, wk.ID, update); err != nil {
		return stepDownResult{}, err
	}
	return stepDownResult{toLevel: rung.Level, keptIndoor: update.Indoor != nil && *update.Indoor}, nil
}

// stepDownResult is what applyStepDown tells its caller about the session it
// wrote, for the record of why: the rung it landed on, and whether an indoor
// session stayed indoors.
type stepDownResult struct {
	toLevel    int
	keptIndoor bool
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
	return s.assessReadinessWith(ctx, rider, sessions, latest, now, true)
}

// assessReadinessForForecast is today's assessment as the tomorrow forecast
// reads it: without the post-ride survey. The forecast's inputs are load-based
// by design, and how the legs felt is today's caution, not a reason to ease
// tomorrow too (see docs/superpowers/specs/2026-09-29-ride-survey-and-why-design.md).
func (s *Server) assessReadinessForForecast(ctx context.Context, rider string, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot, now time.Time) readiness.Assessment {
	return s.assessReadinessWith(ctx, rider, sessions, latest, now, false)
}

func (s *Server) assessReadinessWith(ctx context.Context, rider string, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot, now time.Time, withSurvey bool) readiness.Assessment {
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
			HRVLastNight: row.HRVLastNight, HRVWeeklyAvg: row.HRVWeeklyAvg,
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

	var survey []readiness.SurveyDay
	if withSurvey {
		survey = s.surveyDays(ctx, rider, sessions, now)
	}
	return readiness.AssessWithSurvey(today, history, tsb, tsbDate, dailyLoadsForReadiness(sessions), survey, now)
}

// surveyLookbackDays is how far back readiness reads the post-ride survey: its
// heavy-legs rule needs today, yesterday and the day before.
const surveyLookbackDays = 3

// surveyDays folds the rider's recent survey answers into one entry per day.
// Analyses carry no date of their own, so each is dated by its session; several
// rides in a day become one entry where heavy legs and high stress win, since
// either on any ride is what the rule is asking about. A failed read degrades
// to no survey, as a failed wellness read does.
func (s *Server) surveyDays(ctx context.Context, rider string, sessions []workout.CompletedSession, now time.Time) []readiness.SurveyDay {
	since := now.AddDate(0, 0, -surveyLookbackDays).Format("2006-01-02")
	analyses, err := s.Training.ListAnalyses(ctx, rider, since)
	if err != nil {
		s.logger().Warn("adapt: reading the ride survey failed", "rider", rider, "err", err)
		return nil
	}
	dateOf := make(map[string]string, len(sessions))
	for _, sess := range sessions {
		dateOf[sess.ID] = sess.Date
	}
	byDate := map[string]*readiness.SurveyDay{}
	var order []string
	for _, a := range analyses {
		date, ok := dateOf[a.SessionID]
		if !ok || (a.Legs == "" && a.Stress == "") {
			continue
		}
		day, seen := byDate[date]
		if !seen {
			day = &readiness.SurveyDay{Date: date}
			byDate[date] = day
			order = append(order, date)
		}
		if a.Legs == "heavy" || day.Legs == "" {
			day.Legs = a.Legs
		}
		if a.Stress == "high" || day.Stress == "" {
			day.Stress = a.Stress
		}
	}
	out := make([]readiness.SurveyDay, 0, len(order))
	for _, date := range order {
		out = append(out, *byDate[date])
	}
	return out
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
