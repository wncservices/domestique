package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

type canRevertOut struct {
	Indoor          bool `json:"indoor"`
	CanRevertIndoor bool `json:"canRevertIndoor"`
}

func (h *pushHarness) patchWorkout(id, body string) *http.Response {
	h.t.Helper()
	return h.as("wilant", "cyclists", http.MethodPatch, "/api/training/workouts/"+id, body)
}

func (h *pushHarness) dto(id string) (canRevertOut, json.RawMessage) {
	h.t.Helper()
	resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+id, "")
	raw := readAll(h.t, resp)
	var out canRevertOut
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatal(err)
	}
	var steps struct {
		Steps json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(raw, &steps); err != nil {
		h.t.Fatal(err)
	}
	return out, steps.Steps
}

func TestEditingTheStepsOfAnIndoorWorkoutRetiresItsOutdoorOriginal(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	ride := h.indoorRide(goal, indoorFuture, 13200)
	h.convert("wilant", ride.ID, "")
	if d, _ := h.dto(ride.ID); !d.Indoor || !d.CanRevertIndoor {
		t.Fatalf("after convert: %+v, want indoor and revertable", d)
	}

	resp := h.patchWorkout(ride.ID, `{"steps":[{"name":"Ride","intensity":"active","duration":"time","seconds":1800,"target":"open"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d", resp.StatusCode)
	}
	got := h.stored(ride.ID)
	if !got.Indoor || got.OutdoorSteps != nil {
		t.Errorf("indoor=%v outdoor=%v, want indoor kept and the original dropped", got.Indoor, got.OutdoorSteps)
	}
	if d, _ := h.dto(ride.ID); !d.Indoor || d.CanRevertIndoor {
		t.Errorf("dto = %+v, want indoor with no revert on offer", d)
	}
	// Revert is now a no-op: the rider's edit is not thrown away.
	if resp, out := h.revert("wilant", ride.ID); resp.StatusCode != http.StatusOK || !out.Indoor {
		t.Errorf("revert = %d indoor=%v, want 200 and unchanged", resp.StatusCode, out.Indoor)
	}
	if h.stored(ride.ID).Steps[0].Seconds != 1800 {
		t.Error("the revert undid the rider's edit")
	}
}

func TestSavingAnIndoorWorkoutWithItsOwnStepsKeepsRevert(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	ride := h.indoorRide(goal, indoorFuture, 13200)
	h.convert("wilant", ride.ID, "")
	_, steps := h.dto(ride.ID)

	// What the slideover sends on every save: the new name and the steps as they were.
	resp := h.patchWorkout(ride.ID, `{"name":"Renamed ride","steps":`+string(steps)+`}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d", resp.StatusCode)
	}
	if d, _ := h.dto(ride.ID); !d.CanRevertIndoor {
		t.Error("a rename that re-sent identical steps killed revert")
	}
	if resp, out := h.revert("wilant", ride.ID); resp.StatusCode != http.StatusOK || out.Indoor {
		t.Errorf("revert = %d indoor=%v, want it restored", resp.StatusCode, out.Indoor)
	}
	// And a bare rename, with no steps at all.
	h.convert("wilant", ride.ID, "")
	h.patchWorkout(ride.ID, `{"name":"Again"}`)
	if d, _ := h.dto(ride.ID); !d.CanRevertIndoor {
		t.Error("a name-only PATCH killed revert")
	}
}

func TestChangingTheSportOfAnIndoorWorkoutRetiresItsOutdoorOriginal(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	ride := h.indoorRide(goal, indoorFuture, 3600)
	h.convert("wilant", ride.ID, "")

	if resp := h.patchWorkout(ride.ID, `{"sport":"running"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d", resp.StatusCode)
	}
	got := h.stored(ride.ID)
	if got.OutdoorSteps != nil || !got.Indoor {
		t.Errorf("indoor=%v outdoor=%v, want the original dropped and the flag kept", got.Indoor, got.OutdoorSteps)
	}
	// Setting the same sport again is not a change.
	h2 := newReplanHarness(t)
	goal2 := h2.indoorSetup(false)
	ride2 := h2.indoorRide(goal2, indoorFuture, 3600)
	h2.convert("wilant", ride2.ID, "")
	h2.patchWorkout(ride2.ID, `{"sport":"cycling"}`)
	if d, _ := h2.dto(ride2.ID); !d.CanRevertIndoor {
		t.Error("re-sending the same sport killed revert")
	}
}

func TestConvertingTodaysPushedSessionUpdatesTheGarminCopyInTheSameRequest(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	id := h.indoorRide(goal, indoorToday, 13200).ID
	h.push(id)
	h.garmin.workoutCalls = nil

	if resp, _ := h.convert("wilant", id, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("convert status = %d", resp.StatusCode)
	}
	if got := h.calls(); got != "update garmin-workout-1" {
		t.Errorf("calls = %q, want the copy updated during the request", got)
	}

	h.garmin.workoutCalls = nil
	if resp, _ := h.revert("wilant", id); resp.StatusCode != http.StatusOK {
		t.Fatalf("revert status = %d", resp.StatusCode)
	}
	if got := h.calls(); got != "update garmin-workout-1" {
		t.Errorf("after revert calls = %q, want the copy updated", got)
	}
}

func TestConvertingAFutureOrUnpushedSessionPushesNothing(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	future := h.indoorRide(goal, indoorFuture, 3600).ID
	h.push(future)
	unpushed := h.indoorRide(goal, indoorToday, 3600).ID
	h.garmin.workoutCalls = nil

	h.convert("wilant", future, "")
	h.convert("wilant", unpushed, "")
	if got := h.calls(); got != "" {
		t.Errorf("calls = %q, want none: a future day pushes when its day comes, and an unsent session is the rider's to send", got)
	}
}

func TestAFailedPushAfterConvertingIsLoggedAndTheRequestStillSucceeds(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	id := h.indoorRide(goal, indoorToday, 13200).ID
	h.push(id)
	// The rider deleted the copy in Connect and creating a new one now fails.
	delete(h.garmin.remoteWorkouts, "garmin-workout-1")
	h.garmin.pushWorkoutErr = errors.New("garmin is down")

	var logs bytes.Buffer
	h.srv.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	resp, out := h.convert("wilant", id, "")
	if resp.StatusCode != http.StatusOK || !out.Indoor {
		t.Fatalf("convert = %d indoor=%v, want 200: the push is best-effort", resp.StatusCode, out.Indoor)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "garmin is down") {
		t.Errorf("log = %q, want a Warn about the failed push", logs.String())
	}
}

func TestAnAutomaticEasingRefreshesTheConversionNotesDurations(t *testing.T) {
	h := newTomorrowHarness(t)
	h.saveProfile(true)
	orig := tomorrowLongHard(h, tmTomorrow)
	// Convert through the API so the description carries the real note.
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+orig.ID+"/indoor?today="+tmToday, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("convert = %d", resp.StatusCode)
	}
	before, _ := h.store.GetWorkout(context.Background(), orig.ID)
	if !strings.Contains(before.Description, "Indoor version of Threshold 3x12 (3h00), ") {
		t.Fatalf("description = %q, want the original conversion note", before.Description)
	}
	h.snapshot(40, 100)

	if status, out := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusOK {
		t.Fatalf("ease = %d (%v)", status, out)
	}
	got, _ := h.store.GetWorkout(context.Background(), orig.ID)
	if strings.Count(got.Description, "Indoor version of") != 1 {
		t.Errorf("description = %q, want exactly one indoor note", got.Description)
	}
	if strings.Contains(got.Description, "Indoor version of Threshold 3x12") {
		t.Errorf("description = %q, still names the pre-easing session", got.Description)
	}
	want := "Indoor version of Endurance ride (" // the eased outdoor session, with its own durations
	if !strings.Contains(got.Description, want) {
		t.Errorf("description = %q, want a note starting %q", got.Description, want)
	}
	// The note stays out of the text the UI shows as the adjustment reason.
	if visible := riderVisibleAdjustmentText(got.Description); strings.Contains(visible, "Indoor version") {
		t.Errorf("rider-visible adjustment text = %q, must not carry the indoor note", visible)
	}
	if got.Indoor != true || markerCount(got) != 1 {
		t.Errorf("indoor=%v markers=%d", got.Indoor, markerCount(got))
	}
}

func TestEasingSizesTheEasyRideFromTheOutdoorSessionNotTheShortenedIndoorOne(t *testing.T) {
	outdoor := longRideSteps(9600) // 3 h in all
	indoorSteps := longRideSteps(4800)
	w := workout.Workout{
		Sport: "cycling", Name: "Long ride", Zone: workout.ZoneEndurance, Steps: indoorSteps, Indoor: true,
		OutdoorSteps: &outdoor,
	}
	plain := w
	plain.Indoor, plain.OutdoorSteps, plain.Steps = false, nil, outdoor

	got := workout.PlannedSeconds(scheduler.EasyVariant(w, workout.RiderProfile{FTPWatts: 250}).Steps)
	want := workout.PlannedSeconds(scheduler.EasyVariant(plain, workout.RiderProfile{FTPWatts: 250}).Steps)
	if got != want {
		t.Errorf("easy variant of an indoor session = %v s, want %v s as if sized from the outdoor session", got, want)
	}
}
