package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/progression"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// roundLevelDelta rounds a level change to the same one decimal every
// stored level uses, and guards against IEEE -0: math.Round can hand one
// back for a tiny negative input that rounds to zero (e.g. -0.02*10 rounds
// to -0), and while -0 compares equal to 0, its sign bit would still show
// up as "-0" in anything that formats it directly (JSON, a log line, a
// %v). Shared by applyProgressionForAnalysis (rideanalysis.go) and
// handleSetSessionFeel below — the two places that turn "the level Apply
// actually produced minus where it started" into the value SetAnalysisFeel
// stores.
func roundLevelDelta(v float64) float64 {
	r := math.Round(v*10) / 10
	if r == 0 {
		r = 0
	}
	return r
}

// progressionLevelDTO mirrors workout.ProgressionLevel — the Progression
// card's own source. Mirrored by hand in apps/web/src/api/types.ts, the same
// rule every DTO in training.go already follows.
type progressionLevelDTO struct {
	Sport     string  `json:"sport"`
	Zone      string  `json:"zone"`
	Level     float64 `json:"level"`
	Reason    string  `json:"reason,omitempty"`
	UpdatedAt string  `json:"updatedAt,omitempty"`
	// Why is the structured reason for the level's latest automatic move (a
	// recalibration after an FTP rise). A level whose last move was a ride
	// has only Reason.
	Why *whyDTO `json:"why,omitempty"`
}

// progressionPointDTO is one value a level held from At on — see
// workout.LevelPoint.
type progressionPointDTO struct {
	Sport  string  `json:"sport"`
	Zone   string  `json:"zone"`
	Level  float64 `json:"level"`
	Reason string  `json:"reason,omitempty"`
	At     string  `json:"at"`
}

type progressionResponseDTO struct {
	Levels []progressionLevelDTO `json:"levels"`
	// History is the last year of level moves, oldest first, led for each
	// zone by the value it held before the year began.
	History []progressionPointDTO `json:"history"`
}

// progressionHistoryDays is how far back the Progression chart can reach.
const progressionHistoryDays = 365

// handleGetProgression returns a rider's own progression levels, owner-only
// like every other training endpoint. A rider with nothing saved yet — never
// had a plan scheduled, so levelsFor's own seeding in scheduleGoal has never
// run — gets levels initialised here instead, for every sport they have a
// goal for (seedLevelsForRidersGoals), so the Progression card has something
// to show before the first "Schedule this week" click rather than an empty
// list.
func (s *Server) handleGetProgression(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User

	existing, err := s.Training.ListLevels(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(existing) == 0 {
		if err := s.seedLevelsForRidersGoals(r.Context(), rider); err != nil {
			s.fail(w, err)
			return
		}
		existing, err = s.Training.ListLevels(r.Context(), rider)
		if err != nil {
			s.fail(w, err)
			return
		}
	}

	out := progressionResponseDTO{Levels: make([]progressionLevelDTO, 0, len(existing))}
	ids := make([]string, 0, len(existing))
	for _, l := range existing {
		ids = append(ids, string(l.Sport)+":"+string(l.Zone))
	}
	// One read for every level's reason; a failure leaves them with only
	// their own Reason, as before.
	whys, err := s.Training.LatestAdjustments(r.Context(), rider, workout.SubjectLevel, ids)
	if err != nil {
		s.logger().Warn("could not read why for levels", "rider", rider, "err", err)
	}
	for _, l := range existing {
		dto := progressionLevelDTO{
			Sport: string(l.Sport), Zone: string(l.Zone), Level: l.Level, Reason: l.Reason, UpdatedAt: l.UpdatedAt,
		}
		// Only while the level still rests on that move: a ride that moved it
		// since has its own Reason, and the older why would contradict it.
		if a, ok := whys[string(l.Sport)+":"+string(l.Zone)]; ok && whyIsCurrent(a.CreatedAt, l.UpdatedAt) {
			dto.Why = whyDTOFrom(a)
		}
		out.Levels = append(out.Levels, dto)
	}
	history, err := s.Training.LevelHistory(r.Context(), rider, s.now().AddDate(0, 0, -progressionHistoryDays).UTC().Format(time.RFC3339))
	if err != nil {
		s.fail(w, err)
		return
	}
	out.History = make([]progressionPointDTO, 0, len(history))
	for _, p := range history {
		out.History = append(out.History, progressionPointDTO{
			Sport: string(p.Sport), Zone: string(p.Zone), Level: p.Level, Reason: p.Reason, At: p.At,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// seedLevelsForRidersGoals initialises progression levels — via levelsFor,
// the exact same seeding scheduleGoal already relies on, so a rider never
// gets two different starting levels depending on which path asked first —
// for every distinct sport the rider has a goal for.
func (s *Server) seedLevelsForRidersGoals(ctx context.Context, rider string) error {
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return err
	}
	goals, err := s.Training.ListGoals(ctx, rider)
	if err != nil {
		return err
	}
	seen := map[model.Sport]bool{}
	for _, g := range goals {
		if seen[g.Sport] {
			continue
		}
		seen[g.Sport] = true
		if _, err := s.levelsFor(ctx, rider, profile, g.Sport); err != nil {
			return err
		}
	}
	return nil
}

// feelRequestDTO is the whole post-ride survey. It is a full replace: an
// omitted or empty Legs/Stress clears what was there, so the rider's last tap
// is the whole truth. Feel stays required, the survey starts with it.
type feelRequestDTO struct {
	Feel   int    `json:"feel"`
	Legs   string `json:"legs"`
	Stress string `json:"stress"`
}

// validSurveyLegs and validSurveyStress are the only answers the survey
// takes; "" (unanswered) is allowed for both.
var (
	validSurveyLegs   = map[string]bool{"": true, "fresh": true, "normal": true, "heavy": true}
	validSurveyStress = map[string]bool{"": true, "low": true, "normal": true, "high": true}
)

// handleSetSessionFeel records a rider's own "how did it feel" 1-5 rating
// for one analysed session, and re-applies the level change that ride
// produced with the new feel factored in — a re-rate replaces the change
// rather than stacking a second one on top (docs/superpowers/specs's
// progression-levels design, "Feel rating").
//
// Ownership is checked against the analysis' own rider, not a session
// lookup of its own — session_analyses.rider is exactly who this ride
// belongs to, the same owner-scoped 404 (never a 403 that would confirm the
// id exists at all) every other rider-owned resource in this file uses.
func (s *Server) handleSetSessionFeel(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	var body feelRequestDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Feel < 1 || body.Feel > 5 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "feel must be between 1 and 5"})
		return
	}
	if !validSurveyLegs[body.Legs] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "legs must be fresh, normal or heavy"})
		return
	}
	if !validSurveyStress[body.Stress] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stress must be low, normal or high"})
		return
	}

	id := r.PathValue("id")
	identity := auth.FromContext(r.Context())

	analysis, ok, err := s.Training.GetAnalysis(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok || !isOwnTraining(identity, analysis.Rider) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
		return
	}

	// The outcome progression reacts to is the one this rating would give the
	// ride, not the one stored under the previous rating: all-out on a nailed
	// ride is a struggle, and a re-rate to anything else takes that back.
	rated := analysis
	rated.Feel = body.Feel
	effective := rated.EffectiveOutcome()

	newDelta := 0.0
	if analysis.WorkoutID != "" {
		wk, err := s.Training.GetWorkout(r.Context(), analysis.WorkoutID)
		if err != nil && !errors.Is(err, workout.ErrWorkoutNotFound) {
			s.fail(w, err)
			return
		}
		if err == nil && workout.IsStructuredZone(wk.Zone) && wk.Level > 0 {
			profile, _, err := s.Training.GetProfile(r.Context(), analysis.Rider)
			if err != nil {
				s.fail(w, err)
				return
			}
			levels, err := s.levelsFor(r.Context(), analysis.Rider, profile, wk.Sport)
			if err != nil {
				s.fail(w, err)
				return
			}

			// Undo this ride's own previous change before applying the new
			// one — a re-rate replaces the delta, it never stacks a second
			// adjustment on top of the first.
			curWithout := levels[string(wk.Zone)] - analysis.LevelDelta
			rawDelta := progression.Delta(curWithout, wk.Level, progression.Outcome(effective), body.Feel)
			newLevel := progression.Apply(curWithout, rawDelta)
			reason := progression.Reason(wk.Name, string(wk.Zone), wk.Level, curWithout, newLevel, progression.Outcome(effective))

			// Store what Apply actually did (newLevel - curWithout, rounded),
			// not Delta's raw, unclamped result — see
			// applyProgressionForAnalysis's own comment on why the two can
			// differ at the 1.0/10.0 clamp boundary, and why storing the raw
			// value would drift a later re-rate's own curWithout.
			newDelta = roundLevelDelta(newLevel - curWithout)

			if err := s.Training.SaveLevel(r.Context(), workout.ProgressionLevel{
				Rider: analysis.Rider, Sport: wk.Sport, Zone: wk.Zone, Level: newLevel, Reason: reason,
			}); err != nil {
				s.fail(w, err)
				return
			}
		}
		// A workout with no structured zone or level (or since deleted) has
		// nothing to move — newDelta stays 0 and only the feel rating itself
		// is recorded below.
	}

	if err := s.Training.SetAnalysisSurvey(r.Context(), id, body.Feel, body.Legs, body.Stress, newDelta); err != nil {
		s.fail(w, err)
		return
	}

	s.logger().Info("session feel recorded", "session", id, "rider", identity.User)

	analysis.Feel, analysis.Legs, analysis.Stress = body.Feel, body.Legs, body.Stress
	analysis.LevelDelta = newDelta
	writeJSON(w, http.StatusOK, sessionAnalysisDTOFrom(analysis))
}
