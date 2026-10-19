package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/calendarfeed"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// enduranceToday plants a generated, untouched endurance or long ride.
func (h *tomorrowHarness) enduranceOn(date, name string, steps []workout.WorkoutStep) workout.Workout {
	h.t.Helper()
	w, err := h.store.CreateWorkout(context.Background(), api.LegacyFlagged(workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: h.goalID, Sport: model.SportCycling, Name: name,
		Date: date, Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneEndurance, Steps: steps,
	}))
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

func (h *tomorrowHarness) checkIn(legs int) {
	h.t.Helper()
	if err := h.store.SaveCheckIn(context.Background(), workout.CheckIn{Rider: "wilant", Date: tmToday, Legs: legs}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *tomorrowHarness) todayVerdict() string {
	h.t.Helper()
	status, _, raw := h.get("wilant", "cyclists", "")
	if status != 200 {
		h.t.Fatalf("readiness status = %d", status)
	}
	var today struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(raw["today"], &today); err != nil {
		h.t.Fatal(err)
	}
	return today.Verdict
}

// A Wahoo-only rider has no wellness rows at all; the check-in is their only
// signal. The morning pass ran before they answered, so the first tick after
// it eases nothing, and the next tick after the answer does.
func TestCheckInLegsOneEasesTodaysEnduranceRideOnTheNextTick(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	orig := h.enduranceOn(tmToday, "Long ride", longRideSteps(9600))

	h.srv.AdaptWorkouts(ctx)
	if got, _ := h.store.GetWorkout(ctx, orig.ID); markerCount(got) != 0 {
		t.Fatalf("before the check-in the ride was changed: %q", got.Description)
	}
	if v := h.todayVerdict(); v != "ready" {
		t.Fatalf("verdict before the check-in = %q, want ready", v)
	}

	h.checkIn(1)
	// The Plan and Today pages reflect it at once...
	if v := h.todayVerdict(); v != "rest" {
		t.Fatalf("verdict after legs = 1 is %q, want rest from the check-in alone", v)
	}
	if got, _ := h.store.GetWorkout(ctx, orig.ID); markerCount(got) != 0 {
		t.Fatal("reading the page changed the session")
	}

	// ...the easing waits for the next adaptation tick.
	h.srv.AdaptWorkouts(ctx)
	got, _ := h.store.GetWorkout(ctx, orig.ID)
	if got.Name != "Recovery ride" || workout.PlannedSeconds(got.Steps) != 2700 {
		t.Fatalf("session = %q %v s, want a 45-minute Recovery ride", got.Name, workout.PlannedSeconds(got.Steps))
	}
	if markerCount(got) != 1 || got.Flags.Origin != workout.OriginPlan || len(got.Flags.EasedBy()) != 1 || got.Flags.EasedBy()[0] != why.ReadinessRest {
		t.Errorf("marker %d flags %+v, want one readiness_rest easing", markerCount(got), got.Flags)
	}
	a, ok := adjustmentFor(t, h.store, orig.ID)
	if !ok || a.Rule != why.ReadinessRest {
		t.Fatalf("adjustment = %+v (found %v), want readiness_rest", a, ok)
	}
	if in := decodeInputs[why.ReadinessInputs](t, a); len(in.Signals) != 1 || in.Signals[0].Kind != "checkin" {
		t.Errorf("inputs = %+v, want the check-in signal", in)
	}
	// Once is enough.
	h.srv.AdaptWorkouts(ctx)
	if n := adjustmentRows(t, h.autoScheduleHarness); n != 1 {
		t.Errorf("rows after a second tick = %d, want 1", n)
	}
}

func TestCautionShortensTodaysEnduranceRideAndKeepsItsName(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	orig := h.enduranceOn(tmToday, scheduler.ClimbingLongRideName, longRideSteps(9600)) // 3 h
	if err := h.store.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: tmToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52, ReadinessLevel: "LOW", ReadinessScore: 40,
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.AdaptWorkouts(ctx)

	got, _ := h.store.GetWorkout(ctx, orig.ID)
	if got.Name != scheduler.ClimbingLongRideName {
		t.Errorf("name = %q, want it kept", got.Name)
	}
	if want := 0.8 * 10800; workout.PlannedSeconds(got.Steps) != float64(int(want)) {
		t.Errorf("length = %v s, want %v", workout.PlannedSeconds(got.Steps), want)
	}
	if a, ok := adjustmentFor(t, h.store, orig.ID); !ok || a.Rule != why.ReadinessCaution {
		t.Errorf("adjustment = %+v (found %v), want readiness_caution", a, ok)
	}
}

func TestSingleAmberEasesNothing(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	if err := h.store.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: tmToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52,
	}); err != nil {
		t.Fatal(err)
	}
	hard := h.hard(tmToday)
	long := h.enduranceOn(tmToday, "Long ride", longRideSteps(9600))

	if v := h.todayVerdict(); v != "ready" {
		t.Fatalf("verdict = %q, want ready for a lone amber", v)
	}
	h.srv.AdaptWorkouts(ctx)
	for _, w := range []workout.Workout{hard, long} {
		got, _ := h.store.GetWorkout(ctx, w.ID)
		if markerCount(got) != 0 || got.Name != w.Name || workout.PlannedSeconds(got.Steps) != workout.PlannedSeconds(w.Steps) {
			t.Errorf("%s was changed by a single amber: %q", w.Name, got.Description)
		}
	}
	if n := adjustmentRows(t, h.autoScheduleHarness); n != 0 {
		t.Errorf("adjustment rows = %d, want 0", n)
	}
}

func TestReadinessEndpointShowsAPushVerdict(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	today := time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 30; i++ {
		v := 64.0
		if i%2 == 0 {
			v = 56
		}
		if err := h.store.SaveWellness(ctx, workout.DailyWellness{
			Rider: "wilant", Date: today.AddDate(0, 0, -i).Format("2006-01-02"), HRVLastNight: v, SleepSeconds: 8 * 3600,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: tmToday, HRVLastNight: 70, SleepSeconds: 8 * 3600,
	}); err != nil {
		t.Fatal(err)
	}
	if v := h.todayVerdict(); v != "push" {
		t.Fatalf("verdict = %q, want push", v)
	}
	// Push is a green light, not a change: nothing is eased, and the
	// tomorrow banner and the crew advice treat it as ready.
	hard := h.hard(tmToday)
	h.hard(tmTomorrow)
	h.srv.AdaptWorkouts(ctx)
	if got, _ := h.store.GetWorkout(ctx, hard.ID); markerCount(got) != 0 {
		t.Errorf("a push morning eased the session: %q", got.Description)
	}
	status, body, raw := h.get("wilant", "cyclists", "")
	if status != 200 || body.Tomorrow != nil {
		t.Errorf("status %d tomorrow %+v, want no easing banner on a push day", status, body.Tomorrow)
	}
	if c, ok := raw["crewRide"]; ok && string(c) != "null" {
		t.Errorf("crewRide = %s, want none", c)
	}
}

func TestShortenedSessionStaysIndoor(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	h.saveProfile(true)
	orig := h.enduranceOn(tmToday, "Long ride", longRideSteps(9600))
	makeIndoor(t, h.store, orig.ID)
	if err := h.store.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: tmToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52, ReadinessLevel: "LOW", ReadinessScore: 40,
	}); err != nil {
		t.Fatal(err)
	}

	h.srv.AdaptWorkouts(ctx)

	got, _ := h.store.GetWorkout(ctx, orig.ID)
	if !got.Indoor || got.OutdoorSteps == nil {
		t.Fatalf("indoor = %v outdoor steps = %v, want it kept indoor", got.Indoor, got.OutdoorSteps != nil)
	}
	if got.Name != "Long ride" || markerCount(got) != 1 {
		t.Errorf("name %q markers %d", got.Name, markerCount(got))
	}
	// 80 % of the outdoor length, not of the trainer form.
	if out := workout.PlannedSeconds(*got.OutdoorSteps); out != 0.8*workout.PlannedSeconds(orig.Steps) {
		t.Errorf("outdoor length = %v, want 80%% of %v", out, workout.PlannedSeconds(orig.Steps))
	}
	assertTrainerShape(t, got.Steps)
	a, ok := adjustmentFor(t, h.store, orig.ID)
	if !ok || !decodeInputs[why.ReadinessInputs](t, a).Indoor {
		t.Errorf("adjustment %+v (found %v), want indoor recorded", a, ok)
	}
	if !strings.Contains(got.Description, scheduler.AdjustedMarker) {
		t.Errorf("description = %q", got.Description)
	}
}

// A rider who is told to rest is never given a longer ride: a 30-minute ride
// is already easier than the 45-minute recovery ride that would replace it.
func TestRestLeavesAShortEnduranceRideAlone(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	orig := h.enduranceOn(tmToday, "Endurance ride", longRideSteps(600)) // 30 minutes in all
	if s := workout.PlannedSeconds(orig.Steps); s != 1800 {
		t.Fatalf("setup: ride is %v s", s)
	}
	h.checkIn(1)
	h.srv.AdaptWorkouts(ctx)
	got, _ := h.store.GetWorkout(ctx, orig.ID)
	if got.Name != orig.Name || workout.PlannedSeconds(got.Steps) != 1800 || markerCount(got) != 0 {
		t.Errorf("a short ride was changed on a rest day: %q %v s %q", got.Name, workout.PlannedSeconds(got.Steps), got.Description)
	}
	if n := adjustmentRows(t, h.autoScheduleHarness); n != 0 {
		t.Errorf("adjustment rows = %d, want 0", n)
	}
}

// A routed ride keeps the course it was made for; endurance easing is
// cycling-only and leaves it be, for caution and for rest alike.
func TestReadinessLeavesARoutedEnduranceRideAlone(t *testing.T) {
	for _, verdict := range []string{"rest", "caution"} {
		t.Run(verdict, func(t *testing.T) {
			h := newTomorrowHarness(t)
			ctx := context.Background()
			orig := h.enduranceOn(tmToday, "Long ride", longRideSteps(9600))
			slug := "morning-loop"
			if _, err := h.store.UpdateWorkout(ctx, orig.ID, workout.UpdateWorkoutRequest{RouteSlug: &slug}); err != nil {
				t.Fatal(err)
			}
			if verdict == "rest" {
				h.checkIn(1)
			} else if err := h.store.SaveWellness(ctx, workout.DailyWellness{
				Rider: "wilant", Date: tmToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52, ReadinessLevel: "LOW", ReadinessScore: 40,
			}); err != nil {
				t.Fatal(err)
			}
			if v := h.todayVerdict(); v != verdict {
				t.Fatalf("setup: verdict = %q", v)
			}
			h.srv.AdaptWorkouts(ctx)
			got, _ := h.store.GetWorkout(ctx, orig.ID)
			if got.Name != "Long ride" || workout.PlannedSeconds(got.Steps) != workout.PlannedSeconds(orig.Steps) || markerCount(got) != 0 || got.RouteSlug != slug {
				t.Errorf("a routed ride was changed: %q %v s %q", got.Name, workout.PlannedSeconds(got.Steps), got.Description)
			}
		})
	}
}

// The description of an eased session is copied verbatim into the calendar
// feed a third-party server keeps, so it must say nothing about health.
func TestReadinessEasingDescriptionsCarryNoHealthText(t *testing.T) {
	banned := []string{"slept", "sleep", "HRV", "heart rate", "mood", "legs", "form", "load", "readiness is", "score", "unbalanced", "56", "41", "52", "40"}
	cases := map[string]func(*tomorrowHarness){
		"rest endurance": func(h *tomorrowHarness) {
			h.enduranceOn(tmToday, "Long ride", longRideSteps(9600))
			if err := h.store.SaveCheckIn(context.Background(), workout.CheckIn{Rider: "wilant", Date: tmToday, Legs: 1, Mood: 2, SleepQuality: 2, Note: "slept badly"}); err != nil {
				t.Fatal(err)
			}
		},
		"caution endurance": func(h *tomorrowHarness) {
			h.enduranceOn(tmToday, "Long ride", longRideSteps(9600))
			wellnessCaution(t, h)
		},
		"rest hard": func(h *tomorrowHarness) {
			h.hard(tmToday)
			h.checkIn(1)
		},
		"caution hard": func(h *tomorrowHarness) {
			h.hard(tmToday)
			wellnessCaution(t, h)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newTomorrowHarness(t)
			setup(h)
			h.srv.AdaptWorkouts(context.Background())
			ws, err := h.store.ListWorkouts(context.Background(), "wilant")
			if err != nil {
				t.Fatal(err)
			}
			changed := 0
			for _, w := range ws {
				if markerCount(w) == 0 {
					continue
				}
				changed++
				for _, b := range banned {
					if strings.Contains(w.Description, b) {
						t.Errorf("description %q contains %q", w.Description, b)
					}
				}
				if !strings.Contains(w.Description, "readiness") {
					t.Errorf("description %q says nothing of why", w.Description)
				}
				// The popover still has the reasons.
				if a, ok := adjustmentFor(t, h.store, w.ID); !ok || len(a.Inputs) == 0 || a.Text == "" {
					t.Errorf("why record = %+v (found %v), want the signals kept", a, ok)
				}
			}
			if changed != 1 {
				t.Fatalf("changed sessions = %d, want 1", changed)
			}
			for _, ev := range calendarfeed.Events(ws, time.Date(2026, 3, 19, 8, 0, 0, 0, time.UTC), time.UTC, nil, "") {
				for _, b := range []string{"slept", "HRV", "mood", "resting heart rate"} {
					if strings.Contains(ev.Description, b) {
						t.Errorf("calendar event %q contains %q", ev.Description, b)
					}
				}
			}
		})
	}
}

func wellnessCaution(t *testing.T, h *tomorrowHarness) {
	t.Helper()
	if err := h.store.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: tmToday, HRVStatus: "UNBALANCED", HRVLastNight: 41, HRVWeeklyAvg: 52, ReadinessLevel: "LOW", ReadinessScore: 40,
	}); err != nil {
		t.Fatal(err)
	}
}
