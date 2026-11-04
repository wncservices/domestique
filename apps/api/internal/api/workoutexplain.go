package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/llm"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutshape"
)

// maxExplanationsPerPass bounds how many shapes one morning pass writes, so a
// season's worth of new shapes cannot turn one morning into a long run of
// model calls. The rest are written by the next pass or on first view.
const maxExplanationsPerPass = 10

// explanationLookaheadDays is how far ahead the morning pass writes: today and
// the six days after it.
const explanationLookaheadDays = 7

type workoutExplanationDTO struct {
	Why     string                 `json:"why"`
	Summary string                 `json:"summary"`
	Blocks  []llm.ExplanationBlock `json:"blocks"`
	// Source is "model" for a stored model-written text and "template" for the
	// fixed fallback, which is never stored.
	Source string `json:"source"`
}

// handleWorkoutExplanation explains one of the rider's own planned sessions.
// The explanation is keyed by the session's shape (no rider, name or watts), so
// it is shared between riders and written at most once. A miss makes one model
// call within llmRequestContext; the loser of a race, a switched-off job and a
// failed call all get the template now, and the template is never stored.
func (s *Server) handleWorkoutExplanation(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	wk, ok := s.ownWorkoutOrNotFound(w, r)
	if !ok {
		return
	}
	profile, _, err := s.Training.GetProfile(r.Context(), wk.Rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	shape := workoutshape.Text(wk, profile.FTPWatts)
	key := workoutshape.Key(shape)

	out := workoutExplanationDTO{Why: s.workoutWhyLine(r.Context(), wk)}
	exp, fromModel := llm.WorkoutExplanation{}, false
	if s.LLM.Enabled(llm.JobExplainWorkout) {
		exp, fromModel = s.cachedExplanation(r.Context(), key)
		if !fromModel {
			if !s.rateLimitNarration(w, wk.Rider) {
				return
			}
			ctx, cancel := llmRequestContext(r)
			exp, fromModel = s.generateExplanation(ctx, key, shape)
			cancel()
		}
	} else if s.LLM != nil {
		s.logger().Info("workout explanation: the explain_workout job is off, serving the template")
	}
	if fromModel {
		out.Source = "model"
	} else {
		exp = templateExplanation(wk, shape)
		out.Source = "template"
	}
	out.Summary, out.Blocks = exp.Summary, exp.Blocks
	if out.Blocks == nil {
		out.Blocks = []llm.ExplanationBlock{}
	}
	writeJSON(w, http.StatusOK, out)
}

// cachedExplanation reads a stored explanation. A row that does not decode is
// treated as absent: the template stands in, and nothing is rewritten over it.
func (s *Server) cachedExplanation(ctx context.Context, key string) (llm.WorkoutExplanation, bool) {
	text, found, err := s.Training.GetExplanation(ctx, key)
	if err != nil {
		s.logger().Warn("workout explanation: could not read the cache", "err", err)
		return llm.WorkoutExplanation{}, false
	}
	if !found {
		return llm.WorkoutExplanation{}, false
	}
	var exp llm.WorkoutExplanation
	if err := json.Unmarshal([]byte(text), &exp); err != nil || len(exp.Blocks) == 0 {
		s.logger().Warn("workout explanation: a stored explanation is unreadable")
		return llm.WorkoutExplanation{}, false
	}
	return exp, true
}

// generateExplanation makes the one model call for a missing shape and stores
// the answer. It claims the key first, so a second request or replica for the
// same shape gets false at once instead of calling the model again. Every
// failure gives the claim back, so the next request or pass tries again: unlike
// a day's note there is no "already asked today" to keep.
func (s *Server) generateExplanation(ctx context.Context, key, shape string) (llm.WorkoutExplanation, bool) {
	claimed, err := s.Training.ClaimExplanation(ctx, key)
	if err != nil {
		s.logger().Warn("workout explanation: could not claim", "err", err)
		return llm.WorkoutExplanation{}, false
	}
	if !claimed {
		return llm.WorkoutExplanation{}, false
	}
	exp, err := s.LLM.ExplainWorkout(ctx, shape)
	if err != nil {
		s.logger().Warn("workout explanation: the model call failed", "err", err)
		s.releaseExplanation(key)
		return llm.WorkoutExplanation{}, false
	}
	text, err := json.Marshal(exp)
	if err == nil {
		err = s.Training.SaveExplanation(ctx, key, string(text))
	}
	if err != nil {
		s.logger().Warn("workout explanation: could not save", "err", err)
		s.releaseExplanation(key)
		return llm.WorkoutExplanation{}, false
	}
	return exp, true
}

// releaseExplanation gives back an empty claim on its own short context: the
// one that failed may be the cancelled one.
func (s *Server) releaseExplanation(key string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.lifecycle()), 5*time.Second)
	defer cancel()
	if err := s.Training.ReleaseExplanation(ctx, key); err != nil {
		s.logger().Warn("workout explanation: could not release a claim", "err", err)
	}
}

// prewriteExplanations writes the model's explanation for the shapes riders
// with a goal are about to ride, so the first view is a cache hit. It runs
// after the morning notes, writes at most maxExplanationsPerPass shapes (counted
// as claims made, so a failing model is not hammered either), and does nothing
// without the job. Logs carry counts only.
func (s *Server) prewriteExplanations(ctx context.Context, riders []string, today string) {
	if !s.LLM.Enabled(llm.JobExplainWorkout) {
		return
	}
	last := dayOffset(today, explanationLookaheadDays-1)
	type pending struct{ key, shape string }
	var todo []pending
	seen := map[string]bool{}
	for _, rider := range riders {
		if ctx.Err() != nil {
			return
		}
		profile, _, err := s.Training.GetProfile(ctx, rider)
		if err != nil {
			s.logger().Warn("workout explanation: could not read a profile", "err", err)
			continue
		}
		ws, err := s.Training.ListWorkouts(ctx, rider)
		if err != nil {
			s.logger().Warn("workout explanation: could not list workouts", "err", err)
			continue
		}
		for _, wk := range ws {
			if wk.Date < today || wk.Date > last || len(wk.Steps) == 0 {
				continue
			}
			shape := workoutshape.Text(wk, profile.FTPWatts)
			key := workoutshape.Key(shape)
			if !seen[key] {
				seen[key] = true
				todo = append(todo, pending{key, shape})
			}
		}
	}

	attempted, written := 0, 0
	for _, p := range todo {
		if attempted >= maxExplanationsPerPass || ctx.Err() != nil {
			break
		}
		if _, cached := s.cachedExplanation(ctx, p.key); cached {
			continue
		}
		attempted++
		if _, ok := s.generateExplanation(ctx, p.key, p.shape); ok {
			written++
		}
	}
	s.logger().Info("workout explanations written", "shapes", len(todo), "attempted", attempted, "written", written)
}

// workoutWhyLine says where the session sits in the plan, from the plan the
// Plan page shows and without the model: "Build week 3 of 12 for Spring
// sportive", followed by " · recovery week next" or " · taper next". A session
// with no goal, or one the plan does not cover (a week already behind us, a
// goal that cannot be planned), has an empty line.
func (s *Server) workoutWhyLine(ctx context.Context, wk workout.Workout) string {
	if wk.GoalID == "" || wk.Date == "" {
		return ""
	}
	g, err := s.Training.GetGoal(ctx, wk.GoalID)
	if err != nil || g.Rider != wk.Rider {
		return ""
	}
	plan, _, err := s.reconciledPeriodizationPlan(ctx, g, wk.Rider)
	if err != nil {
		return ""
	}
	return whyLine(plan, g.Name, wk.Date)
}

// whyLine is workoutWhyLine's pure part: the plan week containing date.
func whyLine(plan periodization.Plan, goalName, date string) string {
	for i, wk := range plan.Weeks {
		if date < wk.StartDate || date > dayOffset(wk.StartDate, 6) {
			continue
		}
		phase := string(wk.Phase)
		if phase == "" {
			return ""
		}
		line := strings.ToUpper(phase[:1]) + phase[1:] + " week " + strconv.Itoa(wk.Number) + " of " + strconv.Itoa(plan.TotalWeeks)
		if goalName != "" {
			line += " for " + goalName
		}
		if i+1 < len(plan.Weeks) {
			next := plan.Weeks[i+1]
			switch {
			case next.Recovery:
				line += " · recovery week next"
			case next.Phase == periodization.PhaseTaper && wk.Phase != periodization.PhaseTaper:
				line += " · taper next"
			}
		}
		return line
	}
	return ""
}
