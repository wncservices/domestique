package api_test

import (
	"context"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The automatic pass clears its own copies of days ahead. A crew ride row is
// never one of them: its route reaches the head unit through the crew's own
// sync, so a copy recorded against it is not the automatic pass's to withdraw.
func TestAutoPushDoesNotWithdrawACrewRideRowsCopy(t *testing.T) {
	h := newCrewPlanHarness(t)
	ctx := context.Background()
	s := h.setup()
	future := h.seedRide(s.crewID, s.route, cpFriday, "wilant")
	if _, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"}); err != nil {
		t.Fatal(err)
	}
	h.mustGo("wilant", future.ID)
	row, ok := h.fixedRow("wilant", future.ID)
	if !ok {
		t.Fatal("no crew ride row for a ride the rider is going on")
	}

	h.garmin.remoteWorkouts = map[string]string{"remote-crew": row.Name}
	if err := h.training.SavePush(ctx, workout.Push{
		WorkoutID: row.ID, Provider: "garmin", RemoteID: "remote-crew",
		ScheduledDate: row.Date, ContentHash: workout.ContentHash(row), Origin: workout.PushOriginAuto,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{Rider: "wilant", AutoPushWorkouts: true, HoursPerAvailableDay: 1, AvailableDays: []string{"wed"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(ctx)

	if _, have, _ := h.training.GetPush(ctx, row.ID, "garmin"); !have {
		t.Error("the automatic pass dropped the push record of a crew ride row")
	}
	if _, still := h.garmin.remoteWorkouts["remote-crew"]; !still {
		t.Error("the automatic pass withdrew a crew ride row's copy from the account")
	}
}
