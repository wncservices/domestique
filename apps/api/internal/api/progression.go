package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"

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
}

type progressionResponseDTO struct {
	Levels []progressionLevelDTO `json:"levels"`
}

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
	for _, l := range existing {
		out.Levels = append(out.Levels, progressionLevelDTO{
			Sport: string(l.Sport), Zone: string(l.Zone), Level: l.Level, Reason: l.Reason, UpdatedAt: l.UpdatedAt,
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

type feelRequestDTO struct {
	Feel int `json:"feel"`
}

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
			rawDelta := progression.Delta(curWithout, wk.Level, analysis.Outcome, body.Feel)
			newLevel := progression.Apply(curWithout, rawDelta)
			reason := progression.Reason(wk.Name, string(wk.Zone), wk.Level, curWithout, newLevel, analysis.Outcome)

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

	if err := s.Training.SetAnalysisFeel(r.Context(), id, body.Feel, newDelta); err != nil {
		s.fail(w, err)
		return
	}

	s.logger().Info("session feel recorded", "session", id, "rider", identity.User, "feel", body.Feel)

	analysis.Feel = body.Feel
	analysis.LevelDelta = newDelta
	writeJSON(w, http.StatusOK, sessionAnalysisDTOFrom(analysis))
}
