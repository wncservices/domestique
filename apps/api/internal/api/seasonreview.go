package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/seasonreview"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// ---------- DTOs ----------
//
// The review itself is seasonreview.Review, whose JSON tags are the API's.
// Mirrored by hand in apps/web/src/api/types.ts — change them together.

// seasonPeriodDTO is a period the picker offers.
type seasonPeriodDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

type seasonAvailableDTO struct {
	// Years are the calendar years with a completed session, newest first, and
	// always the current one, the page's default.
	Years []string `json:"years"`
	// Goals are the goals that have a season: an event date no earlier than the
	// day the goal was created.
	Goals []seasonPeriodDTO `json:"goals"`
}

type seasonResponseDTO struct {
	seasonreview.Review
	Available seasonAvailableDTO `json:"available"`
}

const (
	firstSeasonYear = 1990
	lastSeasonYear  = 2200
)

// handleSeasonReview is GET /api/training/season?year=2026 or ?goal=<id>, and
// the current year with neither. Owner-only: the rider is the session's, and a
// goal that is another rider's or does not exist is a 404 either way, so the
// answer does not confirm it exists (an admin gets the same: these are the
// rider's own pages). Computed on read from the stored rows; nothing about a
// season is stored.
func (s *Server) handleSeasonReview(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	identity := auth.FromContext(r.Context())
	rider := identity.User
	ctx := r.Context()

	todayTime, ok := s.today(w)
	if !ok {
		return
	}
	today := todayTime.Format(dateLayout)

	q := r.URL.Query()
	var period seasonreview.Period
	switch {
	case q.Has("year") && q.Has("goal"):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "give a year or a goal, not both"})
		return
	case q.Has("goal"):
		id := q.Get("goal")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "goal needs an id"})
			return
		}
		goal, err := s.Training.GetGoal(ctx, id)
		if err != nil {
			s.failTrainingLookup(w, err)
			return
		}
		if !isOwnTraining(identity, goal.Rider) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such goal"})
			return
		}
		p, ok := seasonreview.GoalPeriod(goal, s.trainingLocation())
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this goal has no season: it needs an event date after the day it was created"})
			return
		}
		period = p
	default:
		year := strconv.Itoa(todayTime.Year())
		if q.Has("year") {
			year = q.Get("year")
		}
		n, err := strconv.Atoi(year)
		if err != nil || len(year) != 4 || n < firstSeasonYear || n > lastSeasonYear {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "year must be a four-digit year"})
			return
		}
		period = seasonreview.Period{Kind: seasonreview.KindYear, Label: year, From: year + "-01-01", To: year + "-12-31"}
	}

	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	snapshots, err := s.Training.ListFitnessSnapshots(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	changes, err := s.Training.ListFTPChanges(ctx, rider, "", "")
	if err != nil {
		s.fail(w, err)
		return
	}
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	awards, err := s.Training.ListAwards(ctx, rider, period.From, period.To)
	if err != nil {
		s.fail(w, err)
		return
	}
	goals, err := s.Training.ListGoals(ctx, rider)
	if err != nil {
		s.fail(w, err)
		return
	}

	review := seasonreview.Compute(seasonreview.Input{
		Period: period, Today: today, Sessions: sessions, Snapshots: snapshots,
		FTPChanges: changes, ProfileFTP: profile.FTPWatts, Workouts: workouts, Efforts: awards, Goals: goals,
	})
	writeJSON(w, http.StatusOK, seasonResponseDTO{Review: review, Available: seasonAvailable(sessions, goals, todayTime.Year(), s.trainingLocation())})
}

func seasonAvailable(sessions []workout.CompletedSession, goals []workout.Goal, currentYear int, loc *time.Location) seasonAvailableDTO {
	years := map[string]bool{strconv.Itoa(currentYear): true}
	for _, sess := range sessions {
		if len(sess.Date) >= 4 {
			years[sess.Date[:4]] = true
		}
	}
	out := seasonAvailableDTO{Years: make([]string, 0, len(years)), Goals: []seasonPeriodDTO{}}
	for y := range years {
		out.Years = append(out.Years, y)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out.Years)))
	for _, g := range goals {
		if p, ok := seasonreview.GoalPeriod(g, loc); ok {
			out.Goals = append(out.Goals, seasonPeriodDTO{ID: g.ID, Name: g.Name, From: p.From, To: p.To})
		}
	}
	sort.SliceStable(out.Goals, func(i, j int) bool { return out.Goals[i].To > out.Goals[j].To })
	return out
}

// trainingLocation is the rider's calendar: the training timezone, or nil (UTC
// reading) when the schedule cannot be read.
func (s *Server) trainingLocation() *time.Location {
	if sched, err := s.syncSchedule(); err == nil {
		return sched.Location()
	}
	return nil
}
