package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// All of these run on the replan harness's fixed Wednesday 2026-03-25 (in an
// explicit zone), so they pass under any TZ. The rider has an FTP of 250.

type altOptionOut struct {
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	Zone       string  `json:"zone"`
	Level      float64 `json:"level"`
	Minutes    int     `json:"minutes"`
	TSS        float64 `json:"tss"`
	Difficulty string  `json:"difficulty"`
	Warning    string  `json:"warning"`
}

type altOut struct {
	Options     []altOptionOut `json:"options"`
	HasSnapshot bool           `json:"hasSnapshot"`
}

type altWorkoutOut struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Date               string  `json:"date"`
	GoalID             string  `json:"goalId"`
	Zone               string  `json:"zone"`
	Level              float64 `json:"level"`
	Description        string  `json:"description"`
	Indoor             bool    `json:"indoor"`
	Swapped            bool    `json:"swapped"`
	HasPlannedSnapshot bool    `json:"hasPlannedSnapshot"`
	PlannedSeconds     float64 `json:"plannedSeconds"`
}

func (o altOut) kinds() []string {
	var out []string
	for _, opt := range o.Options {
		out = append(out, opt.Kind)
	}
	return out
}

func (o altOut) find(kind string) (altOptionOut, bool) {
	for _, opt := range o.Options {
		if opt.Kind == kind {
			return opt, true
		}
	}
	return altOptionOut{}, false
}

var altProfile = workout.RiderProfile{FTPWatts: 250}

// altRung is a plan-made structured session on rung level of the cycling zone.
func (h *pushHarness) altRung(goal, date, zone string, level int) workout.Workout {
	h.t.Helper()
	l, ok := workoutlib.LadderFor(model.SportCycling, zone)
	if !ok {
		h.t.Fatalf("no ladder for %s", zone)
	}
	req := workoutlib.Instantiate(l, l.Rungs[level-1], altProfile)
	req.Rider, req.GoalID, req.Date, req.Description = "wilant", goal, date, scheduler.GeneratedDescription
	w, err := h.training.CreateWorkout(context.Background(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

// altEndurance is a plan-made endurance ride of the given length.
func (h *pushHarness) altEndurance(goal, date string, hours float64) workout.Workout {
	h.t.Helper()
	req := scheduler.BuildEnduranceSession(hours, false, model.SportCycling, altProfile)
	req.Rider, req.GoalID, req.Date = "wilant", goal, date
	w, err := h.training.CreateWorkout(context.Background(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

func (h *pushHarness) altLevel(zone string, level float64) {
	h.t.Helper()
	if err := h.training.SaveLevel(context.Background(), workout.ProgressionLevel{
		Rider: "wilant", Sport: model.SportCycling, Zone: workout.Zone(zone), Level: level, Reason: "test",
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *pushHarness) altGet(user, groups, id, query string) (*http.Response, altOut) {
	h.t.Helper()
	resp := h.as(user, groups, http.MethodGet, "/api/training/workouts/"+id+"/alternates"+query, "")
	var out altOut
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp, out
}

func (h *pushHarness) altSwap(user, groups, id, kind string) (*http.Response, altWorkoutOut) {
	h.t.Helper()
	return h.altDecode(h.as(user, groups, http.MethodPost, "/api/training/workouts/"+id+"/alternates", `{"kind":"`+kind+`"}`))
}

func (h *pushHarness) altRevert(user, groups, id string) (*http.Response, altWorkoutOut) {
	h.t.Helper()
	return h.altDecode(h.as(user, groups, http.MethodPost, "/api/training/workouts/"+id+"/alternates/revert", ""))
}

func (h *pushHarness) altDecode(resp *http.Response) (*http.Response, altWorkoutOut) {
	h.t.Helper()
	var out altWorkoutOut
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp, out
}

func altSteps(t *testing.T, zone string, level int) []workout.WorkoutStep {
	t.Helper()
	l, _ := workoutlib.LadderFor(model.SportCycling, zone)
	return workoutlib.Instantiate(l, l.Rungs[level-1], altProfile).Steps
}

func TestAlternatesListsTheOptionsWithTSSAndDifficultyAndWritesNothing(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5.2)
	w := h.altRung(goal, indoorFuture, "threshold", 4)

	resp, out := h.altGet("wilant", "cyclists", w.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// L 5.2 caps harder at 6; rung 4 is 68 min so a longer one needs 85+ min
	// and there is none at or below the cap.
	if got := strings.Join(out.kinds(), ","); got != "easier,harder,shorter" {
		t.Fatalf("kinds = %s, want easier,harder,shorter", got)
	}
	easier, _ := out.find("easier")
	harder, _ := out.find("harder")
	shorter, _ := out.find("shorter")
	if easier.Level != 3 || harder.Level != 5 || shorter.Level != 2 {
		t.Errorf("levels = %v %v %v, want 3 5 2", easier.Level, harder.Level, shorter.Level)
	}
	if easier.Difficulty != "Recovery" || harder.Difficulty != "Productive" || shorter.Difficulty != "Recovery" {
		t.Errorf("difficulty = %s %s %s", easier.Difficulty, harder.Difficulty, shorter.Difficulty)
	}
	if harder.TSS <= 0 || harder.Minutes != 71 || harder.Zone != "threshold" || !strings.HasPrefix(harder.Name, "Threshold") {
		t.Errorf("harder = %+v, want a TSS, 71 min, zone threshold", harder)
	}
	if out.HasSnapshot {
		t.Error("nothing has been swapped yet")
	}
	if got := h.stored(w.ID); got.UpdatedAt != w.UpdatedAt || got.PlannedSnapshot != nil {
		t.Errorf("GET wrote to the workout: %+v", got)
	}
}

func TestAlternatesAreEmptyForWhatIsNotAPlannedSession(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	ctx := context.Background()
	past := h.altRung(goal, indoorYesterday, "threshold", 4)
	ridden := h.altRung(goal, indoorToday, "threshold", 4)
	if _, err := h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden", Sport: "cycling", Date: indoorToday, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	test, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal, Sport: model.SportCycling, Name: "FTP Test (ramp)", Date: indoorFuture,
		Description: scheduler.GeneratedDescription, TestProtocol: "ramp", Zone: workout.ZoneThreshold, Level: 4,
		Steps: altSteps(t, "threshold", 4),
	})
	if err != nil {
		t.Fatal(err)
	}
	riderBuilt, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "My ride", Date: indoorFuture, Description: "mine",
		Zone: workout.ZoneThreshold, Level: 4, Steps: altSteps(t, "threshold", 4),
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, id := range map[string]string{"past": past.ID, "ridden": ridden.ID, "test": test.ID, "rider-built": riderBuilt.ID} {
		resp, out := h.altGet("wilant", "cyclists", id, "")
		if resp.StatusCode != http.StatusOK || len(out.Options) != 0 {
			t.Errorf("%s: GET = %d with %d options, want 200 and none", name, resp.StatusCode, len(out.Options))
		}
		if resp, _ := h.altSwap("wilant", "cyclists", id, "easier"); resp.StatusCode != http.StatusConflict {
			t.Errorf("%s: swap = %d, want 409", name, resp.StatusCode)
		}
		if got := h.stored(id); got.PlannedSnapshot != nil || strings.Contains(got.Description, scheduler.SwappedMarker) {
			t.Errorf("%s: was changed despite the refusal: %+v", name, got)
		}
	}
}

func TestAHarderOptionCarriesAReadinessWarningOnATiredDayOnly(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5)
	todays := h.altRung(goal, indoorToday, "threshold", 4)
	inAWeek := h.altRung(goal, "2026-04-01", "threshold", 4)

	_, rested := h.altGet("wilant", "cyclists", todays.ID, "")
	if o, _ := rested.find("harder"); o.Warning != "" {
		t.Error("a rested rider got a readiness warning")
	}
	if err := h.training.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: indoorToday, ReadinessLevel: "POOR", ReadinessScore: 10,
	}); err != nil {
		t.Fatal(err)
	}
	_, out := h.altGet("wilant", "cyclists", todays.ID, "")
	harder, ok := out.find("harder")
	if !ok || harder.Warning != "Readiness is low today" {
		t.Errorf("harder = %+v (%v), want the readiness warning", harder, ok)
	}
	if easier, _ := out.find("easier"); easier.Warning != "" {
		t.Errorf("an easier option was warned about: %q", easier.Warning)
	}
	// Still offered: the rider decides.
	if resp, _ := h.altSwap("wilant", "cyclists", todays.ID, "harder"); resp.StatusCode != http.StatusOK {
		t.Errorf("harder on a tired day = %d, want 200", resp.StatusCode)
	}
	// A session a week away is not today's or tomorrow's.
	_, out = h.altGet("wilant", "cyclists", inAWeek.ID, "")
	if harder, _ := out.find("harder"); harder.Warning != "" {
		t.Errorf("a session next week was warned about: %q", harder.Warning)
	}
}

func TestSwapEditsInPlaceMarksItTouchedKeepsTheFirstSnapshotAndNeverMovesALevel(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5.2)
	orig := h.altRung(goal, indoorFuture, "threshold", 4)
	levelsBefore, _ := h.training.ListLevels(context.Background(), "wilant")

	resp, out := h.altSwap("wilant", "cyclists", orig.ID, "harder")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if out.ID != orig.ID || out.Date != orig.Date || out.GoalID != goal || out.Level != 5 || out.Zone != "threshold" {
		t.Errorf("swapped = %+v, want the same id, date and goal at rung 5", out)
	}
	if !out.Swapped || !out.HasPlannedSnapshot {
		t.Errorf("swapped=%v hasPlannedSnapshot=%v, want both true", out.Swapped, out.HasPlannedSnapshot)
	}
	got := h.stored(orig.ID)
	if !reflect.DeepEqual(got.Steps, altSteps(t, "threshold", 5)) {
		t.Error("the steps are not rung 5's")
	}
	if !strings.HasPrefix(got.Description, scheduler.GeneratedDescription) ||
		!strings.Contains(got.Description, scheduler.SwappedMarker+" harder, was "+orig.Name+" (") {
		t.Errorf("description = %q, want the generated prefix and a swap note naming the old session", got.Description)
	}
	if scheduler.IsGenerated(got) {
		t.Error("a swapped session must not be IsGenerated: adaptation would rewrite it")
	}
	snap := got.PlannedSnapshot
	if snap == nil || snap.Name != orig.Name || snap.Level != 4 || snap.Description != orig.Description ||
		!reflect.DeepEqual(snap.Steps, orig.Steps) || snap.Indoor || snap.OutdoorSteps != nil {
		t.Fatalf("snapshot = %+v, want the plan's version", snap)
	}
	if levelsAfter, _ := h.training.ListLevels(context.Background(), "wilant"); !reflect.DeepEqual(levelsBefore, levelsAfter) {
		t.Errorf("a swap moved a level: %+v -> %+v", levelsBefore, levelsAfter)
	}

	// Swaps are repeatable, and the snapshot stays the plan's, not the last swap's.
	if resp, _ := h.altSwap("wilant", "cyclists", orig.ID, "easier"); resp.StatusCode != http.StatusOK {
		t.Fatalf("second swap = %d", resp.StatusCode)
	}
	again := h.stored(orig.ID)
	if again.Level != 4 || again.PlannedSnapshot == nil || !reflect.DeepEqual(*again.PlannedSnapshot, *snap) {
		t.Errorf("after a second swap level=%v snapshot=%+v, want the first snapshot kept", again.Level, again.PlannedSnapshot)
	}
	if strings.Count(again.Description, scheduler.SwappedMarker) != 2 {
		t.Errorf("description = %q, want one note per swap", again.Description)
	}
}

func TestSwapRefusesWhatIsNoLongerOfferedAndBadKinds(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 4)
	rungOne := h.altRung(goal, indoorFuture, "threshold", 1)
	atCap := h.altRung(goal, "2026-04-01", "threshold", 5)

	if resp, _ := h.altSwap("wilant", "cyclists", rungOne.ID, "easier"); resp.StatusCode != http.StatusConflict {
		t.Errorf("easier from rung 1 = %d, want 409", resp.StatusCode)
	}
	if resp, _ := h.altSwap("wilant", "cyclists", atCap.ID, "harder"); resp.StatusCode != http.StatusConflict {
		t.Errorf("harder at the cap = %d, want 409 (the rider's level changed since the menu opened)", resp.StatusCode)
	}
	if resp, _ := h.altSwap("wilant", "cyclists", rungOne.ID, "sideways"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an unknown kind = %d, want 400", resp.StatusCode)
	}
	if got := h.stored(rungOne.ID); got.PlannedSnapshot != nil || got.UpdatedAt != rungOne.UpdatedAt {
		t.Error("a refused swap changed the workout")
	}
}

func TestRevertRestoresTheSnapshotExactlyAndIsIdempotent(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5.2)
	orig := h.altRung(goal, indoorFuture, "threshold", 4)

	if resp, out := h.altRevert("wilant", "cyclists", orig.ID); resp.StatusCode != http.StatusOK || out.Name != orig.Name {
		t.Errorf("revert with no snapshot = %d %+v, want 200 unchanged", resp.StatusCode, out)
	}
	if got := h.stored(orig.ID); got.UpdatedAt != orig.UpdatedAt {
		t.Error("an idempotent revert still wrote")
	}

	h.altSwap("wilant", "cyclists", orig.ID, "harder")
	h.altSwap("wilant", "cyclists", orig.ID, "shorter")
	resp, out := h.altRevert("wilant", "cyclists", orig.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revert = %d", resp.StatusCode)
	}
	got := h.stored(orig.ID)
	if got.Name != orig.Name || got.Zone != orig.Zone || got.Level != orig.Level || !reflect.DeepEqual(got.Steps, orig.Steps) ||
		got.Indoor || got.OutdoorSteps != nil || got.PlannedSnapshot != nil {
		t.Errorf("reverted = %+v, want the plan's version with no snapshot", got)
	}
	if out.HasPlannedSnapshot || !out.Swapped {
		t.Errorf("dto swapped=%v hasSnapshot=%v, want swapped kept (touched) and no snapshot", out.Swapped, out.HasPlannedSnapshot)
	}
	if !strings.Contains(got.Description, "Back to the planned version.") || !strings.Contains(got.Description, scheduler.SwappedMarker) {
		t.Errorf("description = %q, want the revert note and the marker kept", got.Description)
	}
	if scheduler.IsGenerated(got) {
		t.Error("a reverted session stays rider-touched")
	}

	// And again: nothing left to restore.
	before := h.stored(orig.ID)
	if resp, _ := h.altRevert("wilant", "cyclists", orig.ID); resp.StatusCode != http.StatusOK {
		t.Errorf("second revert = %d", resp.StatusCode)
	}
	if after := h.stored(orig.ID); after.UpdatedAt != before.UpdatedAt || after.Description != before.Description {
		t.Error("a second revert changed the session")
	}
}

func TestRevertIsRefusedForARiddenOrPastSession(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5.2)
	ctx := context.Background()
	past := h.altRung(goal, indoorFuture, "threshold", 4)
	today := h.altRung(goal, indoorToday, "threshold", 4)
	h.altSwap("wilant", "cyclists", past.ID, "easier")
	h.altSwap("wilant", "cyclists", today.ID, "easier")

	// Time moves on: the first is now yesterday's, the second is ridden.
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 29, 9, 0, 0, 0, time.FixedZone("CEST", 2*3600)) }
	if resp, _ := h.altRevert("wilant", "cyclists", past.ID); resp.StatusCode != http.StatusConflict {
		t.Errorf("revert of a past session = %d, want 409", resp.StatusCode)
	}
	h.srv.Clock = replanClock()
	if _, err := h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden", Sport: "cycling", Date: indoorToday, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.altRevert("wilant", "cyclists", today.ID); resp.StatusCode != http.StatusConflict {
		t.Errorf("revert of a ridden session = %d, want 409", resp.StatusCode)
	}
	if h.stored(today.ID).PlannedSnapshot == nil {
		t.Error("a refused revert cleared the snapshot")
	}
}

func TestAnIndoorSessionStaysIndoorThroughASwapAndRevertRestoresBothForms(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	orig := h.indoorRide(goal, indoorFuture, 9600) // a 3 h ride: 2h20 after a shorter swap
	if resp, out := h.convert("wilant", orig.ID, ""); resp.StatusCode != http.StatusOK || !out.Indoor {
		t.Fatalf("convert = %d indoor=%v", resp.StatusCode, out.Indoor)
	}
	before := h.stored(orig.ID)

	_, listed := h.altGet("wilant", "cyclists", orig.ID, "")
	shorterOpt, ok := listed.find("shorter")
	if !ok {
		t.Fatalf("kinds = %v, want a shorter option for an indoor session", listed.kinds())
	}
	if shorterOpt.Minutes >= 140 {
		t.Errorf("shorter shows %d min; an indoor session is shown as its trainer version, under the 140 min outdoor form", shorterOpt.Minutes)
	}

	resp, out := h.altSwap("wilant", "cyclists", orig.ID, "shorter")
	if resp.StatusCode != http.StatusOK || !out.Indoor {
		t.Fatalf("swap = %d indoor=%v, want 200 and still indoor", resp.StatusCode, out.Indoor)
	}
	got := h.stored(orig.ID)
	if got.OutdoorSteps == nil || workout.PlannedSeconds(*got.OutdoorSteps) != 8400 {
		t.Fatalf("outdoor steps = %+v, want the rebuilt outdoor shorter ride (2h20)", got.OutdoorSteps)
	}
	if workout.PlannedSeconds(got.Steps) >= 8400 {
		t.Errorf("indoor steps are %v s, want the trainer version, shorter than the outdoor one", workout.PlannedSeconds(got.Steps))
	}
	assertTrainerShape(t, got.Steps)
	if strings.Count(got.Description, "Indoor version of") != 1 {
		t.Errorf("description = %q, want exactly one, current, indoor note", got.Description)
	}
	snap := got.PlannedSnapshot
	if snap == nil || !snap.Indoor || snap.OutdoorSteps == nil || !reflect.DeepEqual(*snap.OutdoorSteps, *before.OutdoorSteps) ||
		!reflect.DeepEqual(snap.Steps, before.Steps) {
		t.Fatalf("snapshot = %+v, want the pre-swap indoor state", snap)
	}

	if resp, _ := h.altRevert("wilant", "cyclists", orig.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("revert = %d", resp.StatusCode)
	}
	back := h.stored(orig.ID)
	if !back.Indoor || !reflect.DeepEqual(back.Steps, before.Steps) || back.OutdoorSteps == nil ||
		!reflect.DeepEqual(*back.OutdoorSteps, *before.OutdoorSteps) {
		t.Errorf("reverted indoor=%v; want the pre-swap trainer steps and outdoor steps back", back.Indoor)
	}
}

func TestAnOutdoorSessionSwappedStaysOutdoor(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	orig := h.indoorRide(goal, indoorFuture, 13200)
	if resp, out := h.altSwap("wilant", "cyclists", orig.ID, "shorter"); resp.StatusCode != http.StatusOK || out.Indoor {
		t.Fatalf("swap = %d indoor=%v, want an outdoor swap to stay outdoor", resp.StatusCode, out.Indoor)
	}
	if got := h.stored(orig.ID); got.OutdoorSteps != nil || workout.PlannedSeconds(got.Steps) != 11100 {
		t.Errorf("swapped outdoor session: outdoor=%v planned=%v", got.OutdoorSteps, workout.PlannedSeconds(got.Steps))
	}
}

func TestSwappingTodaysPushedSessionUpdatesItsCopyAndAFutureOneDoesNot(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	today := h.indoorRide(goal, indoorToday, 13200)
	future := h.indoorRide(goal, indoorFuture, 13200)
	h.push(today.ID)
	h.push(future.ID)
	h.garmin.workoutCalls = nil

	if resp, _ := h.altSwap("wilant", "cyclists", today.ID, "shorter"); resp.StatusCode != http.StatusOK {
		t.Fatalf("swap = %d", resp.StatusCode)
	}
	if got := h.calls(); got != "update garmin-workout-1" {
		t.Errorf("calls after swapping today's = %q, want the copy updated once", got)
	}
	h.garmin.workoutCalls = nil
	if resp, _ := h.altRevert("wilant", "cyclists", today.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("revert = %d", resp.StatusCode)
	}
	if got := h.calls(); got != "update garmin-workout-1" {
		t.Errorf("calls after reverting today's = %q, want the copy updated once", got)
	}

	h.garmin.workoutCalls = nil
	if resp, _ := h.altSwap("wilant", "cyclists", future.ID, "shorter"); resp.StatusCode != http.StatusOK {
		t.Fatalf("future swap = %d", resp.StatusCode)
	}
	if got := h.calls(); got != "" {
		t.Errorf("swapping a future session pushed %q, want nothing (it pushes on its day)", got)
	}
}

func TestReplanLeavesASwappedSessionAlone(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	h.baseProfile(ctx, "wilant")
	fri := h.altEndurance(goal.ID, replanFriday, 1)
	if resp, _ := h.altSwap("wilant", "cyclists", fri.ID, "longer"); resp.StatusCode != http.StatusOK {
		t.Fatalf("swap = %d", resp.StatusCode)
	}
	swapped := h.stored(fri.ID)

	if resp, _ := h.replan("wilant"); resp.StatusCode != http.StatusOK {
		t.Fatalf("replan = %d", resp.StatusCode)
	}
	after, err := h.training.GetWorkout(ctx, fri.ID)
	if err != nil {
		t.Fatalf("the swapped session was deleted by replan: %v", err)
	}
	if !reflect.DeepEqual(after, swapped) {
		t.Errorf("replan changed a swapped session: %+v -> %+v", swapped, after)
	}
	onFriday := 0
	all, _ := h.training.ListWorkouts(ctx, "wilant")
	for _, w := range all {
		if w.Date == replanFriday {
			onFriday++
		}
	}
	if onFriday != 1 {
		t.Errorf("%d sessions on the swapped day, want just the swapped one (its day is still taken)", onFriday)
	}
}

func TestRecoveryAndTaperWeeksOfferNoHarderAndNoStructuredLongerButEnduranceLonger(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", FTPWatts: 250, HoursPerAvailableDay: 2, AvailableDays: []string{"tue", "thu", "sat"},
	}); err != nil {
		t.Fatal(err)
	}
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2026-09-01"})
	if err != nil {
		t.Fatal(err)
	}
	h.altLevel("threshold", 8)
	profile, _, _ := h.training.GetProfile(ctx, "wilant")
	plan, err := periodization.Build(goal, profile, replanClock()())
	if err != nil {
		t.Fatal(err)
	}
	var recovery, taper, ordinary string
	for _, wk := range plan.Weeks {
		start, _ := time.Parse("2006-01-02", wk.StartDate)
		day := start.AddDate(0, 0, 1).Format("2006-01-02")
		switch {
		case wk.StartDate <= indoorToday:
		case wk.Recovery && recovery == "":
			recovery = day
		case wk.Phase == periodization.PhaseTaper && taper == "":
			taper = day
		case !wk.Recovery && wk.Phase != periodization.PhaseTaper && ordinary == "":
			ordinary = day
		}
	}
	if recovery == "" || taper == "" || ordinary == "" {
		t.Fatalf("setup: recovery=%q taper=%q ordinary=%q, want one of each in the plan", recovery, taper, ordinary)
	}

	// Threshold rung 4 (68 min) for a rider at 8: in an ordinary week every
	// option is on offer (longer is rung 8, 86 min, within the cap of 9).
	ord := h.altRung(goal.ID, ordinary, "threshold", 4)
	if _, out := h.altGet("wilant", "cyclists", ord.ID, ""); strings.Join(out.kinds(), ",") != "easier,harder,shorter,longer" {
		t.Fatalf("ordinary week kinds = %v, want all four", out.kinds())
	}
	for name, date := range map[string]string{"recovery": recovery, "taper": taper} {
		s := h.altRung(goal.ID, date, "threshold", 4)
		_, out := h.altGet("wilant", "cyclists", s.ID, "")
		if got := strings.Join(out.kinds(), ","); got != "easier,shorter" {
			t.Errorf("%s week kinds = %s, want easier,shorter only", name, got)
		}
		if resp, _ := h.altSwap("wilant", "cyclists", s.ID, "harder"); resp.StatusCode != http.StatusConflict {
			t.Errorf("%s week: harder = %d, want 409", name, resp.StatusCode)
		}
		e := h.altEndurance(goal.ID, date, 2)
		_, eo := h.altGet("wilant", "cyclists", e.ID, "")
		if _, ok := eo.find("longer"); !ok {
			t.Errorf("%s week: an endurance ride lost its longer option: %v", name, eo.kinds())
		}
	}
}

func TestAlternatesAreOwnerOnly(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5.2)
	w := h.altRung(goal, indoorFuture, "threshold", 4)

	if resp, _ := h.altGet("someone-else", "cyclists", w.ID, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another rider's GET = %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.altSwap("someone-else", "cyclists", w.ID, "easier"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another rider's swap = %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.altRevert("someone-else", "cyclists", w.ID); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another rider's revert = %d, want 404", resp.StatusCode)
	}
	if resp, _ := h.altGet("watcher", "viewers", w.ID, ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer's GET = %d, want 403", resp.StatusCode)
	}
	if resp, _ := h.altSwap("watcher", "viewers", w.ID, "easier"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer's swap = %d, want 403", resp.StatusCode)
	}
	if resp, _ := h.altGet("wilant", "cyclists", "no-such-workout", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown workout's GET = %d, want 404", resp.StatusCode)
	}
	if got := h.stored(w.ID); got.PlannedSnapshot != nil {
		t.Error("someone else's request changed the workout")
	}
}

func TestSwapLogsRiderWorkoutAndKindOnly(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5.2)
	w := h.altRung(goal, indoorToday, "threshold", 4)
	if err := h.training.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: indoorToday, ReadinessLevel: "POOR", ReadinessScore: 10,
	}); err != nil {
		t.Fatal(err)
	}
	var records []spyRecord
	h.srv.Log = slog.New(spyHandler{records: &records})

	h.altGet("wilant", "cyclists", w.ID, "")
	if resp, _ := h.altSwap("wilant", "cyclists", w.ID, "harder"); resp.StatusCode != http.StatusOK {
		t.Fatalf("swap = %d", resp.StatusCode)
	}
	found := false
	for _, r := range records {
		for k := range r.attrs {
			if k != "rider" && k != "workout" && k != "kind" && k != "err" {
				t.Errorf("log %q carries attribute %q; only rider, workout, kind and err may appear", r.msg, k)
			}
		}
		if r.msg == "workout swapped" {
			found = true
			if r.attrs["rider"] != "wilant" || r.attrs["workout"] != w.ID || r.attrs["kind"] != "harder" {
				t.Errorf("swap log attrs = %v", r.attrs)
			}
		}
	}
	if !found {
		t.Error("no 'workout swapped' log line")
	}
}

// After a swap, automatic adaptation leaves the session alone: today's
// readiness easing, and the ease-tomorrow action.
func TestAdaptationAndTheTomorrowForecastLeaveASwappedSessionAlone(t *testing.T) {
	h := newTomorrowHarness(t)
	ctx := context.Background()
	todays := h.hard(tmToday)
	tomorrow := h.hard(tmTomorrow)
	// This clock is in a recovery week of the undated goal, so only easier and
	// shorter are on offer.
	for _, id := range []string{todays.ID, tomorrow.ID} {
		if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+id+"/alternates", `{"kind":"easier"}`); resp.StatusCode != http.StatusOK {
			t.Fatalf("swap %s = %d %s", id, resp.StatusCode, readAll(t, resp))
		}
	}
	swappedToday, _ := h.store.GetWorkout(ctx, todays.ID)
	swappedTomorrow, _ := h.store.GetWorkout(ctx, tomorrow.ID)

	if err := h.store.SaveWellness(ctx, workout.DailyWellness{Rider: "wilant", Date: tmToday, ReadinessLevel: "POOR", ReadinessScore: 10}); err != nil {
		t.Fatal(err)
	}
	h.snapshot(40, 100)
	h.srv.AdaptWorkouts(ctx)
	if status, _ := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusConflict {
		t.Errorf("ease tomorrow on a swapped session = %d, want 409", status)
	}

	for _, want := range []workout.Workout{swappedToday, swappedTomorrow} {
		got, _ := h.store.GetWorkout(ctx, want.ID)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("adaptation rewrote a swapped session: %+v -> %+v", want, got)
		}
	}
}
