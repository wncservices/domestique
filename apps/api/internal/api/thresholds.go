package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/autoprofile"
	"github.com/wncservices/domestique/apps/api/internal/thresholds"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// ---------- DTOs ----------
//
// Mirrored by hand in apps/web/src/api/types.ts — change them together.

type thresholdSuggestionDTO struct {
	ID         string  `json:"id"`
	Field      string  `json:"field"`
	Value      float64 `json:"value"`
	Previous   float64 `json:"previous,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	SourceDate string  `json:"sourceDate,omitempty"`
}

func thresholdSuggestionDTOFrom(s workout.ThresholdSuggestion) thresholdSuggestionDTO {
	return thresholdSuggestionDTO{
		ID: s.ID, Field: s.Field, Value: s.Value, Previous: s.Previous,
		Reason: s.Reason, SourceDate: s.SourceDate,
	}
}

// detectedThresholdDTO is one Auto finding a sync just applied straight to
// the profile — syncMetricsResultDTO's own "detected" field, what the UI
// toasts ("FTP updated to 268 W from Saturday's 20-minute effort").
type detectedThresholdDTO struct {
	Field  string  `json:"field"`
	Value  float64 `json:"value"`
	Reason string  `json:"reason,omitempty"`
}

// ---------- Sync-time detection ----------

// thresholdDetectionResult is detectThresholds' own return: the profile
// with every Auto finding already applied, the DTOs for a sync result's
// "detected" field, the profile field names that changed (folded into the
// caller's own autoFilled list, the same one Garmin biometrics and
// fitnesstest.EstimateFTP already contribute to), and whether an eligible
// FTP power-curve ride exists in the window — see EstimateFTP's own call
// site in syncRiderMetrics for why that last one matters.
type thresholdDetectionResult struct {
	Profile          workout.RiderProfile
	Detected         []detectedThresholdDTO
	AutoFields       []string
	HasFTPPowerCurve bool
}

// detectThresholds runs internal/thresholds.Detect against a rider's last
// 90 days of analysed rides (the down-direction rule's own lookback),
// applies every Auto finding straight to the profile through
// autoprofile.Apply (still marked estimated, exactly like any other
// auto-filled field), and stores a pending suggestion for everything else —
// see docs/superpowers/specs/2026-09-28-threshold-detection-design.md's
// "When a value changes". sessions is the rider's already-loaded
// completed_sessions list (syncRiderMetrics' own `sessions` variable) —
// joined here to each analysis for the Sport/Date thresholds.Ride needs,
// since session_analyses itself carries neither.
func (s *Server) detectThresholds(ctx context.Context, rider string, profile workout.RiderProfile, sessions []workout.CompletedSession, now time.Time) (thresholdDetectionResult, error) {
	sinceDate := now.AddDate(0, 0, -thresholds.HistoryWindowDays).Format("2006-01-02")
	analyses, err := s.Training.ListAnalyses(ctx, rider, sinceDate)
	if err != nil {
		return thresholdDetectionResult{}, err
	}

	sessionByID := make(map[string]workout.CompletedSession, len(sessions))
	for _, sess := range sessions {
		sessionByID[sess.ID] = sess
	}

	ftpCutoff := now.AddDate(0, 0, -thresholds.DetectionWindowDays).Format("2006-01-02")
	var rides []thresholds.Ride
	hasFTPPowerCurve := false
	for _, a := range analyses {
		sess, ok := sessionByID[a.SessionID]
		if !ok {
			// A session this process never loaded this run — nothing to
			// join Sport/Date from, so this analysis cannot be scored. The
			// same defensive skip analyseNewSessions' own candidates filter
			// already relies elsewhere: better to leave one ride out than
			// fail the whole sync over it.
			continue
		}
		curve := make(map[int]float64, len(a.PowerCurve))
		for k, v := range a.PowerCurve {
			if sec, err := strconv.Atoi(k); err == nil {
				curve[sec] = v
			}
		}
		rides = append(rides, thresholds.Ride{
			SessionID: a.SessionID, Date: sess.Date, Sport: sess.Sport,
			PowerCurve: curve, MaxHR: a.MaxHR,
			BestSpeed1200: a.BestSpeed1200, BestSpeed1800: a.BestSpeed1800,
		})
		if sess.Sport == "cycling" && sess.Date >= ftpCutoff && (curve[1200] > 0 || curve[3600] > 0) {
			hasFTPPowerCurve = true
		}
	}

	tp := thresholds.Profile{
		FTPWatts: profile.FTPWatts, FTPEstimated: profile.FTPEstimated,
		MaxHR: profile.MaxHR, MaxHREstimated: profile.IsEstimated(workout.FieldMaxHR),
		ThresholdPaceSecPerKM: profile.ThresholdPaceSecPerKM, PaceEstimated: profile.IsEstimated(workout.FieldThresholdPace),
	}

	result := thresholdDetectionResult{Profile: profile, HasFTPPowerCurve: hasFTPPowerCurve}
	findings := thresholds.Detect(rides, tp, now)
	foundField := make(map[string]bool, len(findings))
	for _, f := range findings {
		foundField[f.Field] = true
		if f.Auto {
			sugg := autoprofile.Suggestion{}
			switch f.Field {
			case "ftp":
				sugg.FTPWatts = f.Value
			case workout.FieldMaxHR:
				sugg.MaxHR = int(math.Round(f.Value))
			case workout.FieldThresholdPace:
				sugg.ThresholdPaceSecPerKM = f.Value
			}
			var changed []string
			result.Profile, changed = autoprofile.Apply(result.Profile, sugg)
			if len(changed) > 0 {
				result.AutoFields = append(result.AutoFields, changed...)
				result.Detected = append(result.Detected, detectedThresholdDTO{Field: f.Field, Value: f.Value, Reason: f.Reason})
				s.logger().Info("threshold auto-applied", "rider", rider, "field", f.Field, "value", f.Value)
			}
			continue
		}
		if err := s.upsertThresholdSuggestion(ctx, rider, f); err != nil {
			return thresholdDetectionResult{}, err
		}
	}

	// Stale-suggestion cleanup: a field this pass found nothing to say about
	// — the rider already typed a value close enough, the ride evidence that
	// produced an earlier suggestion has aged out of the window, whatever —
	// must not leave an outdated pending suggestion sitting in the Fitness
	// banner forever. Only fields with NO finding at all this pass are
	// touched; a field that did produce a finding (Auto or not) is already
	// handled above (Auto never leaves a suggestion behind, and
	// upsertThresholdSuggestion's own CreateSuggestion replaces any existing
	// pending row for that field with the fresh one).
	for _, field := range thresholdFields {
		if foundField[field] {
			continue
		}
		if err := s.Training.DeletePendingSuggestion(ctx, rider, field); err != nil {
			return thresholdDetectionResult{}, err
		}
	}

	return result, nil
}

// thresholdFields is every field internal/thresholds.Detect can report on,
// in Detect's own fixed order — what the stale-suggestion cleanup above
// walks to find a field with nothing to say this pass.
var thresholdFields = []string{"ftp", workout.FieldMaxHR, workout.FieldThresholdPace}

// upsertThresholdSuggestion stores f as a pending suggestion, unless the
// most recently dismissed suggestion for this rider/field/*direction* says
// otherwise — spec: "a dismissed suggestion reappears only if a later
// estimate moves at least a further 3% beyond the dismissed value (1 bpm
// for max HR)." The lookup itself is direction-scoped — LatestDismissedSuggestion
// only ever returns a same-direction row — so an up finding is never held
// back by a dismissed *down* suggestion, or vice versa: those describe
// unrelated claims ("FTP dropped" and "FTP rose" are not the same estimate
// moving further, one replacing a dismissal of the other would be a bug).
func (s *Server) upsertThresholdSuggestion(ctx context.Context, rider string, f thresholds.Finding) error {
	dismissed, ok, err := s.Training.LatestDismissedSuggestion(ctx, rider, f.Field, f.Direction)
	if err != nil {
		return err
	}
	if ok && !thresholdMovedFurther(f, dismissed) {
		return nil
	}

	if _, err := s.Training.CreateSuggestion(ctx, workout.ThresholdSuggestion{
		Rider: rider, Field: f.Field, Value: f.Value, Previous: f.Previous, Direction: f.Direction,
		SourceSessionID: f.SourceSessionID, SourceDate: f.SourceDate, Reason: f.Reason,
	}); err != nil {
		return err
	}
	s.logger().Info("threshold suggestion created", "rider", rider, "field", f.Field, "value", f.Value)
	return nil
}

// thresholdMovedFurther is the dismissed-suggestion gate's margin check:
// given a same-direction dismissal (the caller already scoped
// LatestDismissedSuggestion to f.Direction), has f moved at least the
// spec's own further-margin beyond it (3%, or 1 bpm for max HR)?
func thresholdMovedFurther(f thresholds.Finding, dismissed workout.ThresholdSuggestion) bool {
	if f.Field == workout.FieldMaxHR {
		if f.Direction == "up" {
			return f.Value >= dismissed.Value+1
		}
		return f.Value <= dismissed.Value-1
	}
	if f.Direction == "up" {
		return f.Value >= dismissed.Value*1.03
	}
	return f.Value <= dismissed.Value*0.97
}

// ---------- Accept/dismiss ----------

// applyAcceptedThreshold writes a suggestion's value straight to the
// profile and clears whichever estimated marker the field carries — spec:
// "accept writes the profile (rider-typed from then on)". Unlike
// autoprofile.Apply, this never checks whether the field is fillable: an
// explicit accept always wins, that is the whole point of the rider having
// looked at it and confirmed it.
func applyAcceptedThreshold(p workout.RiderProfile, s workout.ThresholdSuggestion) workout.RiderProfile {
	switch s.Field {
	case "ftp":
		p.FTPWatts = s.Value
		p.FTPEstimated = false
	case workout.FieldMaxHR:
		p.MaxHR = int(math.Round(s.Value))
		p.Estimated = removeEstimatedField(p.Estimated, workout.FieldMaxHR)
	case workout.FieldThresholdPace:
		p.ThresholdPaceSecPerKM = s.Value
		p.Estimated = removeEstimatedField(p.Estimated, workout.FieldThresholdPace)
	}
	return p
}

// removeEstimatedField filters field out of an Estimated list in place —
// the accept-side counterpart to workout.RiderProfile.MarkEstimated, which
// has no unmark of its own since nothing needed one before this feature.
func removeEstimatedField(fields []string, field string) []string {
	out := fields[:0]
	for _, f := range fields {
		if f != field {
			out = append(out, f)
		}
	}
	return out
}

type resolveThresholdRequestDTO struct {
	Action string `json:"action"`
}

// handleListThresholds is GET /api/training/thresholds: a rider's own
// pending suggestions, newest first. Owner-only by construction — the rider
// comes from the session, so there is nothing to check against a path
// parameter the way accept/dismiss has to.
func (s *Server) handleListThresholds(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	list, err := s.Training.ListPendingSuggestions(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	dtos := make([]thresholdSuggestionDTO, 0, len(list))
	for _, sug := range list {
		dtos = append(dtos, thresholdSuggestionDTOFrom(sug))
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": dtos})
}

// handleResolveThreshold is POST /api/training/thresholds/{id}, body
// {"action": "accept"|"dismiss"} — see the design spec's "API and UI".
func (s *Server) handleResolveThreshold(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	var body resolveThresholdRequestDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTrainingBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Action != "accept" && body.Action != "dismiss" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `action must be "accept" or "dismiss"`})
		return
	}

	id := r.PathValue("id")
	sug, err := s.Training.GetSuggestion(r.Context(), id)
	if err != nil {
		s.failTrainingLookup(w, err)
		return
	}

	identity := auth.FromContext(r.Context())
	if !isOwnTraining(identity, sug.Rider) {
		// Not forbidTraining's 403: an unknown id and another rider's
		// suggestion are indistinguishable from outside — spec: "404 for
		// another rider's or unknown id."
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such suggestion"})
		return
	}
	if sug.Status != workout.ThresholdPending {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this suggestion is no longer pending"})
		return
	}

	switch body.Action {
	case "accept":
		profile, _, err := s.Training.GetProfile(r.Context(), sug.Rider)
		if err != nil {
			s.fail(w, err)
			return
		}
		profile.Rider = sug.Rider
		profile = applyAcceptedThreshold(profile, sug)
		if _, err := s.Training.SaveProfile(r.Context(), profile); err != nil {
			s.fail(w, err)
			return
		}
		if err := s.Training.MarkSuggestionAccepted(r.Context(), id); err != nil {
			s.failTrainingLookup(w, err)
			return
		}
		s.logger().Info("threshold suggestion accepted", "rider", sug.Rider, "field", sug.Field, "value", sug.Value)
	case "dismiss":
		if err := s.Training.MarkSuggestionDismissed(r.Context(), id); err != nil {
			s.failTrainingLookup(w, err)
			return
		}
		s.logger().Info("threshold suggestion dismissed", "rider", sug.Rider, "field", sug.Field, "value", sug.Value)
	}

	updated, err := s.Training.GetSuggestion(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, thresholdSuggestionDTOFrom(updated))
}
