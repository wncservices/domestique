package api_test

import (
	"context"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A rolling plan is twelve weeks deep, so a route scheduled 14 weeks out sits in
// a week nothing has filled yet. The "new" ride it makes carries the goal id (so
// the day reads as taken), but it is the rider's own ride, not a plan session: it
// must not make the week look already filled, or that week is recorded as done
// and its plan sessions are never made.
func TestARoutedRideInAnUnfilledWeekDoesNotSuppressThatWeeksPlan(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, []string{"tue", "thu", "sat", "sun"}, 0)
	g := h.createGoal(t, `{"name":"Keep riding"}`)
	h.srv.WaitForBackground()

	thisMon, _ := weekBounds(h.now)
	far := thisMon.AddDate(0, 0, 7*14)
	if n := len(h.week(t, far)); n != 0 {
		t.Fatalf("fixture: week +14 has %d sessions, want none yet", n)
	}

	// What scheduling a route onto a day with nothing on it makes.
	ctx := context.Background()
	date := far.AddDate(0, 0, 1).Format("2006-01-02")
	ride, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "Endurance ride: Sunday loop", GoalID: g.ID, Date: date,
		Description: "Sized to Sunday loop.", Zone: workout.ZoneEndurance,
		Steps: []workout.WorkoutStep{{Name: "Ride", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 7200}},
	})
	if err != nil {
		t.Fatal(err)
	}
	slug, secs := "sunday-loop", 7200.0
	if _, err := h.store.UpdateWorkout(ctx, ride.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug, RouteSeconds: &secs}); err != nil {
		t.Fatal(err)
	}

	// Three weeks on, week +14 is inside the twelve-week window.
	h.now = h.now.AddDate(0, 0, 21)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)
	h.srv.WaitForBackground()

	plan := 0
	for _, wk := range h.week(t, far) {
		if wk.ID != ride.ID && wk.Description == scheduler.GeneratedDescription {
			plan++
		}
	}
	if plan == 0 {
		t.Errorf("week +14 holds no plan sessions beside the routed ride: %+v", h.week(t, far))
	}
	if got, err := h.store.GetWorkout(ctx, ride.ID); err != nil || got.RouteSlug != slug {
		t.Errorf("the routed ride was lost or unlinked: %+v (err %v)", got, err)
	}
}
