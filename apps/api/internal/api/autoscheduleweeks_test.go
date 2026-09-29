package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Every date here is built in UTC and the clock is fixed, so nothing in this
// file depends on the machine's zone or on today's date.
const (
	weeksEventDate = "2027-01-17"
)

func utcNoon(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }

func weekBounds(now time.Time) (thisMon, nextMon time.Time) {
	thisMon = periodization.MondayOf(now)
	return thisMon, thisMon.AddDate(0, 0, 7)
}

// seedWeeksRider gives rider a dated goal and a four-day availability, and
// returns the goal.
func seedWeeksRider(t *testing.T, h *autoScheduleHarness, rider string) workout.Goal {
	t.Helper()
	ctx := context.Background()
	g, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: rider, Name: "Race Day", EventDate: weeksEventDate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: rider, HoursPerAvailableDay: 3, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	return g
}

func datesIn(t *testing.T, ws []workout.Workout, from, to time.Time) []workout.Workout {
	t.Helper()
	var out []workout.Workout
	for _, w := range ws {
		if w.Date >= from.Format("2006-01-02") && w.Date <= to.Format("2006-01-02") {
			out = append(out, w)
		}
	}
	return out
}

func TestAutoScheduleTickPlansThisWeekAndNextWeek(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	now := utcNoon(2026, time.October, 7) // a Wednesday
	h.srv.Clock = func() time.Time { return now }
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	seedWeeksRider(t, h, "wilant")
	thisMon, nextMon := weekBounds(now)

	h.srv.AutoScheduleTick(ctx)

	all, err := h.store.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	thisN := len(datesIn(t, all, thisMon, thisMon.AddDate(0, 0, 6)))
	nextN := len(datesIn(t, all, nextMon, nextMon.AddDate(0, 0, 6)))
	if thisN == 0 {
		t.Error("this week is empty")
	}
	if nextN == 0 {
		t.Error("next week is empty — it must not stay empty")
	}
	if len(all) != thisN+nextN {
		t.Errorf("total workouts = %d, want %d (nothing scheduled beyond next week)", len(all), thisN+nextN)
	}

	h.srv.AutoScheduleTick(ctx)
	again, err := h.store.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(all) {
		t.Errorf("after a second tick: workouts = %d, want still %d", len(again), len(all))
	}
}

// Nothing the rider did to next week is undone or duplicated: an edited
// session, a session dragged to another day, a session they deleted and a
// workout they built themselves all survive the next tick.
func TestAutoScheduleTickLeavesRiderChangesToNextWeekAlone(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	now := utcNoon(2026, time.October, 7)
	h.srv.Clock = func() time.Time { return now }
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	seedWeeksRider(t, h, "wilant")
	_, nextMon := weekBounds(now)
	nextEnd := nextMon.AddDate(0, 0, 6)

	h.srv.AutoScheduleTick(ctx)
	all, _ := h.store.ListWorkouts(ctx, "wilant")
	next := datesIn(t, all, nextMon, nextEnd)
	if len(next) != 4 {
		t.Fatalf("next week workouts = %d, want 4 before the rider edits anything", len(next))
	}

	edited, moved, deleted := next[0], next[1], next[2]
	newName := "My own name"
	if _, err := h.store.UpdateWorkout(ctx, edited.ID, workout.UpdateWorkoutRequest{Name: &newName}); err != nil {
		t.Fatal(err)
	}
	monday := nextMon.Format("2006-01-02")
	if _, err := h.store.UpdateWorkout(ctx, moved.ID, workout.UpdateWorkoutRequest{Date: &monday}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.DeleteWorkout(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	own, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: edited.Sport, Name: "Coffee ride", Date: nextMon.AddDate(0, 0, 2).Format("2006-01-02"),
		Steps: edited.Steps,
	})
	if err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(ctx)

	after, _ := h.store.ListWorkouts(ctx, "wilant")
	nextAfter := datesIn(t, after, nextMon, nextEnd)
	if len(nextAfter) != 4 { // 3 surviving plan-made + the rider's own
		t.Fatalf("next week workouts = %d, want 4 (3 plan-made survivors + the rider's own) — no refill, no duplicates", len(nextAfter))
	}
	byID := map[string]workout.Workout{}
	for _, w := range after {
		byID[w.ID] = w
	}
	if byID[edited.ID].Name != newName {
		t.Errorf("edited workout name = %q, want %q", byID[edited.ID].Name, newName)
	}
	if byID[moved.ID].Date != monday {
		t.Errorf("moved workout date = %s, want %s", byID[moved.ID].Date, monday)
	}
	if _, ok := byID[deleted.ID]; ok {
		t.Error("a workout the rider deleted came back")
	}
	if byID[own.ID].Name != "Coffee ride" {
		t.Errorf("rider-authored workout changed: %+v", byID[own.ID])
	}
}

// Next week gets the plan's week for that Monday, not a copy of this week.
// A rolling plan (no event date) counts weeks from a fixed anchor, so a
// recovery week arrives on a known calendar Monday: this week is a load week,
// next week is the recovery week, and it must be easy — no structured session.
func TestAutoScheduleTickNextWeekIsARecoveryWeekWhenThePlanSaysSo(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	if _, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Keep riding"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 3, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		t.Fatal(err)
	}
	g := workout.Goal{}
	profile, _, _ := h.store.GetProfile(ctx, "wilant")

	// Ask the periodization package itself rather than hard-coding a date a
	// change to its cadence would silently invalidate.
	var now time.Time
	for d := utcNoon(2026, time.September, 7); d.Before(utcNoon(2027, time.January, 1)); d = d.AddDate(0, 0, 7) {
		plan, err := periodization.Build(g, profile, d)
		if err == nil && len(plan.Weeks) >= 2 && !plan.Weeks[0].Recovery && plan.Weeks[1].Recovery {
			now = d
			break
		}
	}
	if now.IsZero() {
		t.Fatal("no load->recovery boundary found in the scanned range; the periodization cadence changed")
	}
	h.srv.Clock = func() time.Time { return now }
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	thisMon, nextMon := weekBounds(now)

	h.srv.AutoScheduleTick(ctx)

	all, _ := h.store.ListWorkouts(ctx, "wilant")
	structured := func(ws []workout.Workout) int {
		n := 0
		for _, w := range ws {
			if workout.IsStructuredZone(w.Zone) {
				n++
			}
		}
		return n
	}
	thisWeek := datesIn(t, all, thisMon, thisMon.AddDate(0, 0, 6))
	nextWeek := datesIn(t, all, nextMon, nextMon.AddDate(0, 0, 6))
	if len(nextWeek) == 0 {
		t.Fatal("next week is empty")
	}
	if structured(thisWeek) == 0 {
		t.Errorf("this week (a load week) has no structured session: %+v", thisWeek)
	}
	if got := structured(nextWeek); got != 0 {
		t.Errorf("next week is a recovery week but has %d structured sessions", got)
	}
}

// At a phase boundary of a dated plan, next week is exactly what the
// scheduler builds for the plan's second week — the new phase's sessions, not
// this week's repeated.
func TestAutoScheduleTickNextWeekMatchesThePlansNextWeekAtAPhaseBoundary(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	g := seedWeeksRider(t, h, "wilant")
	profile, _, _ := h.store.GetProfile(ctx, "wilant")

	var now time.Time
	var plan periodization.Plan
	for d := utcNoon(2026, time.September, 7); d.Before(utcNoon(2027, time.January, 10)); d = d.AddDate(0, 0, 7) {
		p, err := periodization.Build(g, profile, d)
		if err == nil && len(p.Weeks) >= 2 && p.Weeks[0].Phase != p.Weeks[1].Phase {
			now, plan = d, p
			break
		}
	}
	if now.IsZero() {
		t.Fatal("no phase boundary found in the scanned range")
	}
	h.srv.Clock = func() time.Time { return now }
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	_, nextMon := weekBounds(now)

	h.srv.AutoScheduleTick(ctx)

	all, _ := h.store.ListWorkouts(ctx, "wilant")
	got := datesIn(t, all, nextMon, nextMon.AddDate(0, 0, 6))

	levelRows, _ := h.store.ListLevels(ctx, "wilant")
	levels := map[string]float64{}
	for _, l := range levelRows {
		if l.Sport == g.Sport {
			levels[string(l.Zone)] = l.Level
		}
	}
	want, err := scheduler.WeekWorkouts(plan.Weeks[1], profile, levels, "wilant", g.ID, g.Sport)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("test setup: the plan's next week builds nothing")
	}
	if len(got) != len(want) {
		t.Fatalf("next week has %d workouts, want %d", len(got), len(want))
	}
	for _, w := range want {
		found := false
		for _, have := range got {
			if have.Date == w.Date && have.Zone == w.Zone && have.Name == w.Name {
				found = true
			}
		}
		if !found {
			t.Errorf("next week lacks %s %q (%s) from the plan's own next week", w.Date, w.Name, w.Zone)
		}
	}
}

// Replan is "the rest of this week" and must leave next week alone.
func TestReplanDoesNotTouchNextWeek(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	now := utcNoon(2026, time.October, 7)
	h.srv.Clock = func() time.Time { return now }
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	seedWeeksRider(t, h, "wilant")
	_, nextMon := weekBounds(now)
	h.srv.AutoScheduleTick(ctx)

	before, _ := h.store.ListWorkouts(ctx, "wilant")
	ids := map[string]bool{}
	for _, w := range datesIn(t, before, nextMon, nextMon.AddDate(0, 0, 6)) {
		ids[w.ID] = true
	}

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replan status = %d", resp.StatusCode)
	}

	after, _ := h.store.ListWorkouts(ctx, "wilant")
	kept := 0
	for _, w := range datesIn(t, after, nextMon, nextMon.AddDate(0, 0, 6)) {
		if ids[w.ID] {
			kept++
		}
	}
	if kept != len(ids) || len(ids) != 4 {
		t.Errorf("next week: kept %d of %d original workouts, want all 4 untouched by replan", kept, len(ids))
	}
}

type scheduleResp struct {
	Created []struct {
		Date string `json:"date"`
	} `json:"created"`
	Skipped int `json:"skipped"`
}

func TestGoalScheduleFillsAFutureWeekAndRefusesAPastOne(t *testing.T) {
	h := newAutoScheduleHarness(t)
	now := utcNoon(2026, time.October, 7)
	h.srv.Clock = func() time.Time { return now }
	g := seedWeeksRider(t, h, "wilant")
	thisMon, nextMon := weekBounds(now)
	path := "/api/training/goals/" + g.ID + "/schedule"

	// Next week: filled, every workout inside that week.
	resp := h.as("wilant", "cyclists", http.MethodPost, path, `{"weekStart":"`+nextMon.Format("2006-01-02")+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("next week status = %d, want 200", resp.StatusCode)
	}
	var out scheduleResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Created) != 4 {
		t.Fatalf("created = %d, want 4", len(out.Created))
	}
	for _, c := range out.Created {
		if c.Date < nextMon.Format("2006-01-02") || c.Date > nextMon.AddDate(0, 0, 6).Format("2006-01-02") {
			t.Errorf("created %s, outside next week", c.Date)
		}
	}

	// Filling it again creates nothing.
	resp = h.as("wilant", "cyclists", http.MethodPost, path, `{"weekStart":"`+nextMon.Format("2006-01-02")+`"}`)
	out = scheduleResp{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Created) != 0 {
		t.Errorf("second fill created %d, want 0", len(out.Created))
	}

	// A past week is refused, and nothing is created.
	lastMon := thisMon.AddDate(0, 0, -7)
	resp = h.as("wilant", "cyclists", http.MethodPost, path, `{"weekStart":"`+lastMon.Format("2006-01-02")+`"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("past week status = %d, want 400", resp.StatusCode)
	}
	resp = h.as("wilant", "cyclists", http.MethodPost, path, `{"weekStart":"not-a-date"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad date status = %d, want 400", resp.StatusCode)
	}

	// No body is still "this week", as before.
	resp = h.as("wilant", "cyclists", http.MethodPost, path, "")
	out = scheduleResp{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || len(out.Created) == 0 {
		t.Errorf("default fill: status %d, created %d — want this week's workouts", resp.StatusCode, len(out.Created))
	}
}
