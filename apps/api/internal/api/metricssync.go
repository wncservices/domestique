package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type syncMetricsResultDTO struct {
	Synced int `json:"synced"`
	// Warnings names each provider that failed and why, without failing
	// the whole sync — the same "one bad thing never aborts the rest"
	// contract handleGarminCourseImport's own skipped map already keeps
	// for course imports, applied here across providers instead of across
	// individual items: a rider whose Wahoo token expired should still get
	// their Garmin activities recorded.
	Warnings []string `json:"warnings,omitempty"`
	// EstimatedFTPWatts is set only when this sync just produced a fresh
	// FTP estimate that got saved to the rider's profile — see
	// internal/fitnesstest.EstimateFTP and the call site below for exactly
	// when that happens.
	EstimatedFTPWatts float64 `json:"estimatedFtpWatts,omitempty"`
	// RestingHRBpm is set only when this sync just filled in a
	// previously-unset resting heart rate from Garmin's wellness data —
	// see the call site below for why it is never used to overwrite a
	// value the rider entered themselves.
	RestingHRBpm int `json:"restingHrBpm,omitempty"`
}

// handleSyncTrainingMetrics is a rider's own "Sync now" click — a thin HTTP
// wrapper around syncRiderMetrics, which is also what the unattended loop
// (see AutoScheduleTick) calls for every connected rider, so a rider who
// never opens the Fitness page gets exactly the sync one who clicks the
// button does.
func (s *Server) handleSyncTrainingMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}

	rider := auth.FromContext(r.Context()).User
	if rider == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no rider in the session"})
		return
	}

	result, err := s.syncRiderMetrics(r.Context(), rider)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// syncRiderMetrics pulls recently completed activities from whichever of the
// rider's own Garmin/Wahoo accounts are connected, records them as
// completed_sessions, and recomputes the rider's whole CTL/ATL/TSB history
// from the result. Takes a bare context.Context rather than *http.Request so
// the background loop can call it too, with no request of its own — the same
// split scheduleGoal already makes for the same reason.
//
// A provider failing is a warning in the result, never an error: only a
// problem with this app's own storage returns one.
func (s *Server) syncRiderMetrics(ctx context.Context, rider string) (syncMetricsResultDTO, error) {
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return syncMetricsResultDTO{}, err
	}

	var warnings []string
	synced := 0
	var restingHR int

	if s.Garmin != nil {
		if session, ok := s.garminSessionForRider(rider); ok {
			consumer, _ := s.garminConsumer()
			activities, err := s.Garmin.ListActivities(ctx, consumer, session)
			if err != nil {
				warnings = append(warnings, "garmin: "+err.Error())
			} else {
				for _, a := range activities {
					if a.ID == "" || a.StartTime.IsZero() {
						continue
					}
					load := workout.TrainingLoad(a.DurationSeconds, a.AvgPowerWatts, a.AvgHR, profile)
					if _, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
						Rider: rider, Provider: "garmin", ExternalID: a.ID, Sport: a.Sport,
						Date: a.StartTime.Format("2006-01-02"), DurationSeconds: a.DurationSeconds,
						DistanceM: a.DistanceM, AvgHR: a.AvgHR, AvgPowerWatts: a.AvgPowerWatts, TrainingLoad: load,
					}); err != nil {
						warnings = append(warnings, "garmin: recording a session: "+err.Error())
						continue
					}
					synced++
				}
			}

			// Resting HR is a direct overnight measurement, not something
			// computed from ride data the way FTP is — only worth asking
			// for at all when the rider has not already typed one in (see
			// the SaveProfile call below for why that value is never
			// overwritten once set). A small run of recent dates, since a
			// day with no reading — the watch was not worn to bed — is
			// normal and not a reason to give up after one try.
			//
			// Deliberately silent on failure rather than a warning: this
			// hits an undocumented Connect endpoint with no coverage
			// against a live account (see garmin.Client.RestingHeartRate's
			// own doc comment), and it is an optional enhancement to an
			// otherwise-successful sync — exactly the "the action did not
			// happen, but nothing is broken" case AGENTS.md's own
			// Observability checklist calls a Warn, not something that
			// should read as a sync failure to the rider.
			if profile.RestingHR == 0 {
				for daysAgo := 0; daysAgo < 3 && restingHR == 0; daysAgo++ {
					bpm, err := s.Garmin.RestingHeartRate(ctx, consumer, session, time.Now().AddDate(0, 0, -daysAgo))
					if err != nil {
						s.logger().Warn("garmin resting heart rate lookup failed", "rider", rider, "err", err)
						break
					}
					restingHR = bpm
				}
			}
		}
	}

	if s.Wahoo != nil {
		token, err := s.wahooAccessToken(ctx, rider)
		if err != nil {
			// A rider who has simply never connected Wahoo is not a sync
			// failure — the same non-event garminSessionForRider's own `ok`
			// return already treats it as for Garmin, above. Only a real
			// problem (an expired refresh token, an unreadable stored
			// session, a misconfigured deployment) is worth a warning; every
			// "Sync now" click otherwise re-surfaces "has not connected
			// Wahoo" forever for a rider who only uses Garmin.
			if !errors.Is(err, providerlink.ErrNotFound) {
				warnings = append(warnings, "wahoo: "+err.Error())
			}
		} else {
			workouts, err := s.Wahoo.ListWorkouts(ctx, token, 1, 30)
			if err != nil {
				warnings = append(warnings, "wahoo: "+err.Error())
			} else {
				for _, wk := range workouts {
					if wk.ID == "" || wk.Starts.IsZero() {
						continue
					}
					load := workout.TrainingLoad(wk.DurationSeconds, wk.AvgPowerWatts, wk.AvgHR, profile)
					if _, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
						// Wahoo's completed-workout list does not carry a
						// sport this pass decodes (see internal/wahoo's own
						// doc comment) — cycling, the same "this is a
						// cycling library" default AGENTS.md already states
						// for Wahoo route push.
						Rider: rider, Provider: "wahoo", ExternalID: wk.ID, Sport: "cycling",
						Date: wk.Starts.Format("2006-01-02"), DurationSeconds: wk.DurationSeconds,
						DistanceM: wk.DistanceM, AvgHR: wk.AvgHR, AvgPowerWatts: wk.AvgPowerWatts, TrainingLoad: load,
					}); err != nil {
						warnings = append(warnings, "wahoo: recording a session: "+err.Error())
						continue
					}
					synced++
				}
			}
		}
	}

	if synced > 0 {
		if err := s.Training.RecomputeFitnessSnapshots(ctx, rider); err != nil {
			return syncMetricsResultDTO{}, err
		}
	}

	// Both of these fold into one SaveProfile call below rather than two —
	// a first sync on a fresh profile can legitimately produce both a new
	// FTP estimate and a first resting HR reading at once, and there is no
	// reason to write the profile twice for that.
	//
	// GetProfile returns a zero-value RiderProfile (Rider == "") when the
	// rider has never saved one yet — has to be set back before either
	// mutation, or SaveProfile refuses it as ownerless.
	var estimatedFTP float64
	profileChanged := false

	if profile.RestingHR == 0 && restingHR > 0 {
		profile.Rider = rider
		profile.RestingHR = restingHR
		profileChanged = true
		s.logger().Info("resting heart rate synced from garmin", "rider", rider, "bpm", restingHR)
	}

	// Refresh the FTP estimate from whatever sessions are now on file —
	// but only into a field the rider has never confirmed by hand: 0 (never
	// set) or FTPEstimated (a previous estimate, safe to keep refining).
	// See RiderProfile.FTPEstimated's own doc comment for why a
	// rider-entered value is never touched here, synced or not.
	if synced > 0 && (profile.FTPWatts == 0 || profile.FTPEstimated) {
		sessions, err := s.Training.ListSessions(ctx, rider)
		if err != nil {
			return syncMetricsResultDTO{}, err
		}
		if watts, ok := fitnesstest.EstimateFTP(sessions); ok && watts != profile.FTPWatts {
			profile.Rider = rider
			profile.FTPWatts = watts
			profile.FTPEstimated = true
			profileChanged = true
			estimatedFTP = watts
			s.logger().Info("ftp estimated from synced sessions", "rider", rider, "watts", watts)
		}
	}

	if profileChanged {
		if _, err := s.Training.SaveProfile(ctx, profile); err != nil {
			return syncMetricsResultDTO{}, err
		}
	}

	s.logger().Info("training metrics synced", "rider", rider, "synced", synced, "warnings", len(warnings))
	return syncMetricsResultDTO{Synced: synced, Warnings: warnings, EstimatedFTPWatts: estimatedFTP, RestingHRBpm: restingHR}, nil
}

// autoSyncTrainingMetrics runs syncRiderMetrics for every rider who has
// connected Garmin or Wahoo — the unattended half of the "Sync now" button,
// and the reason the adaptive loop does not plan from stale history: both
// adapter.Reconcile and the FTP/resting-HR estimates read completed_sessions,
// which nothing else fills in without a rider clicking.
//
// Called at the top of AutoScheduleTick, inside its lock, so a week is
// always scheduled from history that has just been refreshed, never from
// whatever the rider last synced by hand.
func (s *Server) autoSyncTrainingMetrics(ctx context.Context) {
	if s.Links == nil {
		return
	}

	seen := map[string]bool{}
	var riders []string
	for _, provider := range []string{garminProvider, wahooProvider} {
		list, err := s.Links.ListRiders(provider)
		if err != nil {
			s.logger().Warn("auto-sync metrics: listing riders failed", "provider", provider, "err", err)
			continue
		}
		for _, rider := range list {
			if !seen[rider] {
				seen[rider] = true
				riders = append(riders, rider)
			}
		}
	}

	for _, rider := range riders {
		if ctx.Err() != nil {
			return
		}
		// A rider one provider's call failed for is still a rider the next
		// one gets synced for — one bad account never aborts the pass, the
		// same rule AGENTS.md states for routes.
		result, err := s.syncRiderMetrics(ctx, rider)
		if err != nil {
			s.logger().Error("auto-sync metrics failed", "rider", rider, "err", err)
			continue
		}
		for _, warning := range result.Warnings {
			s.logger().Warn("auto-sync metrics warning", "rider", rider, "warning", warning)
		}
	}
}
