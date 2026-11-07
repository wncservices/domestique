package api

import (
	"context"
	"errors"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/rideanalysis"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// hrHistogramBackfillPerProvider bounds the FIT downloads one metrics pass
// spends on rides analysed before heart-rate histograms existed, so the
// backfill trickles in behind the ordinary analysis instead of competing with it.
const hrHistogramBackfillPerProvider = 10

// hrHistogramBackfillScan is how many unfilled rides one pass looks at, so
// rides of a provider that cannot be fetched do not hide the others behind them.
const hrHistogramBackfillScan = 50

// backfillHRHistograms gives older synced rides their heart-rate histogram. It
// is best-effort: a failure here never fails the metrics pass, and it writes
// the histogram column only (SetHRHistogram), never a rating or the survey.
//
// A download error leaves the ride unfilled to be retried next pass. A file
// that cannot be decoded (or crashes the decoder), or that has no samples,
// stores the empty histogram, because it would fail the same way again. So
// does a Wahoo ride with no file URL left: nothing can ever fetch it. A
// provider the rider is not connected to is skipped without counting, as it
// is not the ride's fault.
//
// missing is read by the caller before this pass's own analysis runs
// (listMissingHRHistograms), so a ride analysed a moment ago, whose file was
// just tried, is not fetched a second time in the same pass.
func (s *Server) backfillHRHistograms(
	ctx context.Context,
	rider string,
	missing []workout.CompletedSession,
	sources map[string]sessionFITSource,
	consumer GarminConsumer,
	garminSession garmin.Session,
	garminConnected bool,
	wahooToken string,
	wahooConnected bool,
) {
	attempts := map[string]int{}
	filled, failed := 0, 0
	for _, sess := range missing {
		if attempts[sess.Provider] >= hrHistogramBackfillPerProvider {
			continue
		}
		src, hasSource := sources[sess.ID]
		raw, err := s.fetchSessionFIT(ctx, sess, src, hasSource, consumer, garminSession, garminConnected, wahooToken, wahooConnected)
		switch {
		case errors.Is(err, errNoGarminSession), errors.Is(err, errNoWahooSession):
			continue
		case errors.Is(err, errNoWahooFileURL), errors.Is(err, errUnknownProvider):
			// Permanent: store the empty histogram so it is not scanned again.
			attempts[sess.Provider]++
			if s.Training.SetHRHistogram(ctx, sess.ID, workout.HRHistogram{}) != nil {
				failed++
			}
			continue
		case err != nil:
			attempts[sess.Provider]++
			failed++
			continue
		}
		attempts[sess.Provider]++
		var hist workout.HRHistogram
		if act, derr := s.guardedDecode(raw, rider, sess.Provider, sess.ID); derr == nil && act != nil {
			hist = rideanalysis.HRHistogramOf(rideanalysis.Resample(act.Records))
		}
		if err := s.Training.SetHRHistogram(ctx, sess.ID, hist); err != nil {
			failed++
			continue
		}
		filled++
	}
	if failed > 0 {
		s.logger().Warn("hr histogram backfill: some rides could not be filled", "rider", rider, "failed", failed)
	}
	if filled > 0 {
		s.logger().Info("hr histogram backfill", "rider", rider, "filled", filled)
	}
}

// listMissingHRHistograms is the backfill's worklist; a failure to read it
// only skips the backfill.
func (s *Server) listMissingHRHistograms(ctx context.Context, rider string) []workout.CompletedSession {
	missing, err := s.Training.SessionsMissingHRHistogram(ctx, rider, hrHistogramBackfillScan)
	if err != nil {
		s.logger().Warn("hr histogram backfill: could not list rides", "rider", rider, "err", err)
		return nil
	}
	return missing
}
