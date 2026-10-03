package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Planning the whole season.
//
// A goal's plan already runs from this week to the event (or twelve rolling
// weeks for a goal with no date); until now only this week and next became
// real sessions, so a rider who set a goal 27 weeks out saw one filled week
// and a long empty calendar. planSeason turns every remaining plan week into
// sessions, under the same fill-once rule as before: a week is filled once
// per (goal, week), recorded in scheduled_weeks, and never topped up, so a
// session the rider deleted, moved or rewrote stays that way.
//
// Two things follow from building weeks months ahead, and both are handled
// here rather than left to chance:
//
//   - A far week is built from today's levels and FTP, which will have moved
//     by the time it arrives. So when a week becomes next week it is
//     refreshed once (refreshWeek): the sessions nobody has touched are
//     rebuilt from the plan and levels as they stand then.
//   - Recording a week as filled must mean something was filled. See fillWeek.

// seasonPassResult is what one planSeason pass did, for the log line.
type seasonPassResult struct {
	weeks              int
	weeksFilled        int
	sessionsCreated    int
	weeksRefreshed     int
	sessionsRefreshed  int
	weeksMarkedFromOld int
	sessionsTrimmed    int
}

// planSeason fills every plan week of g that has not been filled, and refreshes
// the ones that have just come within reach and not yet been. Idempotent: a
// second pass over an unchanged season creates and changes nothing.
//
// The caller holds the scheduling advisory lock (the tick already does; the
// background pass takes it). seasonMu is the in-process half of the same
// guard, for SQLite, where the advisory lock does nothing.
func (s *Server) planSeason(ctx context.Context, g workout.Goal) (seasonPassResult, error) {
	s.seasonMu.Lock()
	defer s.seasonMu.Unlock()

	var res seasonPassResult
	sc, err := s.seasonContext(ctx, g)
	if err != nil {
		return res, err
	}
	recorded, err := s.Training.ScheduledWeeks(ctx, g.ID)
	if err != nil {
		return res, err
	}
	existing, err := s.Training.ListWorkouts(ctx, g.Rider)
	if err != nil {
		return res, err
	}
	horizon := s.refreshHorizon()
	today := s.now().Format(dateLayout)

	for _, week := range sc.plan.Weeks {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res.weeks++
		refreshed, isRecorded := recorded[week.StartDate]
		switch {
		case !isRecorded && hasPlanSessions(existing, g, week):
			// Sessions of this goal already sit in a week nothing recorded: a
			// deployment that predates scheduled_weeks. Record it as filled
			// rather than topping it up.
			if err := s.recordLegacyWeek(ctx, g, week.StartDate); err != nil {
				return res, err
			}
			res.weeksMarkedFromOld++
		case !isRecorded:
			// A week within reach is recorded as fresh the moment it is filled,
			// so it must not be filled from a route that could not be read: the
			// bias would never be applied. A far week is refreshed later anyway.
			if week.StartDate <= horizon && sc.demandUnreadable() {
				continue
			}
			created, _, err := s.fillWeek(ctx, g, sc, week, existing, "")
			existing = append(existing, created...)
			res.sessionsCreated += len(created)
			if len(created) > 0 {
				res.weeksFilled++
			}
			if err != nil {
				return res, err
			}
		case !refreshed && week.StartDate <= horizon:
			if sc.demandUnreadable() {
				continue // try again next pass rather than consume the refresh
			}
			n, err := s.refreshWeek(ctx, g, sc, week, existing, today)
			res.sessionsRefreshed += n
			res.weeksRefreshed++
			if err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

// hasPlanSessions reports whether g already has a plan-made session in week.
// An FTP test is linked to the goal so its day reads as taken, but it is not a
// plan-made session: a rider who scheduled a test into a week that has not
// been filled yet has not had that week filled.
func hasPlanSessions(existing []workout.Workout, g workout.Goal, week periodization.Week) bool {
	start, end := weekBounds(week)
	for _, wk := range existing {
		if wk.GoalID == g.ID && wk.TestProtocol == "" && wk.Date >= start && wk.Date <= end {
			return true
		}
	}
	return false
}

func weekBounds(week periodization.Week) (start, end string) {
	first, err := time.Parse(dateLayout, week.StartDate)
	if err != nil {
		return week.StartDate, week.StartDate
	}
	return week.StartDate, first.AddDate(0, 0, 6).Format(dateLayout)
}

// recordLegacyWeek records a week already holding this goal's sessions as
// filled, and as refreshed when it is close enough that it was built from
// current data anyway.
func (s *Server) recordLegacyWeek(ctx context.Context, g workout.Goal, weekStart string) error {
	if err := s.Training.MarkWeekScheduled(ctx, g.ID, weekStart); err != nil {
		return err
	}
	if weekStart <= s.refreshHorizon() {
		return s.Training.MarkWeekRefreshed(ctx, g.ID, weekStart)
	}
	return nil
}

// refreshWeek rebuilds, in place, the untouched plan-made sessions of a week
// that has just come within reach, from the plan and levels as they stand
// now, and records that it did. It never creates a session: a date with
// nothing on it stays empty, so a session the rider deleted stays deleted.
// It keeps each session's id, so a copy already on a head unit updates rather
// than duplicating.
//
// Untouched means nobody has done anything to it since the plan made it:
// still a generated session with the generated description word for word (so
// no automatic adjustment, easing or move, and no move by the rider, which
// note themselves there), not an FTP test, and never updated since it was
// created (a rider's edit to the name or the steps leaves the description
// alone, so only the timestamp shows it). Only days after today are
// rebuilt, so a session being ridden or already ridden is never touched.
func (s *Server) refreshWeek(ctx context.Context, g workout.Goal, sc seasonContext, week periodization.Week, existing []workout.Workout, today string) (int, error) {
	requests, err := scheduler.WeekWorkouts(week, sc.profile, sc.levels, g.Rider, g.ID, g.Sport, sc.options()...)
	if err != nil {
		return 0, err
	}
	byDate := make(map[string]workout.CreateWorkoutRequest, len(requests))
	for _, req := range requests {
		byDate[req.Date] = req
	}

	blackout, err := s.blackoutFor(ctx, g.Rider)
	if err != nil {
		return 0, err
	}
	start, end := weekBounds(week)
	changed := 0
	for _, wk := range existing {
		if wk.GoalID != g.ID || wk.Date < start || wk.Date > end || wk.Date <= today || blackout[wk.Date] {
			continue
		}
		if !untouchedPlanSession(wk) {
			continue
		}
		req, ok := byDate[wk.Date]
		if !ok || sameContent(wk, req) {
			continue
		}
		sport, name, steps, zone, level := req.Sport, req.Name, req.Steps, req.Zone, req.Level
		if _, err := s.Training.UpdateWorkout(ctx, wk.ID, workout.UpdateWorkoutRequest{
			Sport: &sport, Name: &name, Steps: &steps, Zone: &zone, Level: &level,
		}); err != nil {
			return changed, err
		}
		changed++
		s.recordSeasonRefresh(ctx, g.Rider, wk, req, sc.profile.FTPWatts)
	}
	return changed, s.Training.MarkWeekRefreshed(ctx, g.ID, week.StartDate)
}

// recordSeasonRefresh explains a rebuilt session. It is only called for one
// whose content actually changed (sameContent has already skipped the rest),
// and it leaves the description alone, so the session can still take one
// automatic adaptation later. The workout does not record the FTP it was
// built on, so only today's is stored.
func (s *Server) recordSeasonRefresh(ctx context.Context, rider string, was workout.Workout, now workout.CreateWorkoutRequest, ftp float64) {
	weekday := was.Date
	if d, err := time.Parse(dateLayout, was.Date); err == nil {
		weekday = d.Weekday().String()
	}
	text := fmt.Sprintf("Rebuilt %s from your current levels and FTP.", weekday)
	if now.Level != was.Level {
		label := strings.ReplaceAll(string(now.Zone), "_", " ")
		if label == "" {
			label = now.Name
		}
		if label == "" {
			label = "session"
		}
		text = fmt.Sprintf("Rebuilt %s from your current levels: %s %s, was %s", weekday, strings.ToUpper(label[:1])+label[1:], levelString(now.Level), levelString(was.Level))
	}
	s.recordSubjectAdjustment(ctx, rider, workout.SubjectWorkout, was.ID, why.NewRecord(why.SeasonRefresh, text, why.SeasonRefreshInputs{
		NameFrom: was.Name, NameTo: now.Name, LevelFrom: was.Level, LevelTo: now.Level, FTPTo: ftp,
	}))
}

func levelString(l float64) string { return strconv.FormatFloat(l, 'f', -1, 64) }

func untouchedPlanSession(wk workout.Workout) bool {
	return scheduler.IsGenerated(wk) &&
		wk.Description == scheduler.GeneratedDescription &&
		wk.TestProtocol == "" &&
		// An indoor version is the rider's own choice. Converting already makes
		// a session touched (the note changes its description), but that must
		// not be the only thing keeping the refresh off it.
		!wk.Indoor &&
		// A route link is the rider's own choice, and a rebuilt session would
		// silently drop it or swap the content under a route chosen for the
		// old one. It already moves UpdatedAt, but the guarantee is stated here.
		wk.RouteSlug == "" &&
		// A swap for an alternate is the rider's choice. It already fails the
		// description and timestamp tests, but the guarantee is stated here
		// rather than left to those.
		!strings.Contains(wk.Description, scheduler.SwappedMarker) &&
		wk.PlannedSnapshot == nil &&
		wk.UpdatedAt == wk.CreatedAt
}

func sameContent(wk workout.Workout, req workout.CreateWorkoutRequest) bool {
	return wk.Sport == req.Sport && wk.Name == req.Name && wk.Zone == req.Zone && wk.Level == req.Level &&
		stepsEqual(wk.Steps, req.Steps)
}

func stepsEqual(a, b []workout.WorkoutStep) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.Name != y.Name || x.Intensity != y.Intensity || x.Duration != y.Duration ||
			x.Seconds != y.Seconds || x.Meters != y.Meters || x.Target != y.Target ||
			x.TargetLow != y.TargetLow || x.TargetHigh != y.TargetHigh || x.Repeat != y.Repeat ||
			!stepsEqual(x.Steps, y.Steps) {
			return false
		}
	}
	return true
}

// trimSeason is the other half of editing a goal: when its plan now ends
// earlier (the event moved up, or the goal became undated and so a shorter
// window), the sessions the old plan put beyond the new last week are cleared.
// Only untouched plan-made sessions go (the same test refreshWeek uses), and
// only ones dated after today: whatever the rider moved, edited, had eased,
// scheduled as an FTP test or rode is theirs now, and stays. Each removed
// session's Garmin copy is taken off through the existing best-effort path,
// and the dropped weeks' scheduled_weeks rows are forgotten, so a later
// extension of the date fills them again. Returns how many sessions were
// deleted. Runs in the background goal-save pass, under its lock.
func (s *Server) trimSeason(ctx context.Context, g workout.Goal) (int, error) {
	s.seasonMu.Lock()
	defer s.seasonMu.Unlock()

	sc, err := s.seasonContext(ctx, g)
	if err != nil {
		return 0, err
	}
	if len(sc.plan.Weeks) == 0 {
		return 0, nil
	}
	lastStart := sc.plan.Weeks[len(sc.plan.Weeks)-1].StartDate
	_, lastEnd := weekBounds(sc.plan.Weeks[len(sc.plan.Weeks)-1])
	today := s.now().Format(dateLayout)

	existing, err := s.Training.ListWorkouts(ctx, g.Rider)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, wk := range existing {
		if wk.GoalID != g.ID || wk.Date <= lastEnd || wk.Date <= today || !untouchedPlanSession(wk) {
			continue
		}
		s.removeWorkoutFromGarmin(ctx, wk)
		if err := s.Training.DeleteWorkout(ctx, wk.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, s.Training.DeleteScheduledWeeksAfter(ctx, g.ID, lastStart)
}

// logSeasonPass writes what a pass did. Counts only, and only when something
// happened: a tick that finds a settled season says nothing.
func (s *Server) logSeasonPass(g workout.Goal, res seasonPassResult) {
	if res.sessionsCreated == 0 && res.sessionsRefreshed == 0 && res.sessionsTrimmed == 0 {
		return
	}
	s.logger().Info("season planned", "goal", g.ID, "rider", g.Rider,
		"weeks", res.weeks, "weeksFilled", res.weeksFilled, "created", res.sessionsCreated,
		"refreshed", res.sessionsRefreshed, "trimmed", res.sessionsTrimmed)
}

// seasonPlanErr logs a failed pass for a goal, staying quiet about the two
// errors that are the rider's own data being unsuitable to plan from (no
// event date reaches here only through a bug; an event in the past is a goal
// that outlived its event).
func (s *Server) seasonPlanErr(g workout.Goal, err error) {
	if errors.Is(err, periodization.ErrNoEventDate) || errors.Is(err, periodization.ErrEventInThePast) || errors.Is(err, context.Canceled) {
		return
	}
	// A write that should have landed did not: Error. The tick retries the
	// goal on its next pass when auto-schedule is on; an edit of the goal
	// retries it either way.
	s.logger().Error("season plan failed", "goal", g.ID, "rider", g.Rider, "err", err)
}

// lifecycle is the context background work runs on: the server's own, so it
// stops at shutdown, and never a request's.
func (s *Server) lifecycle() context.Context {
	if s.Lifecycle != nil {
		return s.Lifecycle
	}
	return context.Background()
}

// WaitForBackground blocks until every background season pass has finished.
// Tests use it to look at the result; nothing in production needs to.
func (s *Server) WaitForBackground() { s.background.Wait() }

// seasonFillAttempts and the default retry wait bound how long a background
// pass keeps trying when the scheduling lock is held by another pod's tick.
const (
	seasonFillAttempts     = 6
	defaultSeasonFillRetry = 5 * time.Second
)

// planGoalNow is what saving a goal does. This week is filled before the
// response goes out, so the Plan page the rider lands on is not empty; the
// rest of the season is planned in the background, so saving does not wait on
// a hundred inserts. Independent of the auto-schedule flag: the rider asked
// for this goal, and the flag governs what the app does unprompted.
func (s *Server) planGoalNow(ctx context.Context, g workout.Goal) {
	s.fillThisWeek(ctx, g)
	s.startSeasonFill(g)
}

// fillThisWeek fills the current week of g on the request's own context,
// under the scheduling lock so it cannot race the tick filling the same new
// goal. If the lock is held it does nothing: the background pass starts with
// this same week and gets it once the lock frees.
func (s *Server) fillThisWeek(ctx context.Context, g workout.Goal) {
	var err error
	withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
		s.seasonMu.Lock()
		defer s.seasonMu.Unlock()
		_, _, err = s.autoScheduleGoalWeek(ctx, g, periodization.MondayOf(s.now()))
	})
	if err != nil && !errors.Is(err, periodization.ErrNoEventDate) && !errors.Is(err, periodization.ErrEventInThePast) {
		// The response still goes out and the background pass tries again.
		s.logger().Warn("could not fill this week for a new goal", "goal", g.ID, "rider", g.Rider, "err", err)
	}
}

// startSeasonFill plans every week of g on a detached goroutine. The context
// derives from the server's lifecycle, not the request's, or it would be
// cancelled the moment the response was written.
func (s *Server) startSeasonFill(g workout.Goal) {
	ctx := s.lifecycle()
	retry := s.SeasonFillRetry
	if retry <= 0 {
		retry = defaultSeasonFillRetry
	}
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		if s.BeforeSeasonFill != nil {
			s.BeforeSeasonFill()
		}
		for attempt := 0; attempt < seasonFillAttempts; attempt++ {
			if ctx.Err() != nil {
				return
			}
			var (
				res  seasonPassResult
				err  error
				gone bool
				cur  workout.Goal
			)
			ran := withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
				// Read again inside the lock: the goal may have been edited or
				// deleted while this waited, and the plan is built from it.
				cur, err = s.Training.GetGoal(ctx, g.ID)
				if errors.Is(err, workout.ErrGoalNotFound) {
					gone, err = true, nil
					return
				}
				if err != nil {
					return
				}
				res, err = s.planSeason(ctx, cur)
				if err == nil {
					res.sessionsTrimmed, err = s.trimSeason(ctx, cur)
				}
			})
			if ran {
				if gone {
					return
				}
				if err != nil {
					s.seasonPlanErr(g, err)
					return
				}
				s.logSeasonPass(cur, res)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(retry):
			}
		}
		s.logger().Warn("season plan not started: scheduling stayed busy", "goal", g.ID, "rider", g.Rider)
	}()
}
