package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The blackout is what keeps a session a life event removed from coming back.
// Every place a plan is built (the fill, the season pass, Replan, the tick) and
// every place it is adapted must refuse the days an event covers.
//
// The clock is Wednesday 2026-10-07; the rider trains Tue/Thu/Sat/Sun.

func (h *onceHarness) event(t *testing.T, kind, start, end, option string) workout.LifeEvent {
	t.Helper()
	e, err := h.store.CreateLifeEvent(context.Background(), workout.LifeEvent{
		Rider: "wilant", Kind: kind, Start: start, End: end, Option: option,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (h *onceHarness) all(t *testing.T) []workout.Workout {
	t.Helper()
	ws, err := h.store.ListWorkouts(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func assertNothingBetween(t *testing.T, ws []workout.Workout, from, to string) {
	t.Helper()
	for _, w := range ws {
		if w.Date >= from && w.Date <= to {
			t.Errorf("%q is planned on %s, inside the life event %s to %s", w.Name, w.Date, from, to)
		}
	}
}

func TestAnEventCreatedBeforeItsWeeksAreFilledKeepsThemClear(t *testing.T) {
	h := newOnceHarness(t)
	h.event(t, "travel", "2026-10-08", "2026-10-11", "no_bike")
	// A month out, in a week the season pass fills in the same tick.
	h.event(t, "busy", "2026-11-03", "2026-11-10", "")

	h.srv.AutoScheduleTick(context.Background())

	ws := h.all(t)
	if len(ws) == 0 {
		t.Fatal("nothing was planned at all")
	}
	assertNothingBetween(t, ws, "2026-10-08", "2026-10-11")
	assertNothingBetween(t, ws, "2026-11-03", "2026-11-10")
	if len(datesIn(t, ws, utcNoon(2026, time.October, 12), utcNoon(2026, time.October, 18))) == 0 {
		t.Error("the week after the trip is empty: only the event's own days may be cleared")
	}
}

func TestARemovedSessionStaysRemovedThroughReplanTheTickAndTheFillButton(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	h.srv.AutoScheduleTick(ctx)

	// What applying a life event does: the Saturday session is removed.
	var sat workout.Workout
	for _, w := range h.all(t) {
		if w.Date == "2026-10-10" {
			sat = w
		}
	}
	if sat.ID == "" {
		t.Fatal("no session on Saturday to remove")
	}
	if err := h.store.DeleteWorkout(ctx, sat.ID); err != nil {
		t.Fatal(err)
	}
	h.event(t, "travel", "2026-10-10", "2026-10-10", "no_bike")

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("replan status %d", resp.StatusCode)
	}
	assertNothingBetween(t, h.all(t), "2026-10-10", "2026-10-10")

	h.srv.AutoScheduleTick(ctx)
	assertNothingBetween(t, h.all(t), "2026-10-10", "2026-10-10")

	resp := h.as("wilant", "cyclists", http.MethodPost, fmt.Sprintf("/api/training/goals/%s/schedule", h.goal.ID), "")
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("fill status %d", resp.StatusCode)
	}
	assertNothingBetween(t, h.all(t), "2026-10-10", "2026-10-10")
}

func TestReplanClearsAnEventDayEvenForAFreshWeek(t *testing.T) {
	h := newOnceHarness(t)
	// Never ticked: replan itself is what plans the rest of the week.
	h.event(t, "illness", "2026-10-10", "2026-10-11", "proper")
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("replan status %d", resp.StatusCode)
	}
	ws := h.all(t)
	if len(ws) == 0 {
		t.Fatal("replan planned nothing")
	}
	assertNothingBetween(t, ws, "2026-10-10", "2026-10-11")
}

func TestALifeEventMoveIsNotRecreatedOnItsVacatedDay(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	h.srv.AutoScheduleTick(ctx)

	var thu workout.Workout
	for _, w := range h.all(t) {
		if w.Date == "2026-10-08" {
			thu = w
		}
	}
	if thu.ID == "" {
		t.Fatal("no session on Thursday")
	}
	// What applying a travel event does to a session it keeps: moved to Friday,
	// noted in the form the scheduler reads, and adjusted.
	friday := "2026-10-09"
	desc := thu.Description + "\n\nRescheduled by a life event: moved from 2026-10-08. " + scheduler.AdjustedMarker + " moved."
	if _, err := h.store.UpdateWorkout(ctx, thu.ID, workout.UpdateWorkoutRequest{Date: &friday, Description: &desc}); err != nil {
		t.Fatal(err)
	}

	// The fill button tops up any empty day of the week; Thursday is vacated,
	// not empty.
	resp := h.as("wilant", "cyclists", http.MethodPost, fmt.Sprintf("/api/training/goals/%s/schedule", h.goal.ID), "")
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("fill status %d", resp.StatusCode)
	}
	h.srv.AutoScheduleTick(ctx)
	for _, w := range h.all(t) {
		if w.Date == "2026-10-08" {
			t.Errorf("%q was planned again on the day it was moved from", w.Name)
		}
	}
}

func (h *onceHarness) hardSession(t *testing.T, date string, minutes int) workout.Workout {
	t.Helper()
	w, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: h.goal.ID, Sport: model.SportCycling, Name: "Threshold 3x12", Date: date,
		Description: scheduler.GeneratedDescription, Zone: workout.ZoneThreshold, Level: 4,
		Steps: []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: float64(minutes * 60), Target: workout.TargetOpen}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestTheReturnRampReachesSessionsMadeAfterTheEvent(t *testing.T) {
	h := newOnceHarness(t)
	ctx := context.Background()
	// Ill 4 to 6 October (3 days, proper): ramp days are the 7th (1) to the
	// 13th (7); the first two are easy days.
	h.event(t, "illness", "2026-10-04", "2026-10-06", "proper")
	day2 := h.hardSession(t, "2026-10-08", 90)
	day3 := h.hardSession(t, "2026-10-09", 60)
	day8 := h.hardSession(t, "2026-10-14", 60)

	h.srv.AdaptWorkouts(ctx)

	got2, _ := h.store.GetWorkout(ctx, day2.ID)
	if got2.Zone != workout.ZoneEndurance || workout.PlannedSeconds(got2.Steps) > 3600 {
		t.Errorf("ramp day 2: zone %s, %v seconds; want an easy session of at most an hour", got2.Zone, workout.PlannedSeconds(got2.Steps))
	}
	got3, _ := h.store.GetWorkout(ctx, day3.ID)
	if got3.Zone != workout.ZoneThreshold || got3.Level != 3 {
		t.Errorf("ramp day 3: zone %s level %v; want threshold one rung down (3)", got3.Zone, got3.Level)
	}
	got8, _ := h.store.GetWorkout(ctx, day8.ID)
	if got8.Level != 4 || strings.Contains(got8.Description, scheduler.AdjustedMarker) {
		t.Errorf("ramp day 8 was changed: %+v", got8)
	}
	for _, g := range []workout.Workout{got2, got3} {
		if n := strings.Count(g.Description, scheduler.AdjustedMarker); n != 1 || !strings.Contains(g.Description, "Life event") {
			t.Errorf("description %q: want one adjusted marker and a life-event note", g.Description)
		}
	}

	// One automatic change per workout: a second pass changes nothing.
	h.srv.AdaptWorkouts(ctx)
	again2, _ := h.store.GetWorkout(ctx, day2.ID)
	again3, _ := h.store.GetWorkout(ctx, day3.ID)
	if again2.Description != got2.Description || again3.Description != got3.Description || again3.Level != 3 {
		t.Error("a second adaptation pass changed a ramped session again")
	}
}

func TestTheRampSurvivesAReplan(t *testing.T) {
	h := newOnceHarness(t)
	h.event(t, "illness", "2026-10-04", "2026-10-06", "proper")
	h.srv.AutoScheduleTick(context.Background()) // plans, adapts

	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("replan status %d", resp.StatusCode)
	}
	eased := 0
	for _, w := range h.all(t) {
		if w.Date >= "2026-10-07" && w.Date <= "2026-10-13" && strings.Contains(w.Description, "Life event") {
			eased++
			if workout.PlannedSeconds(w.Steps) > 5400 {
				t.Errorf("%s on %s is %v seconds, longer than any ramp cap", w.Name, w.Date, workout.PlannedSeconds(w.Steps))
			}
		}
	}
	if eased == 0 {
		t.Error("after a replan no session in the return window carries the ramp")
	}
}

func (h *tomorrowHarness) event(start, end string) {
	h.t.Helper()
	if _, err := h.store.CreateLifeEvent(context.Background(), workout.LifeEvent{
		Rider: "wilant", Kind: "busy", Start: start, End: end,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func TestReadinessChipAndTomorrowAdvisoryAreHiddenOnEventDays(t *testing.T) {
	h := newTomorrowHarness(t)
	h.hard(tmTomorrow)
	h.snapshot(40, 100) // a rest forecast for tomorrow

	_, body, _ := h.get("wilant", "cyclists", "?today="+tmToday)
	if body.Tomorrow == nil {
		t.Fatal("the control case should forecast tomorrow")
	}

	h.event(tmTomorrow, tmTomorrow)
	_, body, _ = h.get("wilant", "cyclists", "?today="+tmToday)
	if body.Tomorrow != nil {
		t.Errorf("tomorrow's advisory shown for an event day: %+v", body.Tomorrow)
	}

	h.event(tmToday, tmToday)
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/readiness?today="+tmToday, "")
	var out struct {
		Today struct {
			LifeEvent bool `json:"lifeEvent"`
		} `json:"today"`
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Today.LifeEvent {
		t.Errorf("today is an event day but the response does not say so: %s", buf.String())
	}
}

func TestAnFTPTestCannotBeScheduledOnAnEventDay(t *testing.T) {
	h := newOnceHarness(t)
	h.event(t, "travel", "2026-10-09", "2026-10-09", "no_bike")
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/tests/ftp",
		`{"protocol":"twenty_minute","date":"2026-10-09"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409 for a day inside a life event", resp.StatusCode)
	}
	for _, w := range h.all(t) {
		if w.TestProtocol != "" {
			t.Fatalf("a test was scheduled anyway: %+v", w)
		}
	}
}
