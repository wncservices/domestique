package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// replanClock is a fixed Wednesday, given an explicit non-UTC location on
// purpose — every test below needs a stable "today" with days before,
// on and after it inside the same Monday-based week, and an explicit
// time.FixedZone means the result cannot depend on whatever TZ the host
// running `go test` happens to have (see AGENTS.md's Tests section).
func replanClock() func() time.Time {
	return func() time.Time {
		return time.Date(2026, 3, 25, 9, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	}
}

const (
	replanMonday   = "2026-03-23"
	replanToday    = "2026-03-25" // Wednesday
	replanThursday = "2026-03-26"
	replanFriday   = "2026-03-27"
	replanSaturday = "2026-03-28"
)

// replanOut mirrors replanResultDTO — just the fields these tests check,
// not the full week shape (trainingweek_test.go already covers that DTO on
// its own).
type replanOut struct {
	Removed  int `json:"removed"`
	Created  int `json:"created"`
	Adjusted int `json:"adjusted"`
	Week     struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"week"`
}

func newReplanHarness(t *testing.T) *pushHarness {
	t.Helper()
	h := newPushHarness(t)
	h.srv.Clock = replanClock()
	return h
}

func (h *pushHarness) replan(rider string) (*http.Response, replanOut) {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodPost, "/api/training/replan", "")
	var out replanOut
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp, out
}

// baseProfile is wed/fri/sat available, three days — enough for exactly one
// structured slot (placeStructuredDays picks the day furthest from the long
// day, which for three sorted days is always the first: Wednesday, i.e.
// "today" throughout these tests) plus Saturday as the long day and Friday
// as plain endurance. Every test below relies on that placement to know,
// without guessing, which rebuilt day is the hard one.
func (h *pushHarness) baseProfile(ctx context.Context, rider string) {
	h.t.Helper()
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: rider, HoursPerAvailableDay: 1.5, AvailableDays: []string{"wed", "fri", "sat"},
	}); err != nil {
		h.t.Fatal(err)
	}
}

func TestReplanKeepsSurvivorsAndRebuildsTheRest(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()

	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	h.baseProfile(ctx, "wilant")

	// Rider-built: no GoalID at all. Must survive untouched, whatever day
	// it lands on.
	riderBuilt, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "My own ride", Date: replanThursday,
		Description: "Whatever I feel like today.",
		Steps:       []workout.WorkoutStep{{Name: "Ride", Duration: workout.DurationTime, Seconds: 1800, Target: workout.TargetOpen}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Plan-made, but dated before today: must survive even though nobody
	// rode it — replan never touches the past.
	beforeToday, err := h.training.CreateWorkout(ctx, generated("wilant", goal.ID, "Endurance ride", replanMonday, 3600))
	if err != nil {
		t.Fatal(err)
	}

	// Plan-made, dated today, and already ridden: must survive.
	riddenToday, err := h.training.CreateWorkout(ctx, generated("wilant", goal.ID, "Sweet spot", replanToday, 3600))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden-1", Sport: "cycling",
		Date: replanToday, DurationSeconds: 3600, TrainingLoad: 60,
	}); err != nil {
		t.Fatal(err)
	}

	// Plan-made, dated this week, not ridden: must be removed and rebuilt.
	// Named distinctly from whatever the scheduler itself would produce
	// ("Endurance ride"/"Long ride") so a fresh row reusing a recycled slug
	// id (workout ids are slugified names, freed once the old row is
	// deleted — see workout.DB.uniqueID) can never be mistaken for survival.
	if _, err := h.training.CreateWorkout(ctx, generated("wilant", goal.ID, "Placeholder — replace me", replanFriday, 3600)); err != nil {
		t.Fatal(err)
	}

	resp, out := h.replan("wilant")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// Friday is rebuilt, and Saturday (the long day) never had a workout at
	// all — scheduleGoal fills it in too, alongside Friday's rebuild.
	if out.Removed != 1 {
		t.Errorf("removed = %d, want 1 (only Friday's un-ridden session)", out.Removed)
	}
	if out.Created != 2 {
		t.Errorf("created = %d, want 2 (Friday rebuilt + Saturday's long day)", out.Created)
	}

	workouts, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]workout.Workout{}
	for _, wk := range workouts {
		byID[wk.ID] = wk
	}
	if _, ok := byID[riderBuilt.ID]; !ok {
		t.Error("rider-built workout was removed, want it kept")
	}
	if _, ok := byID[beforeToday.ID]; !ok {
		t.Error("a workout dated before today was removed, want it kept")
	}
	if _, ok := byID[riddenToday.ID]; !ok {
		t.Error("today's already-ridden generated workout was removed, want it kept")
	}
	for _, wk := range workouts {
		if wk.Name == "Placeholder — replace me" {
			t.Error("the un-ridden plan-made workout survived, want it replaced")
		}
	}
	for _, wk := range workouts {
		if wk.Date < replanToday {
			continue
		}
		if wk.GoalID != "" && wk.Date < replanToday {
			t.Errorf("a plan-made workout %q is dated before today", wk.Date)
		}
	}
	// Nothing at all before today, from either the rebuild or the long day.
	for _, wk := range workouts {
		if wk.Date < replanMonday {
			t.Errorf("unexpected workout dated %q before the week even started", wk.Date)
		}
	}
}

// A goal with an available day earlier in the week than "today" (Monday,
// here) must never get a workout created for that day during a replan —
// the whole point of scheduleGoal's new fromDate parameter.
func TestReplanNeverCreatesAWorkoutBeforeToday(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()

	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"mon", "wed", "fri"},
	}); err != nil {
		t.Fatal(err)
	}

	resp, _ := h.replan("wilant")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	workouts, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	for _, wk := range workouts {
		if wk.Date == replanMonday {
			t.Errorf("a workout was created for Monday (before today): %+v", wk)
		}
		if wk.Date < replanToday {
			t.Errorf("a workout was created before today: %+v", wk)
		}
	}
	if len(workouts) == 0 {
		t.Fatal("no workouts were created at all — the goal/profile setup is broken, not the fromDate guard")
	}
	_ = goal
}

// A level saved after the first schedule must show up in what a replan
// rebuilds — replan is not just re-running the same numbers.
func TestReplanReflectsALevelChangeInTheRebuiltRung(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()

	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	// More hours than baseProfile's other tests need: workoutlib.Pick can
	// only choose a higher rung if its own (longer) duration actually fits
	// inside the structured slot's time budget — too little time per day
	// and a level bump would be capped right back down to the same rung by
	// the duration ceiling, not by the level itself, which is not what this
	// test is checking.
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 4, AvailableDays: []string{"wed", "fri", "sat"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, out := h.replan("wilant"); out.Created == 0 {
		t.Fatal("first replan created nothing to compare a level change against")
	}

	workouts, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	var before workout.Workout
	found := false
	for _, wk := range workouts {
		if wk.Date == replanToday && wk.Zone != "" {
			before, found = wk, true
		}
	}
	if !found {
		t.Fatal("no structured (zoned) workout was scheduled for today to compare levels against")
	}

	// Push the rider's level in that zone up a full point.
	if err := h.training.SaveLevel(ctx, workout.ProgressionLevel{
		Rider: "wilant", Sport: model.SportCycling, Zone: before.Zone, Level: before.Level + 1,
		Reason: "test bump",
	}); err != nil {
		t.Fatal(err)
	}

	if _, out := h.replan("wilant"); out.Removed == 0 {
		t.Fatal("second replan removed nothing, so nothing was actually rebuilt")
	}

	workouts, err = h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	var after workout.Workout
	found = false
	for _, wk := range workouts {
		if wk.Date == replanToday && wk.Zone == before.Zone {
			after, found = wk, true
		}
	}
	if !found {
		t.Fatal("no workout in the same zone was rebuilt for today")
	}
	if after.Level <= before.Level {
		t.Errorf("rebuilt level = %v, want higher than the original %v after raising the rider's own level", after.Level, before.Level)
	}
	_ = goal
}

// Replanning twice must be a no-op the second time round: the same dates,
// names and levels, and the second call's removed must equal its created —
// it tore down exactly what it just put up and rebuilt the identical thing.
func TestReplanningTwiceIsIdempotent(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()

	if _, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"}); err != nil {
		t.Fatal(err)
	}
	h.baseProfile(ctx, "wilant")

	if resp, _ := h.replan("wilant"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first replan: status = %d", resp.StatusCode)
	}
	first, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	type fingerprint struct {
		date, name, sport string
		level             float64
	}
	snapshot := func(workouts []workout.Workout) map[fingerprint]bool {
		out := make(map[fingerprint]bool, len(workouts))
		for _, wk := range workouts {
			out[fingerprint{wk.Date, wk.Name, string(wk.Sport), wk.Level}] = true
		}
		return out
	}
	before := snapshot(first)

	resp, out := h.replan("wilant")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second replan: status = %d", resp.StatusCode)
	}
	if out.Removed != out.Created {
		t.Errorf("second replan: removed = %d, created = %d, want equal", out.Removed, out.Created)
	}

	second, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot(second)
	if len(before) != len(after) {
		t.Fatalf("workout set size changed: %d before, %d after", len(before), len(after))
	}
	for fp := range before {
		if !after[fp] {
			t.Errorf("replanning twice changed %+v — no longer present after the second call", fp)
		}
	}
}

// Removing a plan-made workout must take its copy off Garmin, and — for a
// rider who has opted in to auto-push — the rebuilt week must go straight
// back onto their account.
func TestReplanRemovesFromGarminAndPushesTheRebuild(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()

	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 1.5, AvailableDays: []string{"wed", "fri", "sat"},
		AutoPushWorkouts: true,
	}); err != nil {
		t.Fatal(err)
	}

	notRidden, err := h.training.CreateWorkout(ctx, generated("wilant", goal.ID, "Endurance ride", replanFriday, 3600))
	if err != nil {
		t.Fatal(err)
	}
	h.push(notRidden.ID)

	h.garmin.workoutCalls = nil
	resp, out := h.replan("wilant")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.Removed == 0 {
		t.Fatal("nothing was removed — the Garmin-removal assertion below would be vacuous")
	}

	calls := strings.Join(h.garmin.workoutCalls, "; ")
	if !strings.Contains(calls, "delete garmin-workout-1") {
		t.Errorf("calls = %q, want the removed workout's copy deleted from Garmin", calls)
	}
	if !strings.Contains(calls, "create ") {
		t.Errorf("calls = %q, want the rebuilt week pushed back (AutoPushWorkouts is on)", calls)
	}
}

// A rest verdict from today's Garmin readiness must ease today's own
// rebuilt hard session — the same guarantee AdaptWorkouts gives an
// unattended tick, now inside a rider's own Replan click.
func TestReplanEasesTodaysSessionWhenReadinessSaysRest(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()

	if _, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"}); err != nil {
		t.Fatal(err)
	}
	h.baseProfile(ctx, "wilant")
	if err := h.training.SaveWellness(ctx, workout.DailyWellness{
		Rider: "wilant", Date: replanToday, ReadinessLevel: "POOR", ReadinessScore: 10, SleepScore: 70,
	}); err != nil {
		t.Fatal(err)
	}

	resp, out := h.replan("wilant")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.Adjusted == 0 {
		t.Fatal("adjusted = 0, want at least today's hard session eased for readiness")
	}

	workouts, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	var eased bool
	for _, wk := range workouts {
		if wk.Date == replanToday && strings.Contains(wk.Description, "Swapped for an easy ride") {
			eased = true
		}
	}
	if !eased {
		t.Error("no workout today carries the readiness swap, want the structured session eased")
	}
}

// One rider's replan must never touch another rider's workouts, plan-made
// or otherwise.
func TestReplanNeverTouchesAnotherRidersWorkouts(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()

	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	h.baseProfile(ctx, "wilant")
	otherGoal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "other", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	h.baseProfile(ctx, "other")

	if _, err := h.training.CreateWorkout(ctx, generated("wilant", goal.ID, "Endurance ride", replanFriday, 3600)); err != nil {
		t.Fatal(err)
	}
	othersWorkout, err := h.training.CreateWorkout(ctx, generated("other", otherGoal.ID, "Endurance ride", replanFriday, 3600))
	if err != nil {
		t.Fatal(err)
	}

	resp, _ := h.replan("wilant")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if _, err := h.training.GetWorkout(ctx, othersWorkout.ID); err != nil {
		t.Errorf("the other rider's workout is gone: %v", err)
	}
	otherWorkouts, err := h.training.ListWorkouts(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	if len(otherWorkouts) != 1 {
		t.Errorf("other rider's workouts = %d, want still exactly 1 — replan must not have scheduled anything for them", len(otherWorkouts))
	}
}

func TestReplanRequiresTrainingPermission(t *testing.T) {
	h := newReplanHarness(t)
	resp := h.as("guest", "guests", http.MethodPost, "/api/training/replan", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// No Remote-User at all is a different failure from the wrong role: the
// proxy-mode middleware itself rejects it before auth.FromContext ever runs,
// the same 401 every other route gets with no identity at all.
func TestReplanRequiresAuthentication(t *testing.T) {
	h := newReplanHarness(t)
	req, err := http.NewRequest(http.MethodPost, h.base+"/api/training/replan", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// Sanity check that scheduler.GeneratedDescription/AdjustedMarker are still
// exactly what isPlanMade in replan.go keys off — if either changes shape,
// this test (not just replan_test.go's own end-to-end ones) should fail
// loudly.
func TestGeneratedDescriptionConstantsUnchanged(t *testing.T) {
	if scheduler.GeneratedDescription == "" || scheduler.AdjustedMarker == "" {
		t.Fatal("scheduler's own markers must not be empty")
	}
}
