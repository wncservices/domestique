package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The replan harness's clock is Wednesday 2026-03-25.
const (
	indoorYesterday = "2026-03-24"
	indoorToday     = replanToday
	indoorFuture    = replanSaturday
)

type indoorOut struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	Date                  string  `json:"date"`
	GoalID                string  `json:"goalId"`
	Zone                  string  `json:"zone"`
	Level                 float64 `json:"level"`
	Description           string  `json:"description"`
	Indoor                bool    `json:"indoor"`
	CanRevertIndoor       bool    `json:"canRevertIndoor"`
	PlannedSeconds        float64 `json:"plannedSeconds"`
	OutdoorPlannedSeconds float64 `json:"outdoorPlannedSeconds"`
}

type indoorPreviewOut struct {
	Note            string  `json:"note"`
	ERG             bool    `json:"erg"`
	Changed         bool    `json:"changed"`
	PlannedSeconds  float64 `json:"plannedSeconds"`
	OriginalSeconds float64 `json:"originalSeconds"`
}

// longRide is a plan-made 4 h long ride the way the scheduler builds one:
// warmup, a power range, cooldown, all on the road's terms.
func longRideSteps(mainSecs float64) []workout.WorkoutStep {
	p := func(name string, in workout.Intensity, secs float64) workout.WorkoutStep {
		return workout.WorkoutStep{Name: name, Intensity: in, Duration: workout.DurationTime, Seconds: secs,
			Target: workout.TargetPower, TargetLow: 137.5, TargetHigh: 187.5}
	}
	return []workout.WorkoutStep{
		p("Warmup", workout.IntensityWarmup, 300), p("Warmup", workout.IntensityWarmup, 300),
		p("Long ride", workout.IntensityActive, mainSecs), p("Cooldown", workout.IntensityCooldown, 600),
	}
}

func (h *pushHarness) indoorSetup(smart bool) string {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{Rider: "wilant", FTPWatts: 250, SmartTrainer: smart}); err != nil {
		h.t.Fatal(err)
	}
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		h.t.Fatal(err)
	}
	return goal.ID
}

func (h *pushHarness) indoorRide(goalID, date string, mainSecs float64) workout.Workout {
	h.t.Helper()
	w, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goalID, Sport: model.SportCycling, Name: "Long ride", Date: date,
		Description: scheduler.GeneratedDescription, Zone: workout.ZoneEndurance, Steps: longRideSteps(mainSecs),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

func (h *pushHarness) convert(user, id, query string) (*http.Response, indoorOut) {
	h.t.Helper()
	return h.decodeIndoor(h.as(user, "cyclists", http.MethodPost, "/api/training/workouts/"+id+"/indoor"+query, ""))
}

func (h *pushHarness) revert(user, id string) (*http.Response, indoorOut) {
	h.t.Helper()
	return h.decodeIndoor(h.as(user, "cyclists", http.MethodDelete, "/api/training/workouts/"+id+"/indoor", ""))
}

func (h *pushHarness) decodeIndoor(resp *http.Response) (*http.Response, indoorOut) {
	h.t.Helper()
	var out indoorOut
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp, out
}

func (h *pushHarness) stored(id string) workout.Workout {
	h.t.Helper()
	w, err := h.training.GetWorkout(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

func TestConvertMakesTheWorkoutIndoorInPlace(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	orig := h.indoorRide(goal, indoorFuture, 13200)

	resp, out := h.convert("wilant", orig.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !out.Indoor || out.ID != orig.ID || out.Date != orig.Date || out.GoalID != goal ||
		out.Zone != string(workout.ZoneEndurance) || out.Name != "Long ride" {
		t.Errorf("converted = %+v, want the same workout flagged indoor", out)
	}
	if out.PlannedSeconds != 8400 {
		t.Errorf("planned = %v, want 8400 (a 4 h ride ends about 2h20)", out.PlannedSeconds)
	}
	if out.OutdoorPlannedSeconds != 14400 {
		t.Errorf("outdoor planned = %v, want the original 14400", out.OutdoorPlannedSeconds)
	}
	if !strings.Contains(out.Description, "Indoor version of Long ride (4h00), 2h20 on the trainer.") {
		t.Errorf("description = %q, want the conversion note", out.Description)
	}

	got := h.stored(orig.ID)
	if !got.Indoor || got.OutdoorSteps == nil || !reflect.DeepEqual(*got.OutdoorSteps, orig.Steps) {
		t.Errorf("stored outdoor steps = %+v, want the original steps", got.OutdoorSteps)
	}
	if strings.Contains(got.Description, scheduler.AdjustedMarker) || !scheduler.IsGenerated(got) {
		t.Errorf("conversion must not add the adjusted marker: %q", got.Description)
	}
	if all, _ := h.training.ListWorkouts(context.Background(), "wilant"); len(all) != 1 {
		t.Errorf("%d workouts after converting, want 1 (in place, not a copy)", len(all))
	}
}

func TestConvertTwiceKeepsTheFirstOriginalAndShortensNothingMore(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	orig := h.indoorRide(goal, indoorFuture, 13200)

	h.convert("wilant", orig.ID, "")
	first := h.stored(orig.ID)
	resp, out := h.convert("wilant", orig.ID, "")
	if resp.StatusCode != http.StatusOK || !out.Indoor {
		t.Fatalf("second convert status = %d indoor = %v", resp.StatusCode, out.Indoor)
	}
	second := h.stored(orig.ID)
	if !reflect.DeepEqual(first.Steps, second.Steps) || first.Description != second.Description {
		t.Errorf("a second convert changed the workout: %+v -> %+v", first, second)
	}
	if second.OutdoorSteps == nil || !reflect.DeepEqual(*second.OutdoorSteps, orig.Steps) {
		t.Errorf("outdoor steps after a second convert = %+v, want the first original kept", second.OutdoorSteps)
	}
}

func TestConvertPreviewChangesNothing(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(true)
	orig := h.indoorRide(goal, indoorFuture, 13200)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+orig.ID+"/indoor?preview=1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var p indoorPreviewOut
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.PlannedSeconds != 8400 || p.OriginalSeconds != 14400 || !p.ERG || !p.Changed ||
		!strings.Contains(p.Note, "Indoor version of Long ride") {
		t.Errorf("preview = %+v", p)
	}
	if got := h.stored(orig.ID); got.Indoor || got.OutdoorSteps != nil || !reflect.DeepEqual(got.Steps, orig.Steps) || got.Description != orig.Description {
		t.Errorf("preview changed the workout: %+v", got)
	}
}

func TestSmartTrainerRiderGetsMidpointTargetsOthersKeepRanges(t *testing.T) {
	for _, smart := range []bool{true, false} {
		h := newReplanHarness(t)
		goal := h.indoorSetup(smart)
		orig := h.indoorRide(goal, indoorFuture, 3600)
		h.convert("wilant", orig.ID, "")
		main := h.stored(orig.ID).Steps[2]
		collapsed := main.TargetLow == main.TargetHigh
		if collapsed != smart {
			t.Errorf("smart=%v: main step %v-%v, collapsed=%v", smart, main.TargetLow, main.TargetHigh, collapsed)
		}
	}
}

func TestConvertIsRefusedForRiddenPastRunningAndOtherRiders(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	ctx := context.Background()

	past := h.indoorRide(goal, indoorYesterday, 3600)
	today := h.indoorRide(goal, indoorToday, 3600)
	future := h.indoorRide(goal, indoorFuture, 3600)
	run, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportRunning, Name: "Easy run", Date: indoorFuture,
		Steps: []workout.WorkoutStep{{Name: "Run", Duration: workout.DurationTime, Seconds: 1800, Target: workout.TargetOpen}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden", Sport: "cycling", Date: indoorToday, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		user, id string
		want     int
	}{
		"past date":        {"wilant", past.ID, http.StatusConflict},
		"ridden today":     {"wilant", today.ID, http.StatusConflict},
		"running":          {"wilant", run.ID, http.StatusUnprocessableEntity},
		"another riders":   {"someone-else", future.ID, http.StatusNotFound},
		"unknown workout":  {"wilant", "no-such-workout", http.StatusNotFound},
		"the future works": {"wilant", future.ID, http.StatusOK},
	} {
		if resp, _ := h.convert(tc.user, tc.id, ""); resp.StatusCode != tc.want {
			t.Errorf("%s: status = %d, want %d", name, resp.StatusCode, tc.want)
		}
	}
	for _, id := range []string{past.ID, today.ID, run.ID} {
		if h.stored(id).Indoor {
			t.Errorf("%s was converted despite the refusal", id)
		}
	}
	// A preview is refused the same way: nothing to preview for a past ride.
	if resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+past.ID+"/indoor?preview=1", ""); resp.StatusCode != http.StatusConflict {
		t.Errorf("preview of a past ride = %d, want 409", resp.StatusCode)
	}
}

func TestRevertRestoresTheOriginalStepsExactly(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(true)
	ctx := context.Background()
	// Nested repeat blocks and distance/open steps: the conversion rewrites all of them.
	nested := []workout.WorkoutStep{
		{Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationOpen},
		{Name: "Outer", Repeat: 2, Steps: []workout.WorkoutStep{
			{Name: "Inner", Repeat: 3, Steps: []workout.WorkoutStep{
				{Name: "On", Intensity: workout.IntensityInterval, Duration: workout.DurationDistance, Meters: 1000, Target: workout.TargetPower, TargetLow: 250, TargetHigh: 300},
			}},
			{Name: "Off", Intensity: workout.IntensityRecovery, Duration: workout.DurationOpen},
		}},
	}
	orig, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal, Sport: model.SportCycling, Name: "Hill repeats", Date: indoorFuture,
		Description: scheduler.GeneratedDescription, Zone: workout.ZoneThreshold, Level: 3, Steps: nested,
	})
	if err != nil {
		t.Fatal(err)
	}

	h.convert("wilant", orig.ID, "")
	converted := h.stored(orig.ID)
	if reflect.DeepEqual(converted.Steps, nested) {
		t.Fatal("the conversion changed nothing, so this test proves nothing")
	}

	resp, out := h.revert("wilant", orig.ID)
	if resp.StatusCode != http.StatusOK || out.Indoor {
		t.Fatalf("revert status = %d indoor = %v", resp.StatusCode, out.Indoor)
	}
	got := h.stored(orig.ID)
	if !reflect.DeepEqual(got.Steps, nested) {
		t.Errorf("steps after revert = %+v, want the original exactly", got.Steps)
	}
	if got.Indoor || got.OutdoorSteps != nil {
		t.Errorf("indoor=%v outdoor=%v after revert, want false and nil", got.Indoor, got.OutdoorSteps)
	}
	if !strings.Contains(got.Description, "Back to the outdoor version") {
		t.Errorf("description = %q, want the revert note", got.Description)
	}
	if strings.Contains(got.Description, scheduler.AdjustedMarker) || !scheduler.IsGenerated(got) {
		t.Errorf("revert must not add the adjusted marker: %q", got.Description)
	}
	if got.Name != orig.Name || got.Date != orig.Date || got.Zone != orig.Zone || got.Level != orig.Level {
		t.Errorf("revert changed more than the steps: %+v", got)
	}

	// Convert, revert, convert again round-trips.
	h.convert("wilant", orig.ID, "")
	again := h.stored(orig.ID)
	if !reflect.DeepEqual(again.Steps, converted.Steps) || again.OutdoorSteps == nil || !reflect.DeepEqual(*again.OutdoorSteps, nested) {
		t.Errorf("second conversion differs from the first: %+v", again)
	}
}

func TestRevertIsIdempotentAndRefusedLikeConvert(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	ctx := context.Background()

	plain := h.indoorRide(goal, indoorFuture, 3600)
	before := h.stored(plain.ID)
	if resp, out := h.revert("wilant", plain.ID); resp.StatusCode != http.StatusOK || out.Indoor {
		t.Fatalf("revert of an outdoor workout = %d indoor=%v, want 200 unchanged", resp.StatusCode, out.Indoor)
	}
	if !reflect.DeepEqual(before, h.stored(plain.ID)) {
		t.Error("reverting an outdoor workout changed it")
	}

	h.convert("wilant", plain.ID, "")
	h.revert("wilant", plain.ID)
	restored := h.stored(plain.ID)
	if resp, _ := h.revert("wilant", plain.ID); resp.StatusCode != http.StatusOK || !reflect.DeepEqual(restored, h.stored(plain.ID)) {
		t.Errorf("a second revert changed the workout (status %d)", resp.StatusCode)
	}

	// An indoor flag with no stored original (a row from before this existed)
	// is left alone rather than guessed at.
	orphan := h.indoorRide(goal, indoorFuture, 3600)
	yes := true
	if _, err := h.training.UpdateWorkout(ctx, orphan.ID, workout.UpdateWorkoutRequest{Indoor: &yes}); err != nil {
		t.Fatal(err)
	}
	beforeOrphan := h.stored(orphan.ID)
	if resp, _ := h.revert("wilant", orphan.ID); resp.StatusCode != http.StatusOK || !reflect.DeepEqual(beforeOrphan, h.stored(orphan.ID)) {
		t.Errorf("revert with no outdoor steps = %d, want 200 and unchanged", resp.StatusCode)
	}

	past := h.indoorRide(goal, indoorYesterday, 3600)
	future := h.indoorRide(goal, indoorFuture, 3600)
	h.convert("wilant", future.ID, "")
	if resp, _ := h.revert("wilant", past.ID); resp.StatusCode != http.StatusConflict {
		t.Errorf("revert of a past ride = %d, want 409", resp.StatusCode)
	}
	if resp, _ := h.revert("someone-else", future.ID); resp.StatusCode != http.StatusNotFound {
		t.Errorf("revert of another rider's = %d, want 404", resp.StatusCode)
	}
	if !h.stored(future.ID).Indoor {
		t.Error("another rider's revert took effect")
	}
	if _, err := h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden", Sport: "cycling", Date: indoorToday, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	today := h.indoorRide(goal, indoorToday, 3600)
	if resp, _ := h.revert("wilant", today.ID); resp.StatusCode != http.StatusConflict {
		t.Errorf("revert of a ridden session = %d, want 409", resp.StatusCode)
	}
}

func TestSmartTrainerRoundTripsThroughTheProfileEndpoint(t *testing.T) {
	h := newTrainingHarness(t)
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/profile", `{"ftpWatts":250,"smartTrainer":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d", resp.StatusCode)
	}
	var saved struct {
		SmartTrainer bool `json:"smartTrainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil || !saved.SmartTrainer {
		t.Errorf("saved = %+v err %v", saved, err)
	}
	resp = h.as("wilant", "cyclists", http.MethodGet, "/api/training/profile", "")
	saved.SmartTrainer = false
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil || !saved.SmartTrainer {
		t.Errorf("read back = %+v err %v", saved, err)
	}
}
