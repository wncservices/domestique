package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/muktihari/fit/profile/filedef"

	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/progression"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
	"github.com/wncservices/domestique/apps/api/internal/rideimport"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Errors fetchSessionFIT reports when there is simply nothing to download —
// not a provider failure, just a source this sync cannot reach (a rider
// disconnected the provider since the session was recorded, or — Wahoo's own
// case — the file URL was never stored past the sync that first saw it).
var (
	errNoGarminSession = errors.New("no garmin session for this rider")
	errNoWahooSession  = errors.New("no wahoo session for this rider")
	errNoWahooFileURL  = errors.New("no stored wahoo file url for this session")
	errUnknownProvider = errors.New("unknown provider")
)

// maxAnalysesPerProviderPerSync caps how many rides get a fresh FIT
// download-and-analyse pass in one sync, per provider — spec: "cap 20 per
// rider per provider per sync." A first sync for a rider with months of
// history would otherwise try to download every ride's FIT file in one
// request; ListSessions' own newest-first order means the most recent rides
// win, and whatever is left over catches up on the next sync.
const maxAnalysesPerProviderPerSync = 20

// rideAnalysisWindow is how far back a completed session is still worth
// analysing — spec: "42-day window." That is also ctlDays' own constant in
// internal/workout: a ride older than this has long since aged out of the
// window that training load actually accounts for, so there is nothing to
// gain by chasing down its FIT file.
const rideAnalysisWindow = 42 * 24 * time.Hour

// sessionFITSource is what this sync's own provider list carries for a
// session that was just upserted — the Garmin activity id (already exactly
// completed_sessions.external_id, confirmed against UpsertSession's own
// `id := req.Provider + ":" + req.ExternalID`, so an old session's FIT can
// always be re-requested from Garmin) or the Wahoo file URL (never stored
// anywhere — an old Wahoo session has no way back to its FIT file, so it is
// analysed from the stored summary numbers only). Kept in memory for the
// duration of one sync and dropped afterwards; see AGENTS.md's "never
// persist raw ride data" — this holds an id and a URL, never FIT bytes.
type sessionFITSource struct {
	garminActivityID string
	wahooFileURL     string
	summary          rideanalysis.Summary
	// bestPower is the provider's own best-average-power figures (Garmin's
	// maxAvgPower_*), keyed by window in seconds. It stands in for the FIT's
	// power curve when the file cannot be read: an FTP test needs only one
	// of those windows, and the provider has already worked it out.
	bestPower map[int]float64
}

// analyseNewSessions scores every one of the rider's completed sessions that
// has no analysis in the last rideAnalysisWindow, newest first, up to
// maxAnalysesPerProviderPerSync per provider — see syncRiderMetrics' own
// call site for where this runs (after sessions are upserted) and why.
//
// sources carries the provider ids/URLs this sync's own activity/workout
// lists just supplied, keyed by session id ("provider:externalID") — for a
// session synced in an earlier run that source map has nothing, and this
// falls back to completed_sessions.external_id for Garmin (always
// recoverable) or to the session's own stored summary numbers for Wahoo (a
// file URL is never stored, so there is nothing left to download).
//
// Returns an error only for a failure to read this app's own storage
// (ListSessions/ListAnalyses/ListWorkouts) — the same "only a problem with
// our own storage fails the sync" rule syncRiderMetrics already follows. A
// FIT download or decode failure, or a failed SaveAnalysis/SetSessionLoad,
// is a Warn and the ride is simply retried on the next sync.
func (s *Server) analyseNewSessions(
	ctx context.Context,
	rider string,
	profile workout.RiderProfile,
	sources map[string]sessionFITSource,
	consumer GarminConsumer,
	garminSession garmin.Session,
	garminConnected bool,
	wahooToken string,
	wahooConnected bool,
) ([]string, error) {
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return nil, err
	}

	sinceDate := time.Now().Add(-rideAnalysisWindow).Format("2006-01-02")
	analyzed, err := s.Training.ListAnalyses(ctx, rider, sinceDate)
	if err != nil {
		return nil, err
	}
	analyzedIDs := make(map[string]bool, len(analyzed))
	for _, a := range analyzed {
		analyzedIDs[a.SessionID] = true
	}

	planned, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return nil, err
	}
	links, err := s.Training.SessionLinks(ctx, rider)
	if err != nil {
		return nil, err
	}
	var notes []string

	// sessions is already "most recent first" (ListSessions' own ordering),
	// so capping the first N seen per provider is exactly "newest 20".
	perProvider := map[string]int{}
	var candidates []workout.CompletedSession
	for _, sess := range sessions {
		if sess.Date < sinceDate {
			continue
		}
		if analyzedIDs[sess.ID] {
			continue
		}
		// An imported ride has no provider to fetch its file from, and is
		// analysed when it is imported: there is nothing to retry here, and
		// trying would only log a failed download on every sync.
		if sess.Provider == rideimport.Provider {
			continue
		}
		if perProvider[sess.Provider] >= maxAnalysesPerProviderPerSync {
			continue
		}
		perProvider[sess.Provider]++
		candidates = append(candidates, sess)
	}

	var loadChanged bool
	analyzedCount := map[string]int{}
	downloadFailed := 0
	for _, sess := range candidates {
		src, hasSource := sources[sess.ID]

		raw, downloadErr := s.fetchSessionFIT(ctx, sess, src, hasSource, consumer, garminSession, garminConnected, wahooToken, wahooConnected)
		var act *filedef.Activity
		var fitErr error
		switch {
		case downloadErr != nil:
			fitErr = downloadErr
			downloadFailed++
			s.logger().Warn("ride analysis: fit download failed", "rider", rider, "provider", sess.Provider, "session", sess.ID, "err", downloadErr)
		default:
			decoded, decodeErr := rideanalysis.DecodeFIT(raw)
			if decodeErr != nil {
				fitErr = decodeErr
				s.logger().Warn("ride analysis: fit decode failed", "rider", rider, "provider", sess.Provider, "session", sess.ID, "err", decodeErr)
			} else {
				act = decoded
			}
		}

		summary := rideanalysis.Summary{
			DurationSeconds: sess.DurationSeconds,
			AvgPower:        sess.AvgPowerWatts,
			AvgHR:           sess.AvgHR,
		}
		if hasSource {
			summary.NormalizedPower = src.summary.NormalizedPower
			summary.TSS = src.summary.TSS
			summary.IntensityFactor = src.summary.IntensityFactor
			if src.summary.DurationSeconds > 0 {
				summary.DurationSeconds = src.summary.DurationSeconds
			}
			if src.summary.AvgPower > 0 {
				summary.AvgPower = src.summary.AvgPower
			}
			if src.summary.AvgHR > 0 {
				summary.AvgHR = src.summary.AvgHR
			}
		}

		matched := plannedFor(sess, summary.DurationSeconds, planned, links)
		// An FTP test's whole result comes from the ride's own power curve. A
		// FIT that could not be fetched or decoded says nothing about the ride,
		// and saving a curve-less analysis now would stop the ride ever being
		// analysed again, so the test would be read as "no power" for good. Leave
		// it unanalysed; the next sync tries the file again.
		//
		// Unless the provider's own best-power figures already hold the window
		// the protocol reads: then the file is not needed for the result.
		isTest := matched != nil && matched.TestProtocol != ""
		var fallbackCurve map[int]float64
		if act == nil && hasSource {
			fallbackCurve = src.bestPower
		}
		if act == nil && isTest {
			if _, ok := fitnesstest.FTPFromTest(matched.TestProtocol, fallbackCurve); !ok {
				s.logger().Warn("ride analysis: an FTP test ride has no readable FIT yet, will retry", "rider", rider, "provider", sess.Provider, "session", sess.ID, "err", fitErr)
				// Said out loud as well, with the reason: otherwise the test
				// just looks unmatched and nobody can tell why.
				notes = append(notes, fmt.Sprintf("%s: the activity file for your FTP test on %s could not be read yet (%v); it is tried again on the next sync.", sess.Provider, sess.Date, fitErr))
				continue
			}
			s.logger().Info("ride analysis: FTP test read from the provider's best-power figures, the FIT was unreadable", "rider", rider, "provider", sess.Provider, "session", sess.ID, "err", fitErr)
		}
		analysis := rideanalysis.Analyze(rideanalysis.Input{
			Sport:    sess.Sport,
			Activity: act,
			Summary:  summary,
			Planned:  matched,
			Profile:  profile,
		})
		if act == nil && len(analysis.PowerCurve) == 0 && len(fallbackCurve) > 0 {
			analysis.PowerCurve = fallbackCurve
		}

		var workoutID string
		if matched != nil {
			workoutID = matched.ID
		}

		sa := workout.SessionAnalysis{
			SessionID:        sess.ID,
			Rider:            rider,
			WorkoutID:        workoutID,
			Outcome:          string(analysis.Outcome),
			LoadSource:       string(analysis.LoadSource),
			NormalizedPower:  analysis.NormalizedPower,
			IntensityFactor:  analysis.IntensityFactor,
			TSS:              analysis.TSS,
			DurationRatio:    analysis.DurationRatio,
			MaxHR:            analysis.MaxHR,
			BestHR1200:       analysis.BestHR1200,
			BestSpeed1200:    analysis.BestSpeed1200,
			BestSpeed1800:    analysis.BestSpeed1800,
			PowerZoneSeconds: analysis.PowerZoneSeconds[:],
			HRZoneSeconds:    analysis.HRZoneSeconds[:],
			PowerCurve:       powerCurveDTO(analysis.PowerCurve),
			Steps:            analysisStepsDTO(analysis.Steps),
		}

		if err := s.Training.SaveAnalysis(ctx, sa); err != nil {
			// The sync still succeeds — the ride stays unanalysed (no
			// analysis row was written) and is picked up again on the next
			// sync, since analyzedIDs above is exactly "has a saved
			// analysis."
			s.logger().Warn("ride analysis: saving the analysis failed", "rider", rider, "provider", sess.Provider, "session", sess.ID, "err", err)
			if isTest {
				notes = append(notes, fmt.Sprintf("%s: your FTP test ride on %s could not be saved (%v); it is tried again on the next sync.", sess.Provider, sess.Date, err))
			}
			continue
		}

		// A level move only happens here, right after an analysis is saved
		// for the first time — analyseNewSessions' own candidates filter
		// (analyzedIDs) never re-analyses a session that already has one, so
		// this can never run twice for the same ride: that is what keeps a
		// level move idempotent across syncs, with no extra bookkeeping
		// needed here. A failure here is a Warn — the sync as a whole still
		// succeeds, the ride's own analysis already saved.
		if err := s.applyProgressionForAnalysis(ctx, rider, profile, matched, sess.ID, sa.Outcome); err != nil {
			s.logger().Warn("ride analysis: applying the progression level change failed", "rider", rider, "provider", sess.Provider, "session", sess.ID, "err", err)
		}

		if err := s.Training.SetSessionLoad(ctx, sess.ID, analysis.Load); err != nil {
			s.logger().Warn("ride analysis: updating the session's training load failed", "rider", rider, "provider", sess.Provider, "session", sess.ID, "err", err)
			continue
		}

		loadChanged = true
		analyzedCount[sess.Provider]++
	}

	if loadChanged {
		if err := s.Training.RecomputeFitnessSnapshots(ctx, rider); err != nil {
			return nil, err
		}
	}

	if len(candidates) > 0 {
		s.logger().Info("ride analysis: sync complete", "rider", rider, "analyzed", analyzedCount, "downloadFailures", downloadFailed)
	}

	return notes, nil
}

// plannedFor picks the planned workout a ride is scored against. A link the
// rider made by hand wins outright, "" included ("not a planned session").
// Otherwise it is rideanalysis.MatchPlanned over the workouts no other ride
// has been linked to by hand, so one hand-made link cannot leave a second
// ride auto-matched to the same session.
func plannedFor(sess workout.CompletedSession, rideSeconds float64, planned []workout.Workout, links map[string]string) *workout.Workout {
	if id, ok := links[sess.ID]; ok {
		if id == "" {
			return nil
		}
		for i := range planned {
			if planned[i].ID == id {
				return &planned[i]
			}
		}
		// The workout is gone (DeleteWorkout clears its links, so this is a
		// race at most): fall back to the automatic match.
	}
	claimed := map[string]bool{}
	for sessionID, id := range links {
		if sessionID != sess.ID && id != "" {
			claimed[id] = true
		}
	}
	candidates := planned
	if len(claimed) > 0 {
		candidates = make([]workout.Workout, 0, len(planned))
		for _, w := range planned {
			if !claimed[w.ID] {
				candidates = append(candidates, w)
			}
		}
	}
	return rideanalysis.MatchPlanned(sess.Date, sess.Sport, rideSeconds, candidates)
}

// fetchSessionFIT downloads one session's FIT file, choosing the provider id
// or URL to use exactly as sessionFITSource's own doc comment describes: the
// in-memory hint from this sync's own provider list when there is one,
// otherwise completed_sessions.external_id for Garmin (always recoverable)
// or nothing at all for Wahoo (no file URL survives past one sync).
func (s *Server) fetchSessionFIT(
	ctx context.Context,
	sess workout.CompletedSession,
	src sessionFITSource,
	hasSource bool,
	consumer GarminConsumer,
	garminSession garmin.Session,
	garminConnected bool,
	wahooToken string,
	wahooConnected bool,
) ([]byte, error) {
	switch sess.Provider {
	case garminProvider:
		if s.Garmin == nil || !garminConnected {
			return nil, errNoGarminSession
		}
		activityID := sess.ExternalID
		if hasSource && src.garminActivityID != "" {
			activityID = src.garminActivityID
		}
		return s.Garmin.ActivityFIT(ctx, consumer, garminSession, activityID)
	case wahooProvider:
		if s.Wahoo == nil || !wahooConnected {
			return nil, errNoWahooSession
		}
		fileURL := ""
		if hasSource {
			fileURL = src.wahooFileURL
		}
		if fileURL == "" {
			// Confirmed against ListWorkouts/UpsertSession: Wahoo's file URL
			// is never stored in completed_sessions, only the workout id
			// (external_id) — an old session genuinely has no FIT source
			// left to fetch, not a transient failure.
			return nil, errNoWahooFileURL
		}
		return s.Wahoo.WorkoutFIT(ctx, fileURL)
	default:
		return nil, errUnknownProvider
	}
}

// powerCurveDTO converts Analyze's int-keyed power curve to the
// string-keyed shape workout.SessionAnalysis stores — JSON object keys must
// be strings, and PowerCurve's own map[int]float64 is keyed by window length
// in seconds.
func powerCurveDTO(curve map[int]float64) map[string]float64 {
	if len(curve) == 0 {
		return nil
	}
	out := make(map[string]float64, len(curve))
	for seconds, watts := range curve {
		out[strconv.Itoa(seconds)] = watts
	}
	return out
}

// applyProgressionForAnalysis moves the rider's progression level for the
// analysed ride's zone, following the "levels move after every analysed
// ride" table in docs/superpowers/specs/2026-09-27-progression-levels-design.md.
// Only for a ride that matched a planned workout (matched != nil) whose zone
// is one of the structured ones and whose level is real (> 0) — an
// unplanned ride, or one matched to an endurance workout, has no ladder rung
// to measure against and moves nothing.
//
// feel is always 0 here: this is the first, automatic application from a
// fresh sync, before the rider has had any chance to rate how the ride
// felt. PUT /api/training/sessions/{id}/feel (handleSetSessionFeel,
// progression.go) is what recomputes with a real feel later, replacing —
// not stacking onto — the delta this call stores via SetAnalysisFeel.
func (s *Server) applyProgressionForAnalysis(ctx context.Context, rider string, profile workout.RiderProfile, matched *workout.Workout, sessionID, outcome string) error {
	if matched == nil || !workout.IsStructuredZone(matched.Zone) || matched.Level <= 0 {
		return nil
	}

	levels, err := s.levelsFor(ctx, rider, profile, matched.Sport)
	if err != nil {
		return err
	}
	cur := levels[string(matched.Zone)]

	delta := progression.Delta(cur, matched.Level, progression.Outcome(outcome), 0)
	newLevel := progression.Apply(cur, delta)
	reason := progression.Reason(matched.Name, string(matched.Zone), matched.Level, cur, newLevel, progression.Outcome(outcome))

	// What is stored is the change Apply actually produced — newLevel minus
	// cur, rounded to the same one decimal every stored level uses — not
	// Delta's raw, unclamped result. Apply clamps to [1.0, 10.0] and rounds;
	// storing the raw delta would let a later re-rate's cur_without :=
	// level - storedDelta land somewhere Apply never actually put the rider
	// (e.g. cur 9.9, workoutLevel 10.0, nailed: Delta gives 0.4, but Apply
	// clamps 10.3 down to 10.0 — the real change was only +0.1).
	applied := roundLevelDelta(newLevel - cur)

	if err := s.Training.SaveLevel(ctx, workout.ProgressionLevel{
		Rider: rider, Sport: matched.Sport, Zone: matched.Zone, Level: newLevel, Reason: reason,
	}); err != nil {
		return err
	}
	return s.Training.SetAnalysisFeel(ctx, sessionID, 0, applied)
}

// analysisStepsDTO mirrors rideanalysis.StepResult into
// workout.AnalysisStep — the same by-hand mirroring SessionAnalysis's own
// doc comment describes, since internal/workout deliberately does not
// import internal/rideanalysis.
func analysisStepsDTO(steps []rideanalysis.StepResult) []workout.AnalysisStep {
	if len(steps) == 0 {
		return nil
	}
	out := make([]workout.AnalysisStep, 0, len(steps))
	for _, st := range steps {
		out = append(out, workout.AnalysisStep{
			Index:       st.Index,
			Name:        st.Name,
			Target:      st.Target,
			Low:         st.Low,
			High:        st.High,
			Actual:      st.Actual,
			Result:      st.Result,
			InTargetPct: st.InTargetPct,
			Hard:        st.Hard,
		})
	}
	return out
}
