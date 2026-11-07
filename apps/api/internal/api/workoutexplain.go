package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

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

// explanationPrewriteTimeout bounds the whole pre-write phase, so a slow model
// cannot hold the morning pass open indefinitely.
const explanationPrewriteTimeout = 15 * time.Minute

type workoutExplanationDTO struct {
	Why     string                 `json:"why"`
	Summary string                 `json:"summary"`
	Blocks  []llm.ExplanationBlock `json:"blocks"`
	// Source is "model" for a stored model-written text and "template" for the
	// fixed fallback, which is never stored.
	Source string `json:"source"`
	// Pending is true while the model is writing this session's explanation in
	// the background: the template is shown now and the page asks again.
	Pending bool `json:"pending"`
}

// handleWorkoutExplanation explains one of the rider's own planned sessions.
// The explanation is keyed by the session's shape (no rider, name or watts), so
// it is shared between riders and written at most once. A miss answers the
// template at once and starts the one model call in the background, on the
// server's lifecycle rather than the request: the rider is behind Cloudflare's
// 100 s limit but the model may need longer than that to load. `pending` tells
// the page to ask again. The loser of a race (someone else is writing it) is
// pending too; a switched-off job, a refused limiter and a failed claim are
// not. The template is never stored.
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
	switch {
	case len(wk.Steps) == 0:
		// Nothing to explain: the shape would be sport and zone alone, and the
		// model would invent steps that are not there. No call, nothing cached.
	case s.LLM.Enabled(llm.JobExplainWorkout):
		exp, fromModel = s.cachedExplanation(r.Context(), key)
		if !fromModel {
			// The limiter is charged only once this request holds the claim, and
			// a refusal serves the template: a rider opening sessions is not
			// told to wait for something the page can show now.
			out.Pending = s.startExplanation(r.Context(), key, shape, func() bool {
				return s.NarrationLimiter == nil || s.NarrationLimiter.Allow(wk.Rider)
			})
		}
	case s.LLM != nil:
		s.logger().Debug("workout explanation: the explain_workout job is off, serving the template")
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
//
// admit, when set, is asked once the claim is won and before the model is
// called; false gives the claim back and answers false.
func (s *Server) generateExplanation(ctx context.Context, key, shape string, admit func() bool) (llm.WorkoutExplanation, bool) {
	claimed, err := s.Training.ClaimExplanation(ctx, key)
	if err != nil {
		s.logger().Warn("workout explanation: could not claim", "err", err)
		return llm.WorkoutExplanation{}, false
	}
	if !claimed {
		return llm.WorkoutExplanation{}, false
	}
	if admit != nil && !admit() {
		s.releaseExplanation(key)
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

// explanationBackgroundBudget bounds a background model call. The client keeps
// its own 120 s budget for both attempts; this is only the backstop.
const explanationBackgroundBudget = 3 * time.Minute

// startExplanation claims a missing shape and writes it from the model in the
// background. It answers whether an explanation is now on its way: true when
// this call started one or another request holds the claim, false when nothing
// will be written (the limiter refused, or the claim could not be taken). The
// claim is taken here, before returning, so a second request gets true at once
// without a second model call. A failed call gives the claim back, so the next
// view tries again.
func (s *Server) startExplanation(ctx context.Context, key, shape string, admit func() bool) bool {
	claimed, err := s.Training.ClaimExplanation(ctx, key)
	if err != nil {
		s.logger().Warn("workout explanation: could not claim", "err", err)
		return false
	}
	if !claimed {
		return true
	}
	if admit != nil && !admit() {
		s.releaseExplanation(key)
		return false
	}
	// The write outlives the request, so it runs on the lifecycle context, which
	// carries no span: the model call would start a trace of its own and
	// llm.job would be tagged on nothing. Keep the request's trace identity and
	// open a span for the write, so the call is a child in the same trace.
	parent := trace.SpanContextFromContext(ctx)
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		bg, cancel := context.WithTimeout(trace.ContextWithSpanContext(s.lifecycle(), parent), explanationBackgroundBudget)
		defer cancel()
		bg, span := otel.Tracer("domestique/explainer").Start(bg, "explain_workout.write")
		defer span.End()
		exp, err := s.LLM.ExplainWorkout(bg, shape)
		if err != nil {
			s.logger().Warn("workout explanation: the model call failed", "err", err)
			span.SetStatus(codes.Error, "model call failed")
			s.releaseExplanation(key)
			return
		}
		text, err := json.Marshal(exp)
		if err == nil {
			err = s.Training.SaveExplanation(bg, key, string(text))
		}
		if err != nil {
			s.logger().Warn("workout explanation: could not save", "err", err)
			span.SetStatus(codes.Error, "could not save")
			s.releaseExplanation(key)
		}
	}()
	return true
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
	ctx, cancel := context.WithTimeout(ctx, explanationPrewriteTimeout)
	defer cancel()
	last := dayOffset(today, explanationLookaheadDays-1)
	type pending struct{ date, key, shape string }
	var all []pending
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
			all = append(all, pending{wk.Date, workoutshape.Key(shape), shape})
		}
	}
	// Earliest date first across riders, so today's shapes are claimed before
	// the cap is spent on next week's; a shape shared by several keeps its
	// earliest date.
	sort.SliceStable(all, func(i, j int) bool { return all[i].date < all[j].date })
	var todo []pending
	seen := map[string]bool{}
	for _, p := range all {
		if !seen[p.key] {
			seen[p.key] = true
			todo = append(todo, p)
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
		if _, ok := s.generateExplanation(ctx, p.key, p.shape, nil); ok {
			written++
		}
	}
	s.logger().Info("workout explanations written", "shapes", len(todo), "attempted", attempted, "written", written)
}

// workoutWhyLine says where the session sits in the plan, from the plan the
// Plan page shows and without the model: "Build week 3 of 12 for Spring
// sportive", followed by " · recovery week" when this week is one, and " · recovery week next" or " · taper next" for the week after. A session
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
		if wk.Recovery {
			line += " · recovery week"
		}
		if i+1 < len(plan.Weeks) {
			next := plan.Weeks[i+1]
			switch {
			case next.Recovery:
				line += " · recovery week next"
			case (next.Phase == periodization.PhaseTaper && wk.Phase != periodization.PhaseTaper) || (next.MiniTaperDays > 0 && wk.MiniTaperDays == 0):
				line += " · taper next"
			}
		}
		return line
	}
	return ""
}
