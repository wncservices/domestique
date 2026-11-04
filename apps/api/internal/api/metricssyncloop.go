package api

import (
	"context"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/syncschedule"
)

// metricsSyncLastRunFlag is a row in settings' plain flags table used only
// for its updated_at: when the training-metrics sync last finished. It is a
// marker, not a flag — its enabled column is always true and means nothing.
//
// It is also what coordinates replicas: every pod's timer fires at the same
// slot, and the pass re-reads this timestamp under the advisory lock, so the
// first pod records the run and the rest see it and stand down. In memory it
// would say "never synced" after every deploy and re-sync every time a pod
// started.
//
// A dedicated job_runs table would be tidier, but it is a new table, schema,
// and a two-engine test for one timestamp; the flags table already has the
// timestamp column, the idempotent schema and the engine tests.
const metricsSyncLastRunFlag = "metrics_sync_last_run"

// syncSchedule is the configured morning/evening slots. A nil Config means
// the defaults, the same as an empty training block.
func (s *Server) syncSchedule() (syncschedule.Schedule, error) {
	var t config.TrainingConfig
	if s.Config != nil {
		t = s.Config.Training
	}
	return t.Schedule()
}

// lastMetricsSyncAt is when the metrics sync last finished; zero if never.
func (s *Server) lastMetricsSyncAt() time.Time {
	if s.Settings != nil {
		if meta, err := s.Settings.DescribeFlag(metricsSyncLastRunFlag); err == nil {
			return meta.UpdatedAt
		}
	}
	return s.lastMetricsSync
}

func (s *Server) markMetricsSynced(at time.Time) {
	s.lastMetricsSync = at
	if s.Settings == nil {
		return
	}
	if err := s.Settings.SetFlagAt(metricsSyncLastRunFlag, true, "scheduler", at); err != nil {
		// Warn, not Error: the sync itself succeeded. The cost is one
		// redundant catch-up sync after the next restart.
		s.logger().Warn("metrics sync: could not record the last run", "err", err)
	}
}

// runMetricsPass is one metrics sync, serialised within the process. With
// onlyIfMissed it re-checks under the mutex whether a slot has passed since
// the last run, so a 30-minute tick and the start-up catch-up that race each
// other on boot sync once, not twice. Reports whether it synced.
//
// The caller holds the advisory lock (or, for AutoScheduleTick, the same one
// it already took for the workout-changing steps).
func (s *Server) runMetricsPass(ctx context.Context, onlyIfMissed bool) bool {
	if !onlyIfMissed {
		return s.runMetricsPassWith(ctx, nil)
	}
	sched, err := s.syncSchedule()
	if err != nil {
		return false
	}
	return s.runMetricsPassWith(ctx, &sched)
}

// runMetricsPassWith is runMetricsPass with the schedule already parsed (the
// loop parses it once); nil means run unconditionally.
func (s *Server) runMetricsPassWith(ctx context.Context, sched *syncschedule.Schedule) bool {
	if s.Training == nil || s.Links == nil {
		return false
	}
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()

	if sched != nil && !sched.Missed(s.lastMetricsSyncAt(), s.now()) {
		return false
	}

	riders, failed := s.autoSyncTrainingMetrics(ctx)

	// Cut short by shutdown: not everyone was synced, so it is not a
	// completed run. Left unrecorded, the next start catches up.
	if ctx.Err() != nil {
		return false
	}

	// After wellness, so the first plan-page load after a sync finds the
	// forecast cached. Best effort and never part of whether the sync counts
	// as done: a forecast outage must not make the pass look failed.
	s.warmWeather(ctx)
	// Everyone failing is left unrecorded too, so a restart retries once
	// instead of waiting for the next slot — and it is a Warn, not the Info
	// of a healthy run. Counts only: a rider's name next to anything about
	// their sleep or heart rate is health data in a log line.
	if riders > 0 && failed == riders {
		s.logger().Warn("metrics sync: every rider failed", "riders", riders, "failed", failed)
		return true
	}

	s.markMetricsSynced(s.now())
	if riders > 0 {
		s.logger().Info("metrics sync finished", "riders", riders, "failed", failed)
	}
	return true
}

// SyncTrainingMetrics pulls every connected rider's activities and wellness
// now, whatever the auto-schedule flag says. Exported for tests and for the
// fixed-time loop. Shares AutoScheduleTick's advisory lock, so two replicas —
// or a tick and a scheduled run — never sync at once; if a tick holds it, that
// tick is syncing anyway.
func (s *Server) SyncTrainingMetrics(ctx context.Context) {
	withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
		s.runMetricsPass(ctx, false)
	})
}

// RunMetricsSyncIfMissed syncs only if a scheduled slot has passed since the
// last recorded run, re-checked under the lock. It is both the start-up
// catch-up (the process was down at a slot) and the timer path (see
// RunMetricsSyncLoop): the shared timestamp is what makes replicas whose
// timers fire together sync once. Reports whether it synced.
func (s *Server) RunMetricsSyncIfMissed(ctx context.Context) bool {
	sched, err := s.syncSchedule()
	if err != nil {
		return false
	}
	return s.runMissedWith(ctx, sched)
}

func (s *Server) runMissedWith(ctx context.Context, sched syncschedule.Schedule) bool {
	ran := false
	withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
		ran = s.runMetricsPassWith(ctx, &sched)
	})
	return ran
}

// RunMetricsSyncLoop syncs training metrics at the configured local times
// (default 06:30 and 21:00 Europe/Brussels), independent of auto-schedule.
// Morning catches last night's sleep, HRV and readiness before the day's
// readiness check; evening catches the day's ride. Runs until ctx is
// cancelled; start it once, in its own goroutine, from main.go.
func (s *Server) RunMetricsSyncLoop(ctx context.Context) {
	sched, err := s.syncSchedule()
	if err != nil {
		// config.Validate already rejected this at startup; reaching here
		// means a Server built in code with a bad config.
		s.logger().Error("metrics sync: bad schedule, fixed-time sync disabled", "err", err)
		return
	}

	s.runMissedWith(ctx, sched)

	for {
		now := s.now()
		next := sched.Next(now)
		if next.IsZero() {
			// Cannot happen for a schedule Parse accepted, but a zero
			// duration here would spin this loop hot, so refuse instead.
			s.logger().Error("metrics sync: no next slot could be computed, fixed-time sync disabled")
			return
		}
		// A second of slack so a timer that wakes a hair early does not
		// compute the same slot again and sync twice.
		timer := time.NewTimer(next.Sub(now) + time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			// Not an unconditional sync: every replica's timer fires at this
			// slot, and only the one that finds it still unrecorded, under
			// the lock, runs it.
			s.runMissedWith(ctx, sched)
		}
	}
}
