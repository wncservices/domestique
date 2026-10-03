package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/compliance"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const dateLayout = "2006-01-02"

type weekFocusDTO struct {
	GoalID      string  `json:"goalId"`
	Name        string  `json:"name"`
	Priority    string  `json:"priority"`
	Sport       string  `json:"sport"`
	EventDate   string  `json:"eventDate,omitempty"`
	DaysToEvent *int    `json:"daysToEvent,omitempty"`
	WeekNumber  int     `json:"weekNumber,omitempty"`
	TotalWeeks  int     `json:"totalWeeks,omitempty"`
	Phase       string  `json:"phase,omitempty"`
	Recovery    bool    `json:"recovery,omitempty"`
	TargetHours float64 `json:"targetHours,omitempty"`
}

type weekDayDTO struct {
	Date      string                `json:"date"`
	Status    compliance.Status     `json:"status"`
	Planned   []workoutDTO          `json:"planned"`
	Completed []completedSessionDTO `json:"completed"`
}

type weekTotalsDTO struct {
	PlannedSeconds   float64 `json:"plannedSeconds"`
	CompletedSeconds float64 `json:"completedSeconds"`
}

type trainingWeekDTO struct {
	Start  string        `json:"start"`
	End    string        `json:"end"`
	Today  string        `json:"today"`
	Focus  *weekFocusDTO `json:"focus,omitempty"`
	Days   []weekDayDTO  `json:"days"`
	Totals weekTotalsDTO `json:"totals"`
	// LifeEvents are the rider's events overlapping this week, for the bands.
	LifeEvents []lifeEventDTO `json:"lifeEvents,omitempty"`
}

// handleTrainingWeek is the Plan page's one read: seven days with what was
// planned, what was ridden and how the two compare, plus the focus goal's
// place in its plan. Assembled server-side so the matching rules live in
// Go next to the scheduler that produced the plan, not re-derived in the
// browser.
func (s *Server) handleTrainingWeek(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	ctx := r.Context()
	rider := auth.FromContext(ctx).User
	now := s.now()

	start := periodization.MondayOf(now)
	if q := r.URL.Query().Get("start"); q != "" {
		parsed, err := time.ParseInLocation(dateLayout, q, now.Location())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "start must be YYYY-MM-DD"})
			return
		}
		start = periodization.MondayOf(parsed)
	}
	dto, err := s.buildTrainingWeekDTO(ctx, rider, start, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// buildTrainingWeekDTO is handleTrainingWeek's own assembly, pulled out so
// handleReplan (see replan.go) can hand a rider the same "here's your
// week" shape it always gets after replanning, without a second place that
// knows how to build it — the same "one DTO builder" reasoning
// reconciledPeriodizationPlan's own doc comment gives for the plan itself.
// Takes a bare context.Context rather than *http.Request for the same
// reason reconciledPeriodizationPlan does: replan has no request of its own
// mid-lock to hold one.
func (s *Server) buildTrainingWeekDTO(ctx context.Context, rider string, start, now time.Time) (trainingWeekDTO, error) {
	today := now.Format(dateLayout)

	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return trainingWeekDTO{}, err
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return trainingWeekDTO{}, err
	}
	// One ListAnalyses call for the whole week, covering every date the days
	// loop below can show (start onward) — see analysesSince's own doc
	// comment on why a failure here degrades (no analysis attached) rather
	// than failing the whole week.
	analyses := s.analysesSince(ctx, rider, start.Format(dateLayout))
	// Degrades like analysesSince: without it the week only loses the
	// "linked by you" marker.
	links, err := s.Training.SessionLinks(ctx, rider)
	if err != nil {
		s.logger().Warn("reading ride links failed", "rider", rider, "err", err)
	}
	plannedBy := map[string][]workout.Workout{}
	for _, wk := range workouts {
		if wk.Date != "" {
			plannedBy[wk.Date] = append(plannedBy[wk.Date], wk)
		}
	}
	doneBy := map[string][]workout.CompletedSession{}
	for _, sess := range sessions {
		doneBy[sess.Date] = append(doneBy[sess.Date], sess)
	}

	dto := trainingWeekDTO{
		Start: start.Format(dateLayout),
		End:   start.AddDate(0, 0, 6).Format(dateLayout),
		Today: today,
		Days:  make([]weekDayDTO, 0, 7),
	}
	for i := 0; i < 7; i++ {
		date := start.AddDate(0, 0, i).Format(dateLayout)
		day := weekDayDTO{
			Date:      date,
			Status:    compliance.Day(date, today, plannedBy[date], doneBy[date]),
			Planned:   make([]workoutDTO, 0, len(plannedBy[date])),
			Completed: make([]completedSessionDTO, 0, len(doneBy[date])),
		}
		for _, wk := range plannedBy[date] {
			d := workoutDTOFrom(wk)
			day.Planned = append(day.Planned, d)
			dto.Totals.PlannedSeconds += d.PlannedSeconds
		}
		for _, sess := range doneBy[date] {
			c := completedSessionDTOFrom(sess, analyses)
			_, c.LinkedByHand = links[sess.ID]
			day.Completed = append(day.Completed, c)
			dto.Totals.CompletedSeconds += sess.DurationSeconds
		}
		dto.Days = append(dto.Days, day)
	}

	// One read for the whole week's reasons.
	planned := make([][]workoutDTO, 0, len(dto.Days))
	for _, d := range dto.Days {
		planned = append(planned, d.Planned)
	}
	s.attachWhy(ctx, rider, planned...)

	if dto.LifeEvents, err = s.lifeEventsInWeek(ctx, rider, start); err != nil {
		return trainingWeekDTO{}, err
	}

	focus, err := s.weekFocus(ctx, rider, start, now)
	if err != nil {
		return trainingWeekDTO{}, err
	}
	dto.Focus = focus
	return dto, nil
}

// sortGoalsForFocus orders goals most important first: priority, then dated
// before undated, then nearest event. The week header and the FTP test
// suggestion both take the first goal whose plan covers the week, so they
// agree on which goal the rider is training for.
func sortGoalsForFocus(goals []workout.Goal) {
	sort.SliceStable(goals, func(i, j int) bool {
		a, b := goals[i], goals[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority // "A" < "B" < "C"
		}
		if (a.EventDate == "") != (b.EventDate == "") {
			return a.EventDate != ""
		}
		return a.EventDate < b.EventDate
	})
}

// weekFocus picks the goal the header talks about: the most important one
// whose plan covers this week, nearest event first, dated before undated —
// a race on the calendar outranks "keep training". A goal that cannot be
// planned (event already past) is skipped rather than failing the page.
func (s *Server) weekFocus(ctx context.Context, rider string, start, now time.Time) (*weekFocusDTO, error) {
	goals, err := s.Training.ListGoals(ctx, rider)
	if err != nil {
		return nil, err
	}
	sortGoalsForFocus(goals)

	startDate := start.Format(dateLayout)
	for _, g := range goals {
		plan, _, err := s.reconciledPeriodizationPlan(ctx, g, rider)
		if err == periodization.ErrNoEventDate || err == periodization.ErrEventInThePast {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, wk := range plan.Weeks {
			if wk.StartDate != startDate {
				continue
			}
			f := &weekFocusDTO{
				GoalID: g.ID, Name: g.Name, Priority: string(g.Priority), Sport: string(g.Sport),
				EventDate: g.EventDate, WeekNumber: wk.Number, TotalWeeks: plan.TotalWeeks,
				Phase: string(wk.Phase), Recovery: wk.Recovery, TargetHours: wk.TargetHours,
			}
			if g.EventDate != "" {
				if ev, err := time.ParseInLocation(dateLayout, g.EventDate, now.Location()); err == nil {
					today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
					days := int(ev.Sub(today).Hours()/24 + 0.5)
					f.DaysToEvent = &days
				}
			}
			return f, nil
		}
	}
	return nil, nil
}
