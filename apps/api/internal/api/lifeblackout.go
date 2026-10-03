package api

import (
	"context"
	"fmt"

	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Life events (internal/lifeevents) are enforced here, at every place a plan is
// made or adapted. The blackout is the one fact they all consult: a session a
// life event removed has to stay removed, and nothing may be put back on a day
// the rider is away.

// lifeEventLookbackDays is how far back events are still read. The longest
// return ramp runs 14 days after an event ends, so an event that ended two
// weeks ago still decides what its aftermath looks like.
const lifeEventLookbackDays = 15

// indoorAvailable is whether indoor conversion exists on this deployment: the
// gym option of a trip is built on it.
const indoorAvailable = true

var lifeCaps = lifeevents.Capabilities{Indoor: indoorAvailable}

// lifeEventsFor is the rider's events that still matter today: those that
// end within lifeEventLookbackDays or later.
func (s *Server) lifeEventsFor(ctx context.Context, rider string) ([]lifeevents.Event, error) {
	if s.Training == nil {
		return nil, nil
	}
	from := s.now().AddDate(0, 0, -lifeEventLookbackDays).Format(dateLayout)
	return s.Training.ListLifeEvents(ctx, rider, from)
}

// blackoutFor is the set of dates the rider's life events cover. An error is
// returned, not swallowed: a plan built without it could put a removed session
// straight back.
func (s *Server) blackoutFor(ctx context.Context, rider string) (map[string]bool, error) {
	events, err := s.lifeEventsFor(ctx, rider)
	if err != nil {
		return nil, fmt.Errorf("reading life events: %w", err)
	}
	return lifeevents.Blackout(events), nil
}

// applyLifeUpdate writes the content change of one ease, shorten or move. An
// indoor session stays on the trainer: the same hook every other automatic
// change uses converts the replacement again.
func (s *Server) applyLifeUpdate(ctx context.Context, wk workout.Workout, profile workout.RiderProfile, c lifeevents.Change) error {
	if c.Update == nil {
		return fmt.Errorf("change %s carries nothing to write", c.ID)
	}
	req := *c.Update
	if c.ReKeepIndoor && req.Zone != nil {
		s.keepIndoor(&req, wk, profile, *req.Zone)
	}
	_, err := s.Training.UpdateWorkout(ctx, wk.ID, req)
	return err
}

// applyReturnRamp is the one place a life event changes the plan without a
// fresh confirmation: weeks filled after an event was made (the season is
// planned ahead and refreshed as weeks come into reach) get the same return
// ramp the preview stated, here, where every other automatic rule runs. It
// only touches generated, unadjusted sessions, writes the adjusted marker, and
// skips any workout this pass has already changed, so a session is changed
// automatically at most once. applied is that set, and is added to.
func (s *Server) applyReturnRamp(ctx context.Context, rider string, workouts []workout.Workout, profile workout.RiderProfile, events []lifeevents.Event, applied map[string]bool) {
	if len(events) == 0 {
		return
	}
	now := s.now()
	ridden, err := s.riddenToday(ctx, rider, now.Format(dateLayout), workouts)
	if err != nil {
		s.logger().Warn("adapt: could not tell what was ridden today, the return ramp was skipped", "rider", rider, "err", err)
		return
	}
	diff := lifeevents.Preview(lifeevents.Input{
		Events: events, Previous: events, RampAll: true,
		Workouts: workouts, Ridden: ridden, Profile: profile, Now: now, Caps: lifeCaps,
	})
	byID := make(map[string]workout.Workout, len(workouts))
	for _, w := range workouts {
		byID[w.ID] = w
	}
	for _, c := range diff.Changes {
		if (c.Op != lifeevents.OpEase && c.Op != lifeevents.OpShort) || applied[c.WorkoutID] {
			continue
		}
		wk := byID[c.WorkoutID]
		if err := s.applyLifeUpdate(ctx, wk, profile, c); err != nil {
			s.logger().Warn("adapt: could not apply the return ramp to a workout", "workout", wk.ID, "rider", rider, "err", err)
			continue
		}
		applied[c.WorkoutID] = true
		if c.Ramp != nil {
			rec := why.NewRecord(why.IllnessRamp, c.Reason, why.IllnessRampInputs{
				Kind: c.Ramp.Kind, Option: c.Ramp.Option, EndDate: c.Ramp.End,
				Day: c.Ramp.Day, EasyDays: c.Ramp.EasyDays, UntilDay: c.Ramp.UntilDay,
			})
			s.recordAdjustment(ctx, rider, wk.ID, rec, wk.Indoor)
		}
		// The rule id and the kind of event, never the reason or a date next to a name.
		s.logger().Info("workout adapted automatically", "workout", wk.ID, "rider", rider, "change", "return ramp", "rule", string(why.IllnessRamp))
	}
}
