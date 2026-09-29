package api_test

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The automatic push sends only today's session, so the head unit shows one
// day. These run on a fixed UTC clock; "today" is the deployment's local date
// (Europe/Brussels unless configured), so each test says which instant it is.

const allDaysJSON = `["mon","tue","wed","thu","fri","sat","sun"]`

// todayHarness is a rider with a Stay Fit goal, every day available and
// auto-push on, with auto-schedule on and the clock at now.
type todayHarness struct {
	*pushHarness
	now time.Time
}

func newTodayHarness(t *testing.T, now time.Time, autoPush bool) *todayHarness {
	t.Helper()
	h := &todayHarness{pushHarness: newPushHarness(t), now: now}
	h.srv.Clock = func() time.Time { return h.now }
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	body := `{"hoursPerAvailableDay":1.5,"availableDays":` + allDaysJSON
	if autoPush {
		body += `,"autoPushWorkouts":true`
	}
	if resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", body+`}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("save profile: status %d", resp.StatusCode)
	}
	if _, err := h.training.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"}); err != nil {
		t.Fatal(err)
	}
	return h
}

// calendarDates is every date the fake Garmin calendar holds, sorted.
func (h *todayHarness) calendarDates() []string {
	var out []string
	for _, d := range h.garmin.calendar {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func (h *todayHarness) workoutsOn(t *testing.T, date string) []workout.Workout {
	t.Helper()
	var out []workout.Workout
	all, err := h.training.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range all {
		if w.Date == date {
			out = append(out, w)
		}
	}
	return out
}

func TestAutoPushSendsOnlyTodaysWorkout(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), true) // Wednesday
	h.srv.AutoScheduleTick(context.Background())

	if got, want := h.calendarDates(), []string{"2026-10-07"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("calendar = %v, want only today %v", got, want)
	}
	if n := len(h.garmin.remoteWorkouts); n != 1 {
		t.Errorf("account holds %d workouts, want just today's", n)
	}
}

func TestTomorrowsWorkoutIsPushedTheNextDay(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), true)
	ctx := context.Background()
	h.srv.AutoScheduleTick(ctx)

	h.now = utcNoon(2026, time.October, 8)
	h.srv.AutoScheduleTick(ctx)

	got := h.calendarDates()
	if len(got) != 2 || got[0] != "2026-10-07" || got[1] != "2026-10-08" {
		t.Errorf("calendar = %v, want yesterday's copy left alone and the new day added", got)
	}
}

func TestAutoPushRemovesItsOwnFutureCopiesButNotManualPushes(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), true)
	ctx := context.Background()
	h.srv.AutoScheduleTick(ctx)

	// What the old fortnight-ahead pass left on the account: two plan-made
	// sessions in the future, recorded as auto-pushed.
	future := []workout.Workout{}
	for _, date := range []string{"2026-10-09", "2026-10-10"} {
		future = append(future, h.workoutsOn(t, date)...)
	}
	if len(future) != 2 {
		t.Fatalf("test needs two future plan-made sessions, has %d", len(future))
	}
	for i, wk := range future {
		remote, entry := "legacy-"+wk.ID, "legacy-entry-"+wk.ID
		h.garmin.remoteWorkouts[remote] = wk.Name
		if h.garmin.calendar == nil {
			h.garmin.calendar = map[string]string{}
		}
		h.garmin.calendar[entry] = wk.Date
		if err := h.training.SavePush(ctx, workout.Push{
			WorkoutID: wk.ID, Provider: "garmin", RemoteID: remote, ScheduleID: entry,
			ScheduledDate: wk.Date, ContentHash: workout.ContentHash(wk), Origin: workout.PushOriginAuto,
		}); err != nil {
			t.Fatal(i, err)
		}
	}

	// A rider's own explicit push of a future day: a hand-built workout, and a
	// plan-made one, both by the button.
	manualOwn := h.newWorkout("2026-10-12")
	h.push(manualOwn)
	planMade := h.workoutsOn(t, "2026-10-11")
	if len(planMade) != 1 {
		t.Fatalf("test needs one plan-made session on the 11th, has %d", len(planMade))
	}
	h.push(planMade[0].ID)

	h.garmin.workoutCalls = nil
	h.srv.AutoScheduleTick(ctx)

	for _, wk := range future {
		if _, ok := h.garmin.remoteWorkouts["legacy-"+wk.ID]; ok {
			t.Errorf("the auto-pushed copy of %s is still on the account", wk.Date)
		}
		if _, ok := h.garmin.calendar["legacy-entry-"+wk.ID]; ok {
			t.Errorf("the calendar entry for %s is still there", wk.Date)
		}
		if _, have, _ := h.training.GetPush(ctx, wk.ID, "garmin"); have {
			t.Errorf("the push record for %s survived its removal", wk.Date)
		}
	}
	for _, id := range []string{manualOwn, planMade[0].ID} {
		push, have, _ := h.training.GetPush(ctx, id, "garmin")
		if !have || push.Origin != workout.PushOriginManual {
			t.Errorf("manual push of %s = %+v (have %v), want kept and still manual", id, push, have)
		}
		if _, ok := h.garmin.remoteWorkouts[push.RemoteID]; !ok {
			t.Errorf("manual push of %s was taken off the account", id)
		}
	}
	if strings.Contains(h.calls(), "delete garmin-workout") {
		t.Errorf("calls = %q, a manual copy was deleted", h.calls())
	}
}

// A workout pushed by hand and then moved to another day is still the rider's
// explicit push: the automatic pass keeps it in step but never retracts it.
func TestAnAutoPassNeverTakesManualOwnershipAway(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), true)
	ctx := context.Background()
	h.srv.AutoScheduleTick(ctx)

	today := h.workoutsOn(t, "2026-10-07")
	if len(today) != 1 {
		t.Fatalf("today has %d sessions", len(today))
	}
	h.push(today[0].ID) // by hand, over the copy the auto pass already made
	h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+today[0].ID, `{"date":"2026-10-14"}`)

	h.srv.AutoScheduleTick(ctx)

	push, have, _ := h.training.GetPush(ctx, today[0].ID, "garmin")
	if !have || push.Origin != workout.PushOriginManual {
		t.Fatalf("push = %+v (have %v), want the rider's manual push kept", push, have)
	}
}

func TestARiderWithAutoPushOffGetsNothing(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), false)
	h.srv.AutoScheduleTick(context.Background())

	if len(h.garmin.remoteWorkouts) != 0 || len(h.garmin.calendar) != 0 || h.calls() != "" {
		t.Errorf("account holds %v / %v after calls %q, want nothing", h.garmin.remoteWorkouts, h.garmin.calendar, h.calls())
	}
}

// "Today" is the rider's local date, not UTC's: at 23:30 UTC the deployment's
// zone (Brussels) is already on the next day, and that is the day delivered.
func TestTodayIsTheDeploymentsLocalDate(t *testing.T) {
	for name, tc := range map[string]struct {
		now  time.Time
		want string
	}{
		"23:30 UTC is already tomorrow in Brussels": {time.Date(2026, time.October, 7, 23, 30, 0, 0, time.UTC), "2026-10-08"},
		"21:30 UTC is still today in Brussels":      {time.Date(2026, time.October, 7, 21, 30, 0, 0, time.UTC), "2026-10-07"},
		"23:30 UTC in winter is one hour ahead":     {time.Date(2026, time.December, 9, 23, 30, 0, 0, time.UTC), "2026-12-10"},
		"22:30 UTC in winter is still today":        {time.Date(2026, time.December, 9, 22, 30, 0, 0, time.UTC), "2026-12-09"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newTodayHarness(t, tc.now, true)
			h.srv.AutoScheduleTick(context.Background())

			got := h.calendarDates()
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("calendar = %v, want only %s", got, tc.want)
			}
		})
	}
}

// training.timezone decides the day: at noon UTC it is already the next morning
// in Auckland.
func TestTodayFollowsTheConfiguredTimezone(t *testing.T) {
	h := newTodayHarness(t, utcNoon(2026, time.October, 7), true)
	h.srv.Config = &config.Config{Training: config.TrainingConfig{Timezone: "Pacific/Auckland"}}
	h.srv.AutoScheduleTick(context.Background())

	if got := h.calendarDates(); len(got) != 1 || got[0] != "2026-10-08" {
		t.Errorf("calendar = %v, want only 2026-10-08", got)
	}
}

// Replan and scheduling an FTP test push through the same pass, so they too
// place only today.
func TestReplanPushesOnlyToday(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	if _, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"wed", "fri", "sat"}, AutoPushWorkouts: true,
	}); err != nil {
		t.Fatal(err)
	}

	if resp, out := h.replan("wilant"); resp.StatusCode != http.StatusOK || out.Created < 3 {
		t.Fatalf("replan: status %d, created %d, want a week of sessions", resp.StatusCode, out.Created)
	}

	if len(h.garmin.calendar) != 1 {
		t.Fatalf("calendar = %v, want just today's session", h.garmin.calendar)
	}
	for _, date := range h.garmin.calendar {
		if date != replanToday {
			t.Errorf("calendar holds %s, want only %s", date, replanToday)
		}
	}
}

func TestSchedulingAFutureFTPTestDoesNotPushItYet(t *testing.T) {
	h := newReplanHarness(t)
	h.scheduleSetup(250)
	if _, err := h.training.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: "wilant", FTPWatts: 250, HoursPerAvailableDay: 1.5, AvailableDays: []string{"wed", "fri", "sat"}, AutoPushWorkouts: true,
	}); err != nil {
		t.Fatal(err)
	}

	resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"ramp","date":"`+replanFriday+`"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	for _, date := range h.garmin.calendar {
		if date == replanFriday {
			t.Errorf("Friday's test was sent on Wednesday: %v", h.garmin.calendar)
		}
	}
}
