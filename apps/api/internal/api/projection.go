package api

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/projection"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The race-day projection endpoint.
//
// Everything here is derived on every read from rows that already exist (the
// snapshots, the sessions, the planned workouts, the goals, the profile and
// scheduled_weeks), so there is no table, nothing cached, and nothing for a
// reschedule, a ride sync or an FTP change to invalidate. It writes nothing.
// The maths lives in internal/projection; this file only assembles inputs.

type projectionBandDTO struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

type projectionGoalDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	EventDate string `json:"eventDate"`
	Priority  string `json:"priority"`
}

type projectionPointDTO struct {
	Date string  `json:"date"`
	CTL  float64 `json:"ctl"`
	ATL  float64 `json:"atl"`
	TSB  float64 `json:"tsb"`
	Load float64 `json:"load"`
}

type projectionRaceDayDTO struct {
	Date string  `json:"date"`
	CTL  float64 `json:"ctl"`
	ATL  float64 `json:"atl"`
	TSB  float64 `json:"tsb"`
}

type projectionVerdictDTO struct {
	Key     string `json:"key"`
	Message string `json:"message"`
	Tone    string `json:"tone,omitempty"`
}

type projectionRampWarningDTO struct {
	WeekStart string  `json:"weekStart"`
	PerWeek   float64 `json:"perWeek"`
	ExcessTSS float64 `json:"excessTss"`
}

type projectionRampDTO struct {
	MaxPerWeek float64                    `json:"maxPerWeek"`
	Warnings   []projectionRampWarningDTO `json:"warnings"`
}

// projectionSuggestionDTO is text plus the numbers behind it. Nothing applies
// it: a plan changes where it always did.
type projectionSuggestionDTO struct {
	Text      string   `json:"text"`
	Days      int      `json:"days,omitempty"`
	TSB       *float64 `json:"tsb,omitempty"`
	Reaches   *bool    `json:"reaches,omitempty"`
	ExcessTSS float64  `json:"excessTss,omitempty"`
}

type projectionEventDTO struct {
	GoalID   string            `json:"goalId"`
	Name     string            `json:"name"`
	Date     string            `json:"date"`
	Priority string            `json:"priority"`
	CTL      float64           `json:"ctl"`
	ATL      float64           `json:"atl"`
	TSB      float64           `json:"tsb"`
	Band     projectionBandDTO `json:"band"`
}

type projectionDTO struct {
	Available   bool                     `json:"available"`
	Reason      string                   `json:"reason,omitempty"`
	Goal        *projectionGoalDTO       `json:"goal"`
	Points      []projectionPointDTO     `json:"points"`
	RaceDay     *projectionRaceDayDTO    `json:"raceDay"`
	Band        *projectionBandDTO       `json:"band"`
	TargetCTL   float64                  `json:"targetCtl"`
	Verdict     *projectionVerdictDTO    `json:"verdict"`
	Ramp        *projectionRampDTO       `json:"ramp"`
	Suggestion  *projectionSuggestionDTO `json:"suggestion"`
	Events      []projectionEventDTO     `json:"events"`
	Assumptions []string                 `json:"assumptions"`
}

// handleGetProjection answers "if I ride the plan, will I arrive fresh and
// fit?" for the primary event: the nearest A goal, else the nearest goal of any
// priority (which then gets no verdict). ?goal=<id> picks another of the
// rider's own goals; one that is not theirs is a 404, never a 403.
func (s *Server) handleGetProjection(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User
	now := s.now()
	today := now.Format(dateLayout)

	out := projectionDTO{Points: []projectionPointDTO{}, Events: []projectionEventDTO{}, Assumptions: []string{}}
	finish := func(goalID string) {
		key := "none"
		if out.Verdict != nil {
			key = out.Verdict.Key
		} else if !out.Available {
			key = string(projection.KeyUnavailable)
		}
		// Rider, goal and verdict key only: a CTL, a TSB or an FTP next to a
		// rider's name is a health value in a log line.
		s.logger().Info("race-day projection", "rider", rider, "goal", goalID, "verdict", key)
		writeJSON(w, http.StatusOK, out)
	}
	unavailable := func(g *workout.Goal, reason string) {
		out.Available, out.Reason = false, reason
		if g != nil {
			out.Goal = &projectionGoalDTO{ID: g.ID, Name: g.Name, EventDate: g.EventDate, Priority: string(g.Priority)}
			if g.Priority == workout.PriorityA {
				out.Verdict = &projectionVerdictDTO{Key: string(projection.KeyUnavailable), Message: reason}
			}
			finish(g.ID)
			return
		}
		finish("")
	}
	degraded := func(g *workout.Goal, what string, err error) {
		s.logger().Warn("race-day projection input unavailable", "rider", rider, "input", what, "err", err)
		unavailable(g, "Your form cannot be projected right now")
	}

	goals, err := s.Training.ListGoals(ctx, rider)
	if err != nil {
		degraded(nil, "goals", err)
		return
	}
	target, found := pickProjectionGoal(goals, today, r.URL.Query().Get("goal"))
	if want := r.URL.Query().Get("goal"); want != "" && !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": workout.ErrGoalNotFound.Error()})
		return
	}
	if !found {
		unavailable(nil, "No upcoming event: add a goal with a date to project your form")
		return
	}
	g := &target

	eventDay, err := time.Parse(dateLayout, g.EventDate)
	todayDay, _ := time.Parse(dateLayout, today)
	days := int(eventDay.Sub(todayDay).Hours() / 24)
	switch {
	case err != nil:
		unavailable(g, "This goal has no event date")
		return
	case days == 0:
		unavailable(g, "The event is today")
		return
	case days < 0:
		unavailable(g, "The event has passed")
		return
	case days > projection.MaxDays:
		unavailable(g, fmt.Sprintf("The event is more than %d days away", projection.MaxDays))
		return
	}

	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		degraded(g, "profile", err)
		return
	}
	if profile.FTPWatts <= 0 {
		unavailable(g, "Set your FTP to project your form")
		return
	}
	snapshots, err := s.Training.ListFitnessSnapshots(ctx, rider)
	if err != nil {
		degraded(g, "snapshots", err)
		return
	}
	if len(snapshots) == 0 {
		unavailable(g, "Ride a few sessions first: there is no training history to project from")
		return
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		degraded(g, "sessions", err)
		return
	}
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		degraded(g, "workouts", err)
		return
	}

	actual := map[string]float64{}
	for _, sess := range sessions {
		actual[sess.Date] += sess.TrainingLoad
	}
	// Every workout the rider owns counts, not only this goal's: tests,
	// rider-built sessions and other goals' sessions all load the body.
	planned := map[string]float64{}
	otherSports := false
	for _, wk := range workouts {
		if wk.Date < today {
			continue
		}
		if wk.Sport != model.SportCycling {
			otherSports = true
			continue
		}
		// The stored steps are what the rider rides, indoor form included.
		if tss, ok := adapter.PlannedTSS(wk, profile.FTPWatts); ok {
			planned[wk.Date] += tss
		}
	}
	start := snapshots[len(snapshots)-1]
	in := projection.Input{Start: start, Actual: actual, Planned: planned, Today: today, Event: g.EventDate}

	points := projection.Roll(in)
	if len(points) == 0 {
		unavailable(g, "Your form cannot be projected right now")
		return
	}
	for _, p := range points {
		out.Points = append(out.Points, projectionPointDTO{Date: p.Date, CTL: p.CTL, ATL: p.ATL, TSB: p.TSB, Load: p.Load})
	}
	raceDay := points[len(points)-1]
	out.Available = true
	out.Goal = &projectionGoalDTO{ID: g.ID, Name: g.Name, EventDate: g.EventDate, Priority: string(g.Priority)}
	out.RaceDay = &projectionRaceDayDTO{Date: raceDay.Date, CTL: raceDay.CTL, ATL: raceDay.ATL, TSB: raceDay.TSB}

	hours, stated := projection.EventDuration(g.TargetDistanceM, g.TargetElevationM)
	band := projection.BandFor(hours)
	out.Band = &projectionBandDTO{Low: band.Low, High: band.High}
	out.TargetCTL = projection.TargetCTL(hours)

	// Every future goal within the horizon, projected to its own date: B and C
	// are information only, no verdict and no band colouring.
	for _, ev := range goals {
		if ev.EventDate <= today {
			continue
		}
		evIn := in
		evIn.Event = ev.EventDate
		pts := projection.Roll(evIn)
		if len(pts) == 0 {
			continue
		}
		end := pts[len(pts)-1]
		evHours, _ := projection.EventDuration(ev.TargetDistanceM, ev.TargetElevationM)
		evBand := projection.BandFor(evHours)
		out.Events = append(out.Events, projectionEventDTO{
			GoalID: ev.ID, Name: ev.Name, Date: ev.EventDate, Priority: string(ev.Priority),
			CTL: end.CTL, ATL: end.ATL, TSB: end.TSB, Band: projectionBandDTO{Low: evBand.Low, High: evBand.High},
		})
	}
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].Date < out.Events[j].Date })

	out.Assumptions = projectionAssumptions(profile.FTPWatts, otherSports, goals, *g, today, hours, stated)

	if g.Priority != workout.PriorityA {
		finish(g.ID)
		return
	}

	// The verdict for an A goal needs to know how much of the plan is real.
	plan, _, err := s.reconciledPeriodizationPlan(ctx, *g, rider)
	if err != nil {
		degraded(g, "plan", err)
		return
	}
	recorded, err := s.Training.ScheduledWeeks(ctx, g.ID)
	if err != nil {
		degraded(g, "scheduled weeks", err)
		return
	}
	covered, total := planWeeksCovered(plan.Weeks, recorded, g.EventDate)

	verdict := projection.Judge(projection.JudgeInput{
		CTL: raceDay.CTL, TSB: raceDay.TSB, Band: band, TargetCTL: out.TargetCTL,
		WeeksCovered: covered, WeeksTotal: total,
	})
	out.Verdict = &projectionVerdictDTO{Key: string(verdict.Key), Message: verdict.Message, Tone: verdict.Tone}

	// The ramp reads from the current week's Monday on, so a week that has
	// already gone by is never flagged.
	thisMonday := periodization.MondayOf(todayDay).Format(dateLayout)
	var recent []projection.Point
	for _, p := range projection.RollWithHistory(in) {
		if p.Date >= thisMonday {
			recent = append(recent, p)
		}
	}
	ramp := projection.Ramps(recent)
	out.Ramp = &projectionRampDTO{MaxPerWeek: ramp.MaxPerWeek, Warnings: []projectionRampWarningDTO{}}
	for _, wn := range ramp.Warnings {
		out.Ramp.Warnings = append(out.Ramp.Warnings, projectionRampWarningDTO{WeekStart: wn.WeekStart, PerWeek: wn.PerWeek, ExcessTSS: wn.ExcessTSS})
	}

	if sug := projection.Suggest(in, verdict, band, ramp); sug != nil {
		dto := &projectionSuggestionDTO{Text: sug.Text, ExcessTSS: sug.ExcessTSS}
		if sug.Days > 0 {
			tsb, reaches := sug.TSB, sug.Reaches
			dto.Days, dto.TSB, dto.Reaches = sug.Days, &tsb, &reaches
		}
		out.Suggestion = dto
	}
	finish(g.ID)
}

// pickProjectionGoal returns the goal asked for by id, else the nearest
// upcoming A goal, else the nearest upcoming goal of any priority. A goal
// dated today still counts as upcoming so the rider is told why there is
// nothing to project rather than being handed a different event.
func pickProjectionGoal(goals []workout.Goal, today, id string) (workout.Goal, bool) {
	if id != "" {
		for _, g := range goals {
			if g.ID == id {
				return g, true
			}
		}
		return workout.Goal{}, false
	}
	var nearestA, nearest *workout.Goal
	for i := range goals {
		g := &goals[i]
		if g.EventDate == "" || g.EventDate < today {
			continue
		}
		if nearest == nil || g.EventDate < nearest.EventDate {
			nearest = g
		}
		if g.Priority == workout.PriorityA && (nearestA == nil || g.EventDate < nearestA.EventDate) {
			nearestA = g
		}
	}
	switch {
	case nearestA != nil:
		return *nearestA, true
	case nearest != nil:
		return *nearest, true
	}
	return workout.Goal{}, false
}

// planWeeksCovered counts the plan weeks up to the event and how many of them
// have been turned into workouts (recorded in scheduled_weeks).
func planWeeksCovered(weeks []periodization.Week, recorded map[string]bool, eventDate string) (covered, total int) {
	for _, wk := range weeks {
		if wk.StartDate > eventDate {
			continue
		}
		total++
		if _, ok := recorded[wk.StartDate]; ok {
			covered++
		}
	}
	return covered, total
}

func projectionAssumptions(ftp float64, otherSports bool, goals []workout.Goal, target workout.Goal, today string, hours float64, stated bool) []string {
	a := []string{
		"Assumes you ride the plan as written.",
		fmt.Sprintf("Uses your FTP of %.0f W today; targets will move if your FTP does.", ftp),
	}
	if otherSports {
		a = append(a, "Planned load is estimated from each workout's power targets; cycling sessions only.")
	}
	a = append(a, "Days with no planned workout count as rest.")
	for _, g := range goals {
		if g.ID != target.ID && g.EventDate > today && g.EventDate < target.EventDate && g.Priority != workout.PriorityA {
			a = append(a, "Does not include the load of B and C events.")
			break
		}
	}
	if stated {
		a = append(a, fmt.Sprintf("Event length estimated at %.1f h from distance and elevation.", hours))
	} else {
		a = append(a, "No distance stated: assuming a medium-length event.")
	}
	return a
}
