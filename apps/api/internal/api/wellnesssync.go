package api

import (
	"context"
	"sync"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// wellnessBackfillDays is how far back the first sync for a rider with no
// daily_wellness rows yet reaches — enough for readiness's own resting-HR
// baseline, which needs at least 7 readings out of the last 28 days (see
// docs/superpowers/specs/2026-09-28-readiness-design.md).
const wellnessBackfillDays = 28

// wellnessBackfillCooldown bounds how often a rider whose daily_wellness
// stays empty is even considered for the 28-day backfill — same shape and
// reasoning as biometricsRefreshEvery: these are undocumented endpoints, and
// a rider whose rows never end up saved (an expired session, a display name
// Garmin no longer recognises, or simply a persistent write failure) must
// not turn into 27 extra Garmin calls on every single sync tick, forever.
// The mark is recorded whether or not the backfill that follows actually
// runs — see wellnessBackfillCache's own doc comment.
const wellnessBackfillCooldown = 24 * time.Hour

// wellnessBackfillCache remembers, per rider, when a backfill was last
// *considered* — not only when it last succeeded. Recording the attempt
// regardless of outcome is the point: without it, a rider stuck at zero
// rows (whatever the reason) would be re-evaluated for a fresh 28-day
// backfill on every sync tick, which is exactly the bug this cache exists
// to close. In-memory only, same as biometricsCache — a restart costs one
// extra consideration per rider, nothing more.
type wellnessBackfillCache struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// attemptedRecently reports whether rider was last considered for a
// backfill less than wellnessBackfillCooldown ago.
func (c *wellnessBackfillCache) attemptedRecently(rider string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.m[rider]
	return ok && now.Sub(at) < wellnessBackfillCooldown
}

// recordAttempt marks rider as considered for a backfill at now.
func (c *wellnessBackfillCache) recordAttempt(rider string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]time.Time{}
	}
	c.m[rider] = now
}

// syncRiderWellness fetches today's and yesterday's Garmin wellness (HRV,
// sleep, Training Readiness, resting HR) for a rider with Garmin connected
// and saves both — every sync, unconditionally. The very first time a rider
// has no daily_wellness rows at all, it also backfills the previous
// wellnessBackfillDays days in the same pass, subject to two guards:
//
//   - it is skipped outright in a pass where both today's and yesterday's
//     *fetches* failed — an expired session or an unrecognised display name
//     fails every date the same way, so 27 more calls would only confirm
//     what is already known; and
//   - it is attempted at most once per rider per wellnessBackfillCooldown
//     regardless of outcome (wellnessBackfillCache), so a rider whose rows
//     never end up saved for some other reason still cannot re-trigger a
//     full backfill on every tick.
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

	todayOK := s.fetchAndSaveWellness(ctx, rider, consumer, session, s.now())
	yesterdayOK := s.fetchAndSaveWellness(ctx, rider, consumer, session, s.now().AddDate(0, 0, -1))

	if len(existing) != 0 {
		return
	}
	if s.wellnessBackfill.attemptedRecently(rider, s.now()) {
		return
	}
	s.wellnessBackfill.recordAttempt(rider, s.now())

	if !todayOK && !yesterdayOK {
		return
	}

	for daysAgo := 2; daysAgo <= wellnessBackfillDays; daysAgo++ {
		s.fetchAndSaveWellness(ctx, rider, consumer, session, s.now().AddDate(0, 0, -daysAgo))
	}
}

// fetchAndSaveWellness fetches one date's Garmin wellness and saves it,
// warning (never failing) on either a fetch or a save problem. Its bool
// result is strictly about the *fetch* — a save failure is still logged but
// does not count as "this date failed" for syncRiderWellness's own
// both-today-and-yesterday-failed check, which is about whether reaching
// further back is likely to succeed, not about storage.
func (s *Server) fetchAndSaveWellness(ctx context.Context, rider string, consumer GarminConsumer, session garmin.Session, date time.Time) bool {
	w, err := s.Garmin.Wellness(ctx, consumer, session, date)
	if err != nil {
		s.logger().Warn("wellness sync: garmin lookup failed", "rider", rider, "err", err)
		return false
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
	return true
}
