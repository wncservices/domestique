package api

import (
	"context"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/crewplan"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// This file gathers what readiness.ForecastTomorrow needs, the way
// assessReadiness does for today's Assess. Nothing here reads the process's
// local time zone: every date is a calendar date derived from the "today"
// the caller passes (the browser's own day, or the server's UTC day for the
// sync tick), so the results do not move with TZ. See
// docs/superpowers/specs/2026-09-28-readiness-tomorrow-design.md.

const dateFormat = "2006-01-02"

// calendarDay reduces t to midnight UTC of the calendar date it shows, in
// whatever zone it carries — the caller has already decided which day it
// means, and a zone conversion here would move it.
func calendarDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// maxConsecutiveHardDays caps consecutiveHardDays: ForecastTomorrow only
// asks whether the run reaches 2, so counting further buys nothing.
const maxConsecutiveHardDays = 7

// forecastTomorrow builds readiness.TomorrowInput and returns the forecast
// plus the workout it applies to — ok is false when tomorrow has nothing
// generated, hard, untouched and not yet done to ease, in which case there
// is nothing to warn about and nothing an "ease tomorrow" click could do.
// The one call site that knows how to gather what the pure forecast needs,
// same role assessReadiness plays for today's Assess.
//
// todayAssessment is today's own verdict, already computed once by the
// caller for the same day. Each input degrades to "no reason" on its own
// (no FTP, no or stale snapshot, thin load history) without touching the
// others.
func forecastTomorrow(today time.Time, workouts []workout.Workout, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot, todayAssessment readiness.Assessment, profile workout.RiderProfile) (readiness.Assessment, workout.Workout, bool) {
	today = calendarDay(today)
	tomorrowStr := today.AddDate(0, 0, 1).Format(dateFormat)

	var target workout.Workout
	found := false
	for _, w := range workouts {
		if w.Date == tomorrowStr && scheduler.IsGenerated(w) && scheduler.IsHardSession(w) && !adapter.WorkoutDone(w, sessions) {
			target, found = w, true
			break
		}
	}
	if !found {
		return readiness.Assessment{}, workout.Workout{}, false
	}

	forecast := outlookTomorrow(today, workouts, sessions, latest, todayAssessment, profile)

	// A caution is applied as a step-down, which needs a ladder for the
	// workout's sport and zone and a rung below its level; a legacy
	// zone-less workout, or a zone with no ladder for its sport, has none,
	// and offering to ease something the click could only fail on would be
	// a banner that turns into a 500.
	if forecast.Verdict == readiness.Caution {
		if _, _, err := stepDownRung(target); err != nil {
			return readiness.Assessment{}, workout.Workout{}, false
		}
	}
	return forecast, target, true
}

// outlookTomorrow is the forecast itself, for tomorrow whatever is planned on
// it: forecastTomorrow asks it for a hard session it may ease, and the crew
// ride advice asks it about a ride it never changes. Each input degrades to "no
// reason" on its own (no FTP, no or stale snapshot, thin load history) without
// touching the others.
func outlookTomorrow(today time.Time, workouts []workout.Workout, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot, todayAssessment readiness.Assessment, profile workout.RiderProfile) readiness.Assessment {
	today = calendarDay(today)
	todayStr := today.Format(dateFormat)

	in := readiness.TomorrowInput{
		TodayVerdict:        todayAssessment.Verdict,
		ConsecutiveHardDays: consecutiveHardDays(workouts, sessions, today),
	}

	dayLoads := map[string]float64{}
	for _, s := range sessions {
		dayLoads[s.Date] += s.TrainingLoad
	}
	todayLoad, haveTodayLoad := todayTrainingLoad(workouts, sessions, profile.FTPWatts, todayStr)

	if haveTodayLoad {
		in.ProjectedTSB, in.HaveProjectedTSB = projectedTSB(latest, dayLoads, todayLoad, today)
	}
	// An unknown load for today (a hard session planned and no FTP to size
	// it) cannot be counted, but that only weakens the ratio to "as of the
	// sessions on file" - no session has synced yet in that case, so 0 is
	// what is on file.
	in.ACWR, in.HaveACWR = acwrThroughToday(dailyLoadsForReadiness(sessions), todayLoad, today)

	return readiness.ForecastTomorrow(in)
}

// projectedTSB rolls latest's CTL/ATL forward to the start of tomorrow and
// returns the form (CTL - ATL) there. A snapshot's CTL/ATL are the values at
// the start of its own date (see workout.ComputeFitness), so every day from
// the snapshot's date up to and including today is folded in: days before
// today from dayLoads, today from todayLoad (actual, or the caller's
// estimate). ok is false with no snapshot, or one dated more than 2 days
// before today or after it — the same freshness gate readiness applies to
// today's own form rule.
func projectedTSB(latest *workout.FitnessSnapshot, dayLoads map[string]float64, todayLoad float64, today time.Time) (float64, bool) {
	if latest == nil {
		return 0, false
	}
	snapDay, err := time.Parse(dateFormat, latest.Date)
	if err != nil {
		return 0, false
	}
	today = calendarDay(today)
	age := today.Sub(snapDay)
	if age < 0 || age > 2*24*time.Hour {
		return 0, false
	}

	ctl, atl := latest.CTL, latest.ATL
	for d := snapDay; d.Before(today); d = d.AddDate(0, 0, 1) {
		ctl, atl = workout.RollFitness(ctl, atl, dayLoads[d.Format(dateFormat)])
	}
	ctl, atl = workout.RollFitness(ctl, atl, todayLoad)
	return ctl - atl, true
}

// todayTrainingLoad is today's own load: the completed sessions dated today
// if any have synced, else an estimate from today's planned hard workouts'
// power targets (adapter.EstimatePlannedTSS, the same estimate
// overloadedWeek makes). A day with nothing hard planned and nothing ridden
// truly contributes about zero, which is a real answer rather than a
// missing one. ok is false only when a hard session is planned and there is
// no FTP to size it against — the same gate overloadedWeek uses.
func todayTrainingLoad(workouts []workout.Workout, sessions []workout.CompletedSession, ftpWatts float64, todayStr string) (float64, bool) {
	var actual float64
	haveActual := false
	for _, s := range sessions {
		if s.Date == todayStr {
			actual += s.TrainingLoad
			haveActual = true
		}
	}
	if haveActual {
		return actual, true
	}

	var estimate float64
	plannedHard := false
	for _, w := range workouts {
		// A crew ride today is priced by its own estimate (hours x 0.65^2 x 100),
		// which needs no FTP, and not by the flat default an open step would
		// get: it is the biggest load many riders have all week.
		if w.Date == todayStr && w.CrewRideID != "" && !adapter.WorkoutDone(w, sessions) {
			estimate += crewplan.TSSForSeconds(workout.PlannedSeconds(w.Steps))
			continue
		}
		if w.Date != todayStr || !scheduler.IsHardSession(w) {
			continue
		}
		plannedHard = true
		estimate += adapter.EstimatePlannedTSS(w, ftpWatts)
	}
	if plannedHard && ftpWatts <= 0 {
		return 0, false
	}
	return estimate, true
}

// acwrThroughToday is readiness's own acute:chronic ratio with today's load
// counted: any entry already dated today is replaced by todayLoad (actual
// or estimate — never both, or an already-synced session would count
// twice). ok mirrors readiness's own 21-day-coverage gate.
func acwrThroughToday(loads []readiness.Load, todayLoad float64, today time.Time) (float64, bool) {
	today = calendarDay(today)
	todayStr := today.Format(dateFormat)
	withToday := make([]readiness.Load, 0, len(loads)+1)
	for _, l := range loads {
		if l.Date != todayStr {
			withToday = append(withToday, l)
		}
	}
	withToday = append(withToday, readiness.Load{Date: todayStr, Load: todayLoad})
	return readiness.ACWR(withToday, today)
}

// consecutiveHardDays counts the unbroken run of calendar days, ending at
// and including today, that have a plan-made hard workout on them — a day
// with none (an easy day, or nothing planned at all) breaks the run. Days
// strictly before today count only if the session was actually done
// (adapter.WorkoutDone): a skipped hard session tires nobody. Today counts
// as planned, since the forecast assumes today gets ridden. A workout an
// adjustment has already eased still counts if it is still hard by zone (a
// step-down stays in its zone), which is why this does not require
// scheduler.IsGenerated the way eligibility does.
func consecutiveHardDays(workouts []workout.Workout, sessions []workout.CompletedSession, today time.Time) int {
	today = calendarDay(today)
	todayStr := today.Format(dateFormat)
	hard := map[string]bool{}
	for _, w := range workouts {
		if w.GoalID == "" || !scheduler.IsHardSession(w) {
			continue
		}
		if w.Date == todayStr || (w.Date < todayStr && adapter.WorkoutDone(w, sessions)) {
			hard[w.Date] = true
		}
	}
	n := 0
	for d := today; n < maxConsecutiveHardDays && hard[d.Format(dateFormat)]; d = d.AddDate(0, 0, -1) {
		n++
	}
	return n
}

// logTomorrowAdvisory is the sync tick's own look at tomorrow: it logs at
// Info, rider and verdict word only, when the forecast is rest or caution.
// It observes and changes nothing — a rider's own click is the only thing
// that eases tomorrow. Health-adjacent numbers (form, load, ratio) never
// appear next to the rider name.
//
// "Tomorrow" here is the server's UTC tomorrow: the tick has no browser to
// ask, and being a few hours off around local midnight costs nothing on an
// observational line. Workouts are re-read because this runs after the
// pass's own changes, which may have just eased today's session.
func (s *Server) logTomorrowAdvisory(ctx context.Context, rider string, sessions []workout.CompletedSession, latest *workout.FitnessSnapshot, profile workout.RiderProfile) {
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.logger().Warn("adapt: listing workouts for the tomorrow forecast failed", "rider", rider, "err", err)
		return
	}
	// One UTC-anchored "now" for both today's verdict and the forecast, so
	// they describe the same day whatever zone the process runs in.
	today := calendarDay(s.now().UTC())
	todayAssessment := s.assessReadinessForForecast(ctx, rider, sessions, latest, today)
	forecast, _, ok := forecastTomorrow(today, workouts, sessions, latest, todayAssessment, profile)
	// Nothing to advise on a day the rider is away. A failure reading events
	// only costs a log line, so it falls through to the usual advisory.
	if blackout, err := s.blackoutFor(ctx, rider); err == nil && blackout[today.AddDate(0, 0, 1).Format(dateLayout)] {
		ok = false
	}
	if ok && forecast.Verdict != readiness.Ready {
		s.logger().Info("tomorrow's session may need easing", "rider", rider, "risk", string(forecast.Verdict))
	}
}
