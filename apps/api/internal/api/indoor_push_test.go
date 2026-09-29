package api_test

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestConvertingAPushedWorkoutUpdatesItsCopyUnderTheSameRemoteIDAndRevertDoesToo(t *testing.T) {
	h := newPushHarness(t)
	goal := h.indoorSetup(false)
	id := h.indoorRide(goal, tomorrow(), 13200).ID
	h.push(id)
	if h.garmin.remoteWorkouts["garmin-workout-1"] != "Long ride" {
		t.Fatalf("remote name = %q", h.garmin.remoteWorkouts["garmin-workout-1"])
	}

	if resp, _ := h.convert("wilant", id, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("convert status = %d", resp.StatusCode)
	}
	h.garmin.workoutCalls = nil
	h.push(id)
	if got := h.calls(); got != "update garmin-workout-1" {
		t.Errorf("after convert calls = %q, want an in-place update of the same copy", got)
	}
	if got := h.garmin.remoteWorkouts["garmin-workout-1"]; got != "Long ride (indoor)" {
		t.Errorf("remote name = %q, want the indoor suffix", got)
	}
	if h.stored(id).Name != "Long ride" {
		t.Errorf("the stored name changed to %q", h.stored(id).Name)
	}

	h.garmin.workoutCalls = nil
	h.push(id)
	if got := h.calls(); got != "" {
		t.Errorf("an unchanged indoor workout was re-pushed: %q", got)
	}

	if resp, _ := h.revert("wilant", id); resp.StatusCode != http.StatusOK {
		t.Fatalf("revert status = %d", resp.StatusCode)
	}
	h.garmin.workoutCalls = nil
	h.push(id)
	if got := h.calls(); got != "update garmin-workout-1" {
		t.Errorf("after revert calls = %q, want an in-place update of the same copy", got)
	}
	if got := h.garmin.remoteWorkouts["garmin-workout-1"]; got != "Long ride" {
		t.Errorf("remote name after revert = %q", got)
	}
	if len(h.garmin.remoteWorkouts) != 1 {
		t.Errorf("account holds %d workouts, want 1", len(h.garmin.remoteWorkouts))
	}
}

func TestFlaggingAnFTPTestIndoorRePushesItWithoutChangingItsSteps(t *testing.T) {
	h := newPushHarness(t)
	h.indoorSetup(false)
	test, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "FTP Test (twenty minute)", Date: tomorrow(),
		TestProtocol: "twenty_minute", Steps: []workout.WorkoutStep{
			{Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetOpen},
			{Name: "Effort", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 1200, Target: workout.TargetOpen},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.push(test.ID)
	before := h.stored(test.ID).Steps

	if resp, _ := h.convert("wilant", test.ID, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("convert status = %d", resp.StatusCode)
	}
	got := h.stored(test.ID)
	if !got.Indoor || !reflect.DeepEqual(before, got.Steps) {
		t.Fatalf("indoor=%v, steps changed=%v: converting a test flags it and leaves its steps alone", got.Indoor, !reflect.DeepEqual(before, got.Steps))
	}
	h.garmin.workoutCalls = nil
	h.push(test.ID)
	if calls := h.calls(); calls != "update garmin-workout-1" {
		t.Errorf("calls = %q, want the test's copy renamed in place", calls)
	}
	if name := h.garmin.remoteWorkouts["garmin-workout-1"]; name != "FTP Test (twenty minute) (indoor)" {
		t.Errorf("remote name = %q", name)
	}
}

func TestDownloadingAnIndoorWorkoutMarksTheFileAndTheNameIndoor(t *testing.T) {
	h := newPushHarness(t)
	goal := h.indoorSetup(false)
	id := h.indoorRide(goal, tomorrow(), 3600).ID

	fetch := func() (string, *filedef.Workout) {
		resp := h.as("wilant", "cyclists", http.MethodGet, "/api/training/workouts/"+id+"/fit", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		fit, err := decoder.New(bytes.NewReader(readAll(t, resp))).Decode()
		if err != nil {
			t.Fatal(err)
		}
		return resp.Header.Get("Content-Disposition"), filedef.NewWorkout(fit.Messages...)
	}

	cd, plain := fetch()
	if plain.Workout.WktName != "Long ride" || plain.Workout.SubSport == typedef.SubSportIndoorCycling || strings.Contains(cd, "indoor") {
		t.Errorf("outdoor download: name %q sub_sport %v disposition %q", plain.Workout.WktName, plain.Workout.SubSport, cd)
	}

	h.convert("wilant", id, "")
	cd, converted := fetch()
	if converted.Workout.WktName != "Long ride (indoor)" || converted.Workout.SubSport != typedef.SubSportIndoorCycling {
		t.Errorf("indoor download: name %q sub_sport %v", converted.Workout.WktName, converted.Workout.SubSport)
	}
	if !strings.Contains(cd, "(indoor)") {
		t.Errorf("file name %q, want the indoor suffix", cd)
	}
}
