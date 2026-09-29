package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/garmin"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func brusselsClock(t *testing.T, at *time.Time) func() time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	return func() time.Time { return at.In(loc) }
}

// The point of the change: the metrics sync must not depend on the
// auto-schedule flag. It only reads from the providers and writes the
// rider's own history.
func TestSyncTrainingMetricsRunsWithAutoScheduleOff(t *testing.T) {
	fake := &fakeGarmin{activities: []garmin.Activity{
		{ID: "5001", Sport: "cycling", StartTime: time.Date(2026, 3, 4, 7, 0, 0, 0, time.UTC),
			DurationSeconds: 5400, DistanceM: 45000, AvgHR: 150, AvgPowerWatts: 230},
	}}
	h := newMetricsSyncHarness(t, fake)
	h.seedGarminSession("wilant")
	h.seedWahooSession("other")
	// auto-schedule flag is never set: off.

	h.srv.SyncTrainingMetrics(context.Background())

	sessions, err := h.training.ListSessions(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Errorf("wilant sessions = %d, want 1 — the scheduled sync must not need the auto-schedule flag", len(sessions))
	}
	if len(h.wahooCalls) == 0 {
		t.Error("the Wahoo rider was not synced")
	}
}

// The steps that change a rider's workouts stay behind the flag.
func TestSyncTrainingMetricsNeverSchedulesOrAdaptsWorkouts(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.seedGarminSession("wilant")
	ctx := context.Background()

	if _, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{
		Rider: "wilant", Name: "Race Day", EventDate: time.Now().AddDate(0, 0, 70).Format("2006-01-02"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.SyncTrainingMetrics(ctx)

	workouts, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(workouts) != 0 {
		t.Fatalf("workouts = %+v, want none — scheduling stays behind the auto-schedule flag", workouts)
	}
}

// Missed-run rule: on start, sync once if no sync has happened since the last
// scheduled slot, then stay quiet until the next slot has passed.
func TestRunMetricsSyncIfMissed(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.seedWahooSession("other")
	h.srv.Config = &config.Config{} // defaults: 06:30 and 21:00 Europe/Brussels

	loc, _ := time.LoadLocation("Europe/Brussels")
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, loc)
	h.srv.Clock = brusselsClock(t, &now)
	ctx := context.Background()

	if !h.srv.RunMetricsSyncIfMissed(ctx) {
		t.Fatal("never synced, and 06:30 has passed: want a catch-up run")
	}
	if len(h.wahooCalls) != 1 {
		t.Fatalf("wahoo calls after catch-up = %d, want 1", len(h.wahooCalls))
	}

	now = now.Add(2 * time.Hour) // 10:00, no slot since
	if h.srv.RunMetricsSyncIfMissed(ctx) {
		t.Error("ran again with no slot passed since the last sync")
	}
	if len(h.wahooCalls) != 1 {
		t.Errorf("wahoo calls = %d, want still 1", len(h.wahooCalls))
	}

	now = time.Date(2026, 6, 10, 21, 5, 0, 0, loc) // evening slot passed while "down"
	if !h.srv.RunMetricsSyncIfMissed(ctx) {
		t.Error("the 21:00 slot was missed: want a catch-up run")
	}
	if len(h.wahooCalls) != 2 {
		t.Errorf("wahoo calls = %d, want 2", len(h.wahooCalls))
	}
}

// A 30-minute tick that already synced this morning counts: the start-up
// catch-up must not repeat it.
func TestFlaggedTickCountsAsALastRun(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.seedWahooSession("other")
	h.srv.Config = &config.Config{}
	loc, _ := time.LoadLocation("Europe/Brussels")
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, loc)
	h.srv.Clock = brusselsClock(t, &now)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "admin"); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(context.Background())
	if h.srv.RunMetricsSyncIfMissed(context.Background()) {
		t.Error("catch-up ran right after a tick that had just synced")
	}
	if len(h.wahooCalls) != 1 {
		t.Errorf("wahoo calls = %d, want 1", len(h.wahooCalls))
	}
}
