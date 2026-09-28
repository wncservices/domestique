package api

import (
	"context"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// wellnessBackfillDays is how far back the first sync for a rider with no
// daily_wellness rows yet reaches — enough for readiness's own resting-HR
// baseline, which needs at least 7 readings out of the last 28 days (see
// docs/superpowers/specs/2026-09-28-readiness-design.md).
const wellnessBackfillDays = 28

// syncRiderWellness fetches today's and yesterday's Garmin wellness (HRV,
// sleep, Training Readiness, resting HR) for a rider with Garmin connected
// and saves both. The very first time a rider has no daily_wellness rows at
// all, it also backfills the previous wellnessBackfillDays days in the same
// pass — capped to once per rider, ever, by checking for existing rows
// first, not by any per-sync flag.
//
// A Garmin failure, or any signal Garmin could not read (Wellness' own
// Partial field), is a Warn naming the rider and which signals — never
// values, per AGENTS.md's Security guardrails — and never fails the sync:
// the same "an optional enhancement to an otherwise-successful sync" shape
// garminBiometrics already uses.
func (s *Server) syncRiderWellness(ctx context.Context, rider string, consumer GarminConsumer, session garmin.Session) {
	if s.Garmin == nil || s.Training == nil {
		return
	}

	existing, err := s.Training.ListWellness(ctx, rider, "")
	if err != nil {
		s.logger().Warn("wellness sync: reading existing rows failed", "rider", rider, "err", err)
		return
	}

	dates := []time.Time{s.now(), s.now().AddDate(0, 0, -1)}
	if len(existing) == 0 {
		for daysAgo := 2; daysAgo <= wellnessBackfillDays; daysAgo++ {
			dates = append(dates, s.now().AddDate(0, 0, -daysAgo))
		}
	}

	for _, date := range dates {
		w, err := s.Garmin.Wellness(ctx, consumer, session, date)
		if err != nil {
			s.logger().Warn("wellness sync: garmin lookup failed", "rider", rider, "err", err)
			continue
		}
		if len(w.Partial) > 0 {
			s.logger().Warn("wellness sync: some signals could not be read", "rider", rider, "signals", w.Partial)
		}
		row := workout.DailyWellness{
			Rider: rider, Date: w.Date,
			HRVLastNight: w.HRVLastNight, HRVWeeklyAvg: w.HRVWeeklyAvg, HRVStatus: w.HRVStatus,
			SleepSeconds: w.SleepSeconds, SleepScore: w.SleepScore,
			ReadinessScore: w.ReadinessScore, ReadinessLevel: w.ReadinessLevel,
			RestingHR: w.RestingHR,
		}
		if err := s.Training.SaveWellness(ctx, row); err != nil {
			s.logger().Warn("wellness sync: saving a row failed", "rider", rider, "err", err)
		}
	}
}
