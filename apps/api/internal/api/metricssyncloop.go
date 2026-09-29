package api

import (
	"context"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/syncschedule"
)

// metricsSyncLastRunFlag is a row in settings' plain flags table used only
// for its updated_at: when the training-metrics sync last finished. It is the
// one piece of state the missed-run rule needs to survive a restart — in
// memory it would say "never synced" after every deploy and re-sync every
// time a pod started. Its enabled column is always true and means nothing.
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
	if s.Training == nil || s.Links == nil {
		return false
	}
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()

	if onlyIfMissed {
		sched, err := s.syncSchedule()
		if err != nil || !sched.Missed(s.lastMetricsSyncAt(), s.now()) {
			return false
		}
	}

	riders, failed := s.autoSyncTrainingMetrics(ctx)
	s.markMetricsSynced(s.now())
	if riders > 0 {
		// Counts only. A rider's name next to anything about their sleep or
		// heart rate is health data in a log line.
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

// RunMetricsSyncIfMissed is the start-up catch-up: if the process was down at
// a scheduled slot, sync once now. Reports whether it did.
func (s *Server) RunMetricsSyncIfMissed(ctx context.Context) bool {
	ran := false
	withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
		ran = s.runMetricsPass(ctx, true)
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

	s.RunMetricsSyncIfMissed(ctx)

	for {
		now := s.now()
		// A second of slack so a timer that wakes a hair early does not
		// compute the same slot again and sync twice.
		timer := time.NewTimer(sched.Next(now).Sub(now) + time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.SyncTrainingMetrics(ctx)
		}
	}
}
