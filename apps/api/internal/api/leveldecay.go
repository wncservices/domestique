package api

import (
	"context"
	"fmt"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/progression"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// decayLevels is the weekly slip of progression levels in zones the rider has
// left alone: once per rider per ISO week, in the day's first sync slot, a
// zone whose last matched ride is more than 21 days back (or that was never
// ridden) loses 0.1, never going below the level the rider started on.
//
// It runs inside AutoScheduleTick, under the scheduling lock, after the season
// is planned and reoptimise, and before AdaptWorkouts, so a session built or
// eased in the same pass already sees the lowered level. It never fails the
// tick, and logs counts only: a level next to a rider would be health data.
func (s *Server) decayLevels(ctx context.Context) {
	if s.Training == nil || !s.inMorningSlot(s.now()) {
		return
	}
	goals, err := s.Training.ListAllGoals(ctx)
	if err != nil {
		s.logger().Warn("level decay: listing goals failed", "err", err)
		return
	}
	seen := map[string]bool{}
	decayed, failed, unseen := 0, 0, 0
	for _, g := range goals {
		if seen[g.Rider] || ctx.Err() != nil {
			continue
		}
		seen[g.Rider] = true
		// Rides the app has not seen must not read as zones left alone: same
		// rule as the load ledger. The week stays unclaimed for a later tick.
		if s.metricsFailedRider(g.Rider) || s.garminSessionExpired(g.Rider) {
			unseen++
			continue
		}
		n, err := s.decayRiderLevels(ctx, g.Rider)
		if err != nil {
			failed++
			continue
		}
		decayed += n
	}
	if decayed > 0 {
		s.logger().Info("level decay: levels lowered", "levels", decayed)
	}
	if failed > 0 {
		s.logger().Warn("level decay: riders skipped, their levels could not be read", "riders", failed)
	}
	if unseen > 0 {
		s.logger().Warn("level decay: riders skipped, their rides could not be read", "riders", unseen)
	}
}

// decayRiderLevels returns how many levels it lowered. The week is claimed only
// after every read succeeded, so a transient error costs a tick, not the week.
func (s *Server) decayRiderLevels(ctx context.Context, rider string) (int, error) {
	profile, _, err := s.Training.GetProfile(ctx, rider)
	if err != nil {
		return 0, err
	}
	levels, err := s.Training.ListLevels(ctx, rider)
	if err != nil {
		return 0, err
	}
	trained := map[model.Sport]map[string]string{}
	for _, sport := range []model.Sport{model.SportCycling, model.SportRunning} {
		m, err := s.Training.LastTrainedByZone(ctx, rider, string(sport))
		if err != nil {
			return 0, err
		}
		trained[sport] = m
	}

	now := s.localNow()
	claimed, err := s.Training.ClaimDecayWeek(ctx, rider, periodization.MondayOf(now).Format(dateLayout))
	if err != nil || !claimed {
		return 0, err
	}
	today := now.Format(dateLayout)

	start := map[model.Sport]map[string]float64{}
	lowered := 0
	for _, l := range levels {
		if _, ok := start[l.Sport]; !ok {
			start[l.Sport] = map[string]float64{}
			for _, in := range progression.Initial(profile.ExperienceLevel, l.Sport) {
				start[l.Sport][in.Zone] = in.Value
			}
		}
		floor, ok := start[l.Sport][string(l.Zone)]
		if !ok {
			continue
		}
		last := trained[l.Sport][string(l.Zone)]
		to, moved := progression.Decay(l.Level, floor, last, today)
		if !moved {
			continue
		}
		reason := fmt.Sprintf("Not trained lately — %s %.1f → %.1f", l.Zone, l.Level, to)
		from := l.Level
		if err := s.Training.SaveLevel(ctx, workout.ProgressionLevel{
			Rider: rider, Sport: l.Sport, Zone: l.Zone, Level: to, Reason: reason,
		}); err != nil {
			// Counted, not named: the week is claimed, so this zone waits for next week.
			s.logger().Warn("level decay: a level could not be saved")
			continue
		}
		s.recordSubjectAdjustment(ctx, rider, workout.SubjectLevel, string(l.Sport)+":"+string(l.Zone), why.NewRecord(
			why.LevelDecay, reason, why.LevelDecayInputs{From: from, To: to, LastTrained: last}))
		lowered++
	}
	return lowered, nil
}
