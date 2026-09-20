package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
)

// FlagAutoSchedule is settings.Store's flag name for automated training: pull
// every connected rider's completed sessions, then schedule their plan week.
// See FlagAutoSync's own doc comment for why this lives in the plain flags
// table rather than the encrypted settings one.
// Off by default, the same reasoning FlagAutoSync's own default carries:
// a rider who has not yet filled in a fitness profile or reviewed their
// goal's plan should not find workouts appearing on their calendar before
// they have looked at either.
const FlagAutoSchedule = "auto_schedule"

// autoScheduleInterval mirrors autoImportInterval's own reasoning: nothing
// pushes a webhook when a new calendar week starts, so a periodic tick is
// the only way a rider's plan turns into real, dated workout rows without
// them visiting the training page and clicking "Schedule this week's
// workouts" by hand. Half-hourly, not daily, so a goal or profile saved
// today is scheduled the same day rather than tomorrow — the same
// immediacy autoImportInterval already chose for the same reason.
const autoScheduleInterval = 30 * time.Minute

// autoScheduleLockKey is its own advisory-lock key, deliberately separate
// from backgroundSyncLockKey: scheduling a workout only ever touches the
// training tables (goals, workouts, completed_sessions), never
// routes/accounts/state, so there is no reason for it to serialize
// against — or sit waiting behind — a route import-and-push running on
// another pod at the same moment. Two independent locks for two
// independent concerns, the same reasoning AuthActionLimiter's own doc
// comment gives for not sharing ConnectLimiter's budget.
const autoScheduleLockKey = "domestique:auto-schedule"

type autoScheduleDTO struct {
	Enabled bool `json:"enabled"`
	// CanManage is whether *this caller* may change it — same shape
	// autoSyncDTO.CanManage already uses. False hides the toggle rather
	// than offering a 403.
	CanManage bool   `json:"canManage"`
	UpdatedBy string `json:"updatedBy,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func (s *Server) autoScheduleDTOFor(r *http.Request) (autoScheduleDTO, error) {
	enabled, err := s.Settings.Flag(FlagAutoSchedule)
	if err != nil {
		return autoScheduleDTO{}, err
	}
	dto := autoScheduleDTO{
		Enabled:   enabled,
		CanManage: auth.FromContext(r.Context()).Role.Can(auth.PermManageSettings),
	}
	if meta, err := s.Settings.DescribeFlag(FlagAutoSchedule); err == nil {
		dto.UpdatedBy = meta.UpdatedBy
		dto.UpdatedAt = meta.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return dto, nil
}

// handleAutoSchedule reports whether auto-schedule is on.
func (s *Server) handleAutoSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) {
		return
	}
	dto, err := s.autoScheduleDTOFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleSetAutoSchedule flips it. Admin-only, same reasoning
// handleSetAutoSync's own doc comment gives: this changes every rider's
// scheduling behavior at once, not just the caller's own.
func (s *Server) handleSetAutoSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageSettings) {
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	who := auth.FromContext(r.Context()).User
	if err := s.Settings.SetFlag(FlagAutoSchedule, body.Enabled, who); err != nil {
		s.fail(w, err)
		return
	}
	s.logger().Info("auto-schedule setting changed", "enabled", body.Enabled, "by", who)

	dto, err := s.autoScheduleDTOFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// RunAutoScheduleLoop schedules every rider's current plan week on
// autoScheduleInterval — the same run-once-immediately-then-tick shape as
// RunAutoImportLoop, meant to be started once, in its own goroutine, from
// main.go. Runs until ctx is cancelled (server shutdown).
func (s *Server) RunAutoScheduleLoop(ctx context.Context) {
	// Run once immediately rather than waiting a full interval for the
	// first pass — see RunAutoImportLoop's own reasoning.
	s.AutoScheduleTick(ctx)

	ticker := time.NewTicker(autoScheduleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.AutoScheduleTick(ctx)
		}
	}
}

// AutoScheduleTick is one pass — everything RunAutoScheduleLoop does on a
// single tick. Exported so a test can drive it directly instead of
// waiting on a real ticker.
//
// Every goal, across every rider, is scheduled the same
// way handleGoalSchedule schedules one on a rider's own click — same
// scheduleGoal call, same idempotency (a date already covered for a goal
// is left alone), so a rider who has never opened the training page since
// setting a goal and a profile still gets this week's workouts on their
// calendar, and a rider who already clicked "Schedule this week's
// workouts" themselves sees no duplicates when this tick reaches the same
// goal.
func (s *Server) AutoScheduleTick(ctx context.Context) {
	if s.Settings == nil || s.Training == nil {
		return
	}
	enabled, err := s.Settings.Flag(FlagAutoSchedule)
	if err != nil {
		s.logger().Warn("auto-schedule: could not read the flag", "err", err)
		return
	}
	if !enabled {
		return
	}

	withDBLock(ctx, s.dbConn(), autoScheduleLockKey, func() {
		// History first, so the week below is planned from what the rider
		// has actually just trained, not from whenever they last clicked
		// "Sync now" — see autoSyncTrainingMetrics. Runs for every connected
		// rider, goal or not: the FTP and resting-HR estimates it fills in
		// are useful before anyone has set a goal.
		s.autoSyncTrainingMetrics(ctx)

		goals, err := s.Training.ListAllGoals(ctx)
		if err != nil {
			s.logger().Error("auto-schedule: listing goals failed", "err", err)
			return
		}

		for _, g := range goals {
			created, skipped, err := s.scheduleGoal(ctx, g)
			if err != nil {
				// ErrEventInThePast is not a real problem — a rider's own
				// goal simply outlived its event and nobody has deleted it
				// yet, no different from AGENTS.md's "one bad route never
				// aborts a run": this goal is skipped, every other rider's
				// goal still gets scheduled. A goal with no date is not an
				// error either — it gets a rolling general-fitness plan —
				// but ErrNoEventDate stays excluded from the noisy case
				// rather than asserted against, since a defensive check that
				// never fires is cheaper than one that panics if it ever
				// does.
				if err != periodization.ErrNoEventDate && err != periodization.ErrEventInThePast {
					s.logger().Warn("auto-schedule failed for a goal", "goal", g.ID, "rider", g.Rider, "err", err)
				}
				continue
			}
			if len(created) > 0 {
				s.logger().Info("auto-scheduled workouts", "goal", g.ID, "rider", g.Rider, "created", len(created), "skipped", skipped)
			}
		}

		// After scheduling, so this week exists to be adapted, and before
		// the push, so an adjustment reaches the rider's watch in the same
		// pass that makes it rather than half an hour later.
		s.AdaptWorkouts(ctx)
		s.autoPushWorkouts(ctx)
	})
}
