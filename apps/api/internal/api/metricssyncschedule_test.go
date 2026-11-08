package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
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

// Two replicas run the loop against one database. When the slot arrives both
// timers fire, but the shared last-run timestamp — re-checked under the lock —
// means one sync happens, not two. Replica B's clock runs half a second behind
// A's so B checks after A has recorded its run: SQLite has no advisory lock to
// make the two mutually exclusive, and this test is about the re-check.
func TestTwoReplicasSyncOncePerSlot(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.seedWahooSession("other")
	loc, _ := time.LoadLocation("Europe/Brussels")
	slot := time.Date(2026, 6, 10, 21, 0, 0, 0, loc)

	// Each replica has its own clock and its own timer that the test fires by
	// hand: no real time passes. Replica B's clock runs half a second behind
	// A's, as in a real deployment.
	type replica struct {
		srv    *api.Server
		now    atomic.Pointer[time.Time]
		timers chan chan time.Time // one per wait the loop starts
	}
	newR := func(srv *api.Server, at time.Time) *replica {
		r := &replica{srv: srv, timers: make(chan chan time.Time, 4)}
		r.now.Store(&at)
		srv.Config = &config.Config{}
		srv.Clock = func() time.Time { return *r.now.Load() }
		srv.SyncAfter = func(time.Duration) <-chan time.Time {
			c := make(chan time.Time, 1)
			r.timers <- c
			return c
		}
		return r
	}
	start := slot.Add(-1500 * time.Millisecond)
	a := newR(h.srv, start)
	b := newR(h.newReplica(), start.Add(-500*time.Millisecond))

	// Already synced this morning: nothing to catch up on at start.
	if err := h.settings.SetFlagAt("metrics_sync_last_run", true, "scheduler",
		time.Date(2026, 6, 10, 6, 31, 0, 0, loc)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	for _, r := range []*replica{a, b} {
		go func() { r.srv.RunMetricsSyncLoop(ctx); done <- struct{}{} }()
	}
	wait := func(r *replica) chan time.Time {
		select {
		case c := <-r.timers:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("a replica's loop never started waiting for the slot")
			return nil
		}
	}
	timerA, timerB := wait(a), wait(b)

	// Both timers fire (the slot plus a second of slack), A first. A syncs and
	// records the slot; only then does B's timer fire, so B's re-check is what
	// stops it.
	fireA := slot.Add(time.Second)
	a.now.Store(&fireA)
	timerA <- fireA
	wait(a) // A is back to waiting: its pass is over
	fireB := fireA.Add(-500 * time.Millisecond)
	b.now.Store(&fireB)
	timerB <- fireB
	wait(b)
	cancel()
	<-done
	<-done

	if len(h.wahooCalls) != 1 {
		t.Errorf("wahoo calls = %d, want 1 — two replicas must not both sync the same slot", len(h.wahooCalls))
	}
}

// A pass cut short by shutdown did not sync everyone, so it must not be
// recorded as done: the next start catches up.
func TestCancelledMetricsPassIsNotRecorded(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.seedWahooSession("other")
	h.srv.Config = &config.Config{}
	loc, _ := time.LoadLocation("Europe/Brussels")
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, loc)
	h.srv.Clock = brusselsClock(t, &now)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if h.srv.RunMetricsSyncIfMissed(cancelled) {
		t.Error("a cancelled pass reported that it synced")
	}
	if _, err := h.settings.DescribeFlag("metrics_sync_last_run"); err == nil {
		t.Error("a cancelled pass was recorded as a completed sync")
	}
	if !h.srv.RunMetricsSyncIfMissed(context.Background()) {
		t.Error("the next start did not catch up")
	}
}

// Every rider failing is a Warn, and is not recorded, so a restart retries.
func TestEveryRiderFailingIsWarnedAndNotRecorded(t *testing.T) {
	h := newMetricsSyncHarness(t, &fakeGarmin{})
	h.seedWahooSession("other")
	h.srv.Config = &config.Config{}
	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
	loc, _ := time.LoadLocation("Europe/Brussels")
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, loc)
	h.srv.Clock = brusselsClock(t, &now)

	// The one place a rider's sync returns an error rather than a warning is
	// their own training data being unreadable.
	if _, err := h.db.Conn().Exec("DROP TABLE rider_profiles"); err != nil {
		t.Fatal(err)
	}

	h.srv.RunMetricsSyncIfMissed(context.Background())

	// The load ledger reads this: a rider whose sync failed has rides it has not seen.
	if !h.srv.MetricsFailedForTest("other") {
		t.Error("a rider whose sync errored was not marked as failed")
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "metrics sync: every rider failed") {
		t.Errorf("want a Warn about every rider failing, got:\n%s", out)
	}
	if strings.Contains(out, "metrics sync finished") {
		t.Errorf("logged the Info completion line for a run where everyone failed:\n%s", out)
	}
	if _, err := h.settings.DescribeFlag("metrics_sync_last_run"); err == nil {
		t.Error("a run where every rider failed was recorded, so a restart would not retry")
	}
}
