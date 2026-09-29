package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/autoprofile"
	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/providerlink"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
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
	// AutoFilled names every profile field this sync filled in or refreshed
	// on its own — from Garmin's biometrics or the rider's own history — so
	// the UI can tell them what changed and ask them to check it.
	AutoFilled []string `json:"autoFilled,omitempty"`
	// Detected lists every threshold (FTP, max HR, threshold pace) this
	// sync's own internal/thresholds.Detect pass applied straight to the
	// profile — an Auto finding, for an empty or already-estimated field.
	// See detectThresholds' own doc comment; a rider-typed field never
	// appears here, it gets a stored suggestion instead (GET
	// /api/training/thresholds).
	Detected []detectedThresholdDTO `json:"detected,omitempty"`
	// LevelsRecalibrated is set only when this sync's FTP change lowered the
	// rider's progression levels (see recalibrateLevelsForFTP) — the same
	// shape the threshold-accept and profile-save responses carry, so the
	// UI toasts one line for all three. Background syncs have nobody
	// looking at a screen; the Progression card's Reason is what they see.
	LevelsRecalibrated *levelsRecalibratedDTO `json:"levelsRecalibrated,omitempty"`
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

	result, err := s.syncRiderMetrics(r.Context(), rider, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// biometricsRefreshEvery bounds how often the background loop asks Garmin for
// a rider's biometrics and resting heart rate. Both change over weeks, not
// hours, and both are undocumented endpoints: asking every half-hour tick for
// every rider would be the sort of steady traffic that gets an unofficial
// client noticed, for numbers that would come back identical.
const biometricsRefreshEvery = 24 * time.Hour

type cachedBiometrics struct {
	at         time.Time
	biometrics garmin.Biometrics
	restingHR  int
}

// biometricsCache remembers each rider's last Garmin biometrics read, so the
// ticks between refreshes still know what Garmin said. That matters beyond
// saving calls: the profile merge below prefers Garmin's own FTP over the
// one estimated from sessions, and a tick that skipped the fetch and forgot
// the answer would let the estimate quietly overwrite it. In-memory only —
// a restart costs one extra read per rider, nothing more.
type biometricsCache struct {
	mu sync.Mutex
	m  map[string]cachedBiometrics
}

func (c *biometricsCache) get(rider string) (cachedBiometrics, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[rider]
	return v, ok
}

func (c *biometricsCache) put(rider string, v cachedBiometrics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedBiometrics{}
	}
	c.m[rider] = v
}

// garminBiometrics returns what Garmin holds about the rider's physiology,
// from the cache unless it is stale or force is set (a rider's own "Sync
// now" always asks — they are waiting on the answer).
//
// Deliberately silent to the rider on failure, logged at Warn: these are
// undocumented endpoints with no coverage against a live account (see
// garmin.Client.Biometrics and RestingHeartRate), and an optional
// enhancement to an otherwise-successful sync is exactly the "did not
// happen, nothing is broken" case AGENTS.md's Observability checklist calls
// a Warn, not something that should read as a sync failure.
func (s *Server) garminBiometrics(ctx context.Context, rider string, session garmin.Session, wantResting, force bool) cachedBiometrics {
	if cached, ok := s.biometrics.get(rider); ok && !force && time.Since(cached.at) < biometricsRefreshEvery {
		return cached
	}

	consumer, _ := s.garminConsumer()
	out := cachedBiometrics{at: time.Now()}

	b, err := s.Garmin.Biometrics(ctx, consumer, session, time.Now())
	if err != nil {
		s.logger().Warn("garmin biometrics lookup incomplete", "rider", rider, "err", err)
	}
	out.biometrics = b

	// Resting HR is a direct overnight measurement, not something computed
	// from ride data. A small run of recent dates, since a day with no
	// reading — the watch was not worn to bed — is normal and not a reason
	// to give up after one try.
	if wantResting {
		// Collects which sources came back empty and the daily summary's
		// key names (never values) across every date tried, for the one
		// Warn below.
		ctx, diag := garmin.WithRHRDiagnostics(ctx)
		lookupFailed := false
		for daysAgo := 0; daysAgo < 3 && out.restingHR == 0; daysAgo++ {
			bpm, err := s.Garmin.RestingHeartRate(ctx, consumer, session, time.Now().AddDate(0, 0, -daysAgo))
			if err != nil {
				s.logger().Warn("garmin resting heart rate lookup failed", "rider", rider, "err", err)
				lookupFailed = true
				break
			}
			out.restingHR = bpm
		}
		// Once per rider per sync, and only when every date came back empty
		// from every source: the next production log then shows what Garmin
		// actually returned. Warn, not Error — the sync itself succeeded.
		if out.restingHR == 0 && !lookupFailed {
			if sources := diag.Sources(); len(sources) > 0 {
				s.logger().Warn("garmin resting heart rate: no reading from any source",
					"rider", rider, "sources", sources, "summary_keys", diag.SummaryKeys())
			}
		}
	}

	s.biometrics.put(rider, out)
	return out
}

// syncRiderMetrics pulls recently completed activities from whichever of the
// rider's own Garmin/Wahoo accounts are connected, records them as
// completed_sessions, recomputes the rider's whole CTL/ATL/TSB history, and
// fills in whatever of their fitness profile is still empty — from Garmin's
// own biometrics where it holds them, and from the pattern in the rider's
// own history otherwise (see internal/autoprofile for what may and may not
// be overwritten). Takes a bare context.Context rather than *http.Request so
// the background loop can call it too, with no request of its own — the same
// split scheduleGoal already makes for the same reason.
//
// force skips the biometrics cache; the rider's own click sets it.
//
// A provider failing is a warning in the result, never an error: only a
// problem with this app's own storage returns one.
func (s *Server) syncRiderMetrics(ctx context.Context, rider string, force bool) (syncMetricsResultDTO, error) {
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return syncMetricsResultDTO{}, err
	}
	// GetProfile returns a zero-value RiderProfile (Rider == "") when the
	// rider has never saved one yet — SaveProfile refuses that as ownerless.
	profile.Rider = rider
	// What the profile held before this sync touched it: the shared
	// recalibration helper needs it to tell a first save from a rise.
	before := profile

	var warnings []string
	synced := 0
	var restingHR int
	var autoFilled []string

	garminSession, garminConnected := garmin.Session{}, false
	if s.Garmin != nil {
		garminSession, garminConnected = s.garminSessionForRider(rider)
	}

	// sessionFITSources carries the Garmin activity id / Wahoo file URL and
	// provider summary numbers for exactly the sessions this sync's own
	// provider lists just fetched — see analyseNewSessions' own doc comment
	// for why this is kept in memory only, for the duration of this one
	// call, rather than added to completed_sessions.
	sessionFITSources := map[string]sessionFITSource{}
	var wahooToken string
	var wahooConnected bool

	// Biometrics first, and merged into the profile before any session is
	// recorded: a session's training load is computed from FTP and heart
	// rate, so a first sync should score its history with the numbers Garmin
	// just supplied, not with an empty profile.
	var suggestion autoprofile.Suggestion
	if garminConnected {
		wantResting := profile.RestingHR == 0 || profile.IsEstimated(workout.FieldRestingHR)
		bio := s.garminBiometrics(ctx, rider, garminSession, wantResting, force)
		suggestion = autoprofile.Suggestion{
			FTPWatts:              bio.biometrics.CyclingFTPWatts,
			MaxHR:                 bio.biometrics.MaxHR,
			ThresholdPaceSecPerKM: bio.biometrics.ThresholdPaceSecPerKM,
			ThresholdHR:           bio.biometrics.ThresholdHR,
			RestingHR:             bio.restingHR,
		}
		restingHR = bio.restingHR
		var changed []string
		profile, changed = autoprofile.Apply(profile, suggestion)
		autoFilled = append(autoFilled, changed...)

		consumer, _ := s.garminConsumer()
		s.syncRiderWellness(ctx, rider, consumer, garminSession)
	}

	if garminConnected {
		consumer, _ := s.garminConsumer()
		activities, err := s.Garmin.ListActivities(ctx, consumer, garminSession)
		if err != nil {
			warnings = append(warnings, "garmin: "+err.Error())
		} else {
			for _, a := range activities {
				if a.ID == "" || a.StartTime.IsZero() {
					continue
				}
				load := workout.TrainingLoad(a.DurationSeconds, a.AvgPowerWatts, a.AvgHR, profile)
				session, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
					Rider: rider, Provider: "garmin", ExternalID: a.ID, Sport: a.Sport,
					Date: a.StartTime.Format("2006-01-02"), DurationSeconds: a.DurationSeconds,
					DistanceM: a.DistanceM, AvgHR: a.AvgHR, AvgPowerWatts: a.AvgPowerWatts, TrainingLoad: load,
				})
				if err != nil {
					warnings = append(warnings, "garmin: recording a session: "+err.Error())
					continue
				}
				synced++
				sessionFITSources[session.ID] = sessionFITSource{
					garminActivityID: a.ID,
					summary: rideanalysis.Summary{
						DurationSeconds: a.DurationSeconds,
						AvgPower:        a.AvgPowerWatts,
						AvgHR:           a.AvgHR,
						NormalizedPower: a.NormalizedPower,
						TSS:             a.TrainingStressScore,
						IntensityFactor: a.IntensityFactor,
					},
				}
			}
		}
	}

	if s.Wahoo != nil {
		token, err := s.wahooAccessToken(ctx, rider)
		wahooToken, wahooConnected = token, err == nil
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
					session, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
						// Wahoo's completed-workout list does not carry a
						// sport this pass decodes (see internal/wahoo's own
						// doc comment) — cycling, the same "this is a
						// cycling library" default AGENTS.md already states
						// for Wahoo route push.
						Rider: rider, Provider: "wahoo", ExternalID: wk.ID, Sport: "cycling",
						Date: wk.Starts.Format("2006-01-02"), DurationSeconds: wk.DurationSeconds,
						DistanceM: wk.DistanceM, AvgHR: wk.AvgHR, AvgPowerWatts: wk.AvgPowerWatts, TrainingLoad: load,
					})
					if err != nil {
						warnings = append(warnings, "wahoo: recording a session: "+err.Error())
						continue
					}
					synced++
					sessionFITSources[session.ID] = sessionFITSource{
						wahooFileURL: wk.FileURL,
						summary: rideanalysis.Summary{
							DurationSeconds: wk.DurationSeconds,
							AvgPower:        wk.AvgPowerWatts,
							AvgHR:           wk.AvgHR,
							NormalizedPower: wk.NormalizedPower,
							TSS:             wk.TSS,
						},
					}
				}
			}
		}
	}

	if synced > 0 {
		if err := s.Training.RecomputeFitnessSnapshots(ctx, rider); err != nil {
			return syncMetricsResultDTO{}, err
		}
	}

	// Score every newly (and not-yet) analysed ride against its planned
	// workout — after sessions are upserted, so a ride just synced this
	// pass is already in completed_sessions for MatchPlanned/SaveAnalysis
	// to find. See analyseNewSessions' own doc comment for the 42-day
	// window, the per-provider cap, and why a download/decode/save failure
	// here is a Warn rather than a sync failure.
	consumer, _ := s.garminConsumer()
	if err := s.analyseNewSessions(ctx, rider, profile, sessionFITSources, consumer, garminSession, garminConnected, wahooToken, wahooConnected); err != nil {
		return syncMetricsResultDTO{}, err
	}

	// What the history itself can say, now that it is on file: an FTP
	// estimate (only where Garmin gave none — its own detected FTP is a
	// better number than an average over one ride) and the rider's training
	// pattern. Both merge through the same autoprofile.Apply, so the same
	// rule holds: an unset or already-estimated field may be filled, a
	// rider-confirmed one never is.
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return syncMetricsResultDTO{}, err
	}

	// Threshold detection (internal/thresholds): auto-applies to an empty or
	// estimated field, stores a suggestion for a rider-typed one. Runs
	// before the cruder EstimateFTP fallback below, whose own condition
	// reads tdr.HasFTPPowerCurve to decide whether Detect already had
	// something better to say about FTP.
	tdr, err := s.detectThresholds(ctx, rider, profile, sessions, time.Now())
	if err != nil {
		return syncMetricsResultDTO{}, err
	}
	profile = tdr.Profile
	autoFilled = append(autoFilled, tdr.AutoFields...)

	history := autoprofile.Suggestion{}
	if suggestion.FTPWatts == 0 && !tdr.HasFTPPowerCurve {
		if watts, ok := fitnesstest.EstimateFTP(sessions); ok {
			history.FTPWatts = watts
		}
	}
	if pattern, ok := autoprofile.Infer(sessions, time.Now()); ok {
		history.AvailableDays = pattern.AvailableDays
		history.HoursPerAvailableDay = pattern.HoursPerAvailableDay
		history.ExperienceLevel = pattern.ExperienceLevel
	}
	profile, changed := autoprofile.Apply(profile, history)
	autoFilled = append(autoFilled, changed...)

	// autoFilled can hold a field twice (Garmin's FTP, then a session
	// estimate that replaced it in one run) — report it once.
	autoFilled = uniq(autoFilled)

	var estimatedFTP float64
	if slices.Contains(autoFilled, "ftp") {
		estimatedFTP = profile.FTPWatts
	}
	if !slices.Contains(autoFilled, workout.FieldRestingHR) {
		restingHR = 0
	}

	var recalibrated *levelsRecalibratedDTO
	if len(autoFilled) > 0 {
		if _, err := s.Training.SaveProfile(ctx, profile); err != nil {
			return syncMetricsResultDTO{}, err
		}
		s.logger().Info("training profile auto-filled", "rider", rider, "fields", autoFilled)
		// Whichever source produced a new FTP (Garmin, threshold detection,
		// the EstimateFTP fallback), the over-reach risk is the same, so all
		// of them go through the one helper. The profile write has already
		// landed, so a failure here is logged rather than failing the sync.
		if profile.FTPWatts != before.FTPWatts {
			dto, changed, err := s.recalibrateLevelsForFTP(ctx, rider, before)
			if err != nil {
				s.logger().Error("level recalibration failed", "rider", rider, "err", err)
			}
			if changed {
				recalibrated = &dto
			}
		}
	}

	s.logger().Info("training metrics synced", "rider", rider, "synced", synced, "warnings", len(warnings))
	return syncMetricsResultDTO{
		Synced: synced, Warnings: warnings, EstimatedFTPWatts: estimatedFTP, RestingHRBpm: restingHR,
		AutoFilled: autoFilled, Detected: tdr.Detected, LevelsRecalibrated: recalibrated,
	}, nil
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// autoSyncTrainingMetrics runs syncRiderMetrics for every rider who has
// connected Garmin or Wahoo — the unattended half of the "Sync now" button,
// and the reason the adaptive loop does not plan from stale history: both
// adapter.Reconcile and the FTP/resting-HR estimates read completed_sessions,
// which nothing else fills in without a rider clicking.
//
// Called at the top of AutoScheduleTick, inside its lock, so a week is
// always scheduled from history that has just been refreshed, never from
// whatever the rider last synced by hand — and, independently of that flag,
// twice a day by RunMetricsSyncLoop (see metricssyncloop.go). It reads from
// the providers and writes the rider's own history and thresholds; it never
// touches a workout, which is why it does not need auto-schedule's consent.
//
// Returns how many riders it tried and how many of those failed outright.
func (s *Server) autoSyncTrainingMetrics(ctx context.Context) (riders, failed int) {
	if s.Links == nil {
		return 0, 0
	}

	seen := map[string]bool{}
	var connected []string
	for _, provider := range []string{garminProvider, wahooProvider} {
		list, err := s.Links.ListRiders(provider)
		if err != nil {
			s.logger().Warn("auto-sync metrics: listing riders failed", "provider", provider, "err", err)
			continue
		}
		for _, rider := range list {
			if !seen[rider] {
				seen[rider] = true
				connected = append(connected, rider)
			}
		}
	}

	for _, rider := range connected {
		if ctx.Err() != nil {
			return len(connected), failed
		}
		// A rider one provider's call failed for is still a rider the next
		// one gets synced for — one bad account never aborts the pass, the
		// same rule AGENTS.md states for routes.
		result, err := s.syncRiderMetrics(ctx, rider, false)
		if err != nil {
			s.logger().Error("auto-sync metrics failed", "rider", rider, "err", err)
			failed++
			continue
		}
		for _, warning := range result.Warnings {
			s.logger().Warn("auto-sync metrics warning", "rider", rider, "warning", warning)
		}
	}
	return len(connected), failed
}
