package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/alternates"
	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/progression"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/trainnow"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// "I have N minutes today". GET suggests and writes nothing; POST apply is the
// rider choosing one. The suggestions are recomputed on apply and matched on
// kind, zone and level, so a sheet that has been open while the day moved on
// gets a 409 and refetches instead of applying something stale.

const (
	trainNowStaleMessage   = "That suggestion is not available any more. Reload the suggestions and pick again."
	trainNowMinutesMessage = "minutes must be a whole number from 30 to 180"
	trainNowLookbackDays   = 7
)

type trainNowSuggestionDTO struct {
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	Zone       string  `json:"zone"`
	Level      float64 `json:"level,omitempty"`
	Minutes    int     `json:"minutes"`
	TSS        float64 `json:"tss"`
	Difficulty string  `json:"difficulty"`
	Why        string  `json:"why"`
	Warning    string  `json:"warning,omitempty"`
}

type trainNowDTO struct {
	Minutes     int                     `json:"minutes"`
	Verdict     string                  `json:"verdict"`
	Suggestions []trainNowSuggestionDTO `json:"suggestions"`
	// Notice is why there are no suggestions: a life event (proper illness, a
	// trip without a bike) means today is not a day to ride.
	Notice string `json:"notice,omitempty"`
}

// trainNowResult is what suggesting read, kept for apply so it does not read it
// twice.
type trainNowResult struct {
	verdict     readiness.Verdict
	suggestions []trainnow.Suggestion
	profile     workout.RiderProfile
	goalID      string
	workouts    []workout.Workout
	ridden      map[string]bool
	notice      string
}

func parseTrainNowMinutes(raw string) (int, bool) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < trainnow.MinMinutes || n > trainnow.MaxMinutes {
		return 0, false
	}
	return n, true
}

func (s *Server) handleTrainNow(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	minutes, ok := parseTrainNowMinutes(r.URL.Query().Get("minutes"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": trainNowMinutesMessage})
		return
	}
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return
	}
	rider := auth.FromContext(r.Context()).User
	res, err := s.trainNowFor(r.Context(), rider, minutes, today)
	if err != nil {
		s.fail(w, err)
		return
	}
	dto := trainNowDTO{Minutes: minutes, Verdict: string(res.verdict), Suggestions: make([]trainNowSuggestionDTO, 0, len(res.suggestions)), Notice: res.notice}
	for _, sg := range res.suggestions {
		dto.Suggestions = append(dto.Suggestions, trainNowSuggestionDTO{
			Kind: string(sg.Kind), Name: sg.Name, Zone: string(sg.Zone), Level: sg.Level,
			Minutes: int(math.Round(sg.Seconds / 60)), TSS: sg.TSS, Difficulty: sg.Difficulty,
			Why: sg.Why, Warning: sg.Warning,
		})
	}
	writeJSON(w, http.StatusOK, dto)
}

// trainNowFor reads what the suggestions depend on and runs them. It writes
// nothing: in particular it does not seed a missing level, which levelsFor would.
func (s *Server) trainNowFor(ctx context.Context, rider string, minutes int, today time.Time) (trainNowResult, error) {
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return trainNowResult{}, err
	}
	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return trainNowResult{}, err
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return trainNowResult{}, err
	}
	latest, err := s.latestFitness(ctx, rider)
	if err != nil {
		return trainNowResult{}, err
	}
	// No wellness data reads as ready: only the load rule can then shape it.
	assessment := s.assessReadinessAt(ctx, rider, sessions, latest, today)

	plan, goal, _, err := s.focusPlan(ctx, rider, today)
	if err != nil {
		return trainNowResult{}, err
	}
	sport := model.SportCycling
	goalID := ""
	if goal != nil {
		goalID = goal.ID
		if goal.Sport != "" {
			sport = goal.Sport
		}
	}
	levels, err := s.levelMapReadOnly(ctx, rider, profile, sport)
	if err != nil {
		return trainNowResult{}, err
	}

	todayStr := today.Format(dateLayout)
	weekStart := periodization.MondayOf(today)
	weekEnd := weekStart.AddDate(0, 0, 6).Format(dateLayout)
	ridden, err := s.riddenToday(ctx, rider, todayStr, workouts)
	if err != nil {
		return trainNowResult{}, err
	}

	in := trainnow.Input{
		Minutes: minutes, Profile: profile, Sport: sport, HasPlan: goal != nil,
		Verdict: assessment.Verdict, ReadinessReasons: assessment.Reasons,
		Level: func(zone string) float64 {
			if l, ok := levels[zone]; ok {
				return l
			}
			return 3
		},
	}

	// Today's plan-made undone session, else the next one this week. workouts
	// is ordered by date, so the first match is the earliest.
	for i := range workouts {
		wk := workouts[i]
		if !alternates.Plannable(wk) || wk.Date < todayStr || wk.Date > weekEnd || (wk.Date == todayStr && ridden[wk.ID]) {
			continue
		}
		in.Today, in.PlannedIsToday = &wk, wk.Date == todayStr
		break
	}

	if plan != nil && goal != nil {
		in.WeekZones = weekZones(plan, weekStart, profile, levels, rider, goal, workouts)
	}
	in.DoneZones, in.HardDaysLast7 = doneAndHard(workouts, sessions, todayStr, weekStart.Format(dateLayout), today.AddDate(0, 0, -trainNowLookbackDays).Format(dateLayout))

	out := trainNowResult{
		verdict: assessment.Verdict, suggestions: trainnow.Suggest(in), profile: profile,
		goalID: goalID, workouts: workouts, ridden: ridden,
	}
	// A day a life event rules riding out of: say so, and offer nothing.
	events, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		return trainNowResult{}, err
	}
	if reason := lifeevents.NoRideReason(events, todayStr); reason != "" {
		s.logger().Info("trainnow: nothing suggested, the day is inside a life event", "rider", rider)
		out.notice, out.suggestions = reason, nil
	}
	return out, nil
}

// levelMapReadOnly is the rider's level per zone for sport: what is saved, else
// the starting level their experience gives. Never writes.
func (s *Server) levelMapReadOnly(ctx context.Context, rider string, profile workout.RiderProfile, sport model.Sport) (map[string]float64, error) {
	saved, err := s.Training.ListLevels(ctx, rider)
	if err != nil {
		return nil, err
	}
	levels := map[string]float64{}
	for _, l := range progression.Initial(profile.ExperienceLevel, sport) {
		levels[l.Zone] = l.Value
	}
	for _, l := range saved {
		if l.Sport == sport {
			levels[string(l.Zone)] = l.Level
		}
	}
	return levels, nil
}

// weekZones is the structured zones the plan puts in the week starting on
// weekStart, in plan (date) order: what scheduler.WeekWorkouts would build.
func weekZones(plan *periodization.Plan, weekStart time.Time, profile workout.RiderProfile, levels map[string]float64, rider string, goal *workout.Goal, workouts []workout.Workout) []string {
	start := weekStart.Format(dateLayout)
	for _, week := range plan.Weeks {
		if week.StartDate != start {
			continue
		}
		reqs, err := scheduler.WeekWorkouts(week, profile, levels, rider, goal.ID, goal.Sport, fixedOption(workouts, week))
		if err != nil {
			return nil
		}
		sort.SliceStable(reqs, func(i, j int) bool { return reqs[i].Date < reqs[j].Date })
		var zones []string
		seen := map[string]bool{}
		for _, req := range reqs {
			z := string(req.Zone)
			if workout.IsStructuredZone(req.Zone) && !seen[z] {
				seen[z] = true
				zones = append(zones, z)
			}
		}
		return zones
	}
	return nil
}

// doneAndHard reads the completed structured sessions: which zones are done
// this week (from weekStart through today), and how many distinct days in the
// week before today (from since) had one. A workout counts as done by the same
// rule the rest of the app uses (adapter.WorkoutDone).
func doneAndHard(workouts []workout.Workout, sessions []workout.CompletedSession, today, weekStart, since string) (map[string]bool, int) {
	doneZones := map[string]bool{}
	hardDays := map[string]bool{}
	for _, wk := range workouts {
		if !workout.IsStructuredZone(wk.Zone) || wk.Date == "" || wk.Date > today || !adapter.WorkoutDone(wk, sessions) {
			continue
		}
		if wk.Date >= weekStart {
			doneZones[string(wk.Zone)] = true
		}
		if wk.Date < today && wk.Date >= since {
			hardDays[wk.Date] = true
		}
	}
	return doneZones, len(hardDays)
}

func (s *Server) handleTrainNowApply(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	var body struct {
		Minutes int     `json:"minutes"`
		Kind    string  `json:"kind"`
		Zone    string  `json:"zone"`
		Level   float64 `json:"level"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAlternatesBodyBytes)).Decode(&body); err != nil ||
		body.Minutes < trainnow.MinMinutes || body.Minutes > trainnow.MaxMinutes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": trainNowMinutesMessage})
		return
	}
	today, ok := parseTodayParam(w, r, s.now())
	if !ok {
		return
	}
	rider := auth.FromContext(r.Context()).User

	res, err := s.trainNowFor(r.Context(), rider, body.Minutes, today)
	if err != nil {
		s.fail(w, err)
		return
	}
	var chosen *trainnow.Suggestion
	for i := range res.suggestions {
		sg := res.suggestions[i]
		if string(sg.Kind) == body.Kind && string(sg.Zone) == body.Zone && sg.Level == body.Level {
			chosen = &sg
			break
		}
	}
	if chosen == nil {
		s.logger().Info("trainnow apply refused", "rider", rider, "kind", body.Kind)
		writeJSON(w, http.StatusConflict, map[string]string{"error": trainNowStaleMessage})
		return
	}

	todayStr := today.Format(dateLayout)
	target, replace := untouchedToday(res, todayStr)
	var result workout.Workout
	if replace {
		result, err = s.replaceWithSuggestion(r.Context(), target, *chosen, body.Minutes, res.profile)
	} else {
		result, err = s.Training.CreateWorkout(r.Context(), workout.CreateWorkoutRequest{
			Rider: rider, Sport: chosen.Sport, Name: chosen.Name, GoalID: res.goalID, Date: todayStr,
			Description: fmt.Sprintf("Chosen by you for a day with %d minutes.", body.Minutes),
			Steps:       chosen.Steps, Zone: chosen.Zone, Level: chosen.Level,
		})
	}
	if err != nil {
		s.logger().Error("could not apply a trainnow suggestion", "rider", rider, "kind", body.Kind, "err", err)
		s.fail(w, err)
		return
	}
	outcome := "added"
	if replace {
		outcome = "replaced"
	}
	s.logger().Info("trainnow applied", "rider", rider, "workout", result.ID, "kind", body.Kind, "outcome", outcome)

	// Today's session is pushed the way the rest of the app pushes it: an
	// existing copy is updated, and with auto-push on a new one goes out too.
	if replace {
		s.repushToday(r.Context(), result)
	}
	if res.profile.AutoPushWorkouts {
		s.pushWorkoutsForRider(r.Context(), rider)
	}
	writeJSON(w, http.StatusOK, s.workoutDTOWithWhy(r.Context(), result))
}

// untouchedToday is today's plan-made session that a suggestion may replace in
// place: generated and not adjusted or swapped (scheduler.IsGenerated), not an
// FTP test, and not ridden. Anything else on today is only ever added to.
func untouchedToday(res trainNowResult, today string) (workout.Workout, bool) {
	for _, wk := range res.workouts {
		if wk.Date == today && scheduler.IsGenerated(wk) && wk.TestProtocol == "" && !res.ridden[wk.ID] {
			return wk, true
		}
	}
	return workout.Workout{}, false
}

// replaceWithSuggestion edits wk in place into the suggestion, exactly like a
// swap: same id (so a copy on a head unit updates), the plan's version kept in
// planned_snapshot for "Back to planned version", the swap marker appended, an
// indoor session kept indoor.
func (s *Server) replaceWithSuggestion(ctx context.Context, wk workout.Workout, sg trainnow.Suggestion, minutes int, profile workout.RiderProfile) (workout.Workout, error) {
	name, zone, level, steps := sg.Name, sg.Zone, sg.Level, sg.Steps
	description := addNote(wk.Description, fmt.Sprintf("%s for a %d-minute day, was %s (%s).",
		scheduler.SwappedMarker, minutes, wk.Name, swapDuration(workout.PlannedSeconds(wk.Steps))))
	req := workout.UpdateWorkoutRequest{
		Name: &name, Zone: &zone, Level: &level, Steps: &steps, Description: &description,
		PlannedSnapshot: plannedSnapshotOf(wk),
	}
	// A suggestion is built for its own sport; the snapshot keeps the session's,
	// so "Back to planned version" restores it.
	if sg.Sport != "" && sg.Sport != wk.Sport {
		// Only cycling has an indoor version, so a run leaves it behind.
		req.Sport = &sg.Sport
		no := false
		var none []workout.WorkoutStep
		req.Indoor, req.OutdoorSteps = &no, &none
	} else {
		s.keepIndoor(&req, wk, profile, sg.Zone)
	}
	return s.Training.UpdateWorkout(ctx, wk.ID, req)
}
