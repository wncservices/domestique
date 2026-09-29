package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// scheduleFTPTest posts to /api/training/tests/ftp as rider and decodes the
// created workout.
func (h *pushHarness) scheduleFTPTest(rider, body string) (*http.Response, ftpWorkoutOut) {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodPost, "/api/training/tests/ftp", body)
	var out ftpWorkoutOut
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp, out
}

type ftpWorkoutOut struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	GoalID          string  `json:"goalId"`
	Date            string  `json:"date"`
	Description     string  `json:"description"`
	TestProtocol    string  `json:"testProtocol"`
	TestResultWatts float64 `json:"testResultWatts"`
	Steps           []struct {
		Target     string  `json:"target"`
		TargetLow  float64 `json:"targetLow"`
		TargetHigh float64 `json:"targetHigh"`
		Seconds    float64 `json:"seconds"`
	} `json:"steps"`
}

// scheduleSetup is a rider with a goal and FTP, on the replan clock
// (Wednesday 2026-03-25).
func (h *pushHarness) scheduleSetup(ftp float64) workout.Goal {
	h.t.Helper()
	ctx := context.Background()
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit", Priority: workout.PriorityA})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", FTPWatts: ftp, HoursPerAvailableDay: 1.5, AvailableDays: []string{"wed", "fri", "sat"},
	}); err != nil {
		h.t.Fatal(err)
	}
	return goal
}

func TestScheduleAnFTPTestCreatesItOnTheDayLinkedToTheFocusGoal(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.scheduleSetup(250)

	resp, out := h.scheduleFTPTest("wilant", `{"protocol":"ramp","date":"`+replanFriday+`"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if out.Date != replanFriday || out.TestProtocol != "ramp" || out.GoalID != goal.ID {
		t.Errorf("workout = %+v, want ramp on %s linked to %s", out, replanFriday, goal.ID)
	}
	if strings.HasPrefix(out.Description, scheduler.GeneratedDescription) {
		t.Error("the test's description must not read as generated, or replan would delete it")
	}
	// Ramp steps start at 50% of the profile FTP: 125 W.
	var first float64
	for _, s := range out.Steps {
		if s.Target == "power" {
			first = s.TargetLow
			break
		}
	}
	if first != 125 {
		t.Errorf("first ramp step = %v W, want 125 (50%% of 250)", first)
	}
}

func TestSchedulingReplacesOnlyThatDaysPlanMadeSessionAndItsGarminCopy(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	goal := h.scheduleSetup(250)

	onDay, err := h.training.CreateWorkout(ctx, generated("wilant", goal.ID, "Endurance ride", replanFriday, 3600))
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.training.CreateWorkout(ctx, generated("wilant", goal.ID, "Long ride", replanSaturday, 3600))
	if err != nil {
		t.Fatal(err)
	}
	mine, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "My own ride", Date: replanFriday, Description: "mine",
	})
	if err != nil {
		t.Fatal(err)
	}
	someoneElses, err := h.training.CreateWorkout(ctx, generated("sam", goal.ID, "Endurance ride", replanFriday, 3600))
	if err != nil {
		t.Fatal(err)
	}
	h.push(onDay.ID)
	h.garmin.workoutCalls = nil

	if resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"twenty_minute","date":"`+replanFriday+`"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	if _, err := h.training.GetWorkout(ctx, onDay.ID); err == nil {
		t.Error("the plan-made session on the test day should have been replaced")
	}
	for name, id := range map[string]string{"the next day's session": other.ID, "a rider-built workout": mine.ID, "another rider's session": someoneElses.ID} {
		if _, err := h.training.GetWorkout(ctx, id); err != nil {
			t.Errorf("%s was deleted: %v", name, err)
		}
	}
	if calls := h.calls(); !strings.Contains(calls, "delete garmin-workout-1") {
		t.Errorf("calls = %q, want the replaced session's copy removed from Garmin", calls)
	}
}

func TestSchedulingRejectsBadRequests(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	h.scheduleSetup(0) // no FTP on file

	// A ride on the test day already happened.
	if _, err := h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden", Sport: "cycling", Date: replanToday, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		body string
		want int
	}{
		"past day":                   {`{"protocol":"twenty_minute","date":"2026-03-24"}`, http.StatusConflict},
		"day already ridden":         {`{"protocol":"twenty_minute","date":"` + replanToday + `"}`, http.StatusConflict},
		"ramp with no FTP nor guess": {`{"protocol":"ramp","date":"` + replanFriday + `"}`, http.StatusConflict},
		"ramp with a zero guess":     {`{"protocol":"ramp","date":"` + replanFriday + `","estimatedFtp":0}`, http.StatusConflict},
		"unknown protocol":           {`{"protocol":"bogus","date":"` + replanFriday + `"}`, http.StatusBadRequest},
		"malformed date":             {`{"protocol":"twenty_minute","date":"friday"}`, http.StatusBadRequest},
		"negative guess":             {`{"protocol":"ramp","date":"` + replanFriday + `","estimatedFtp":-5}`, http.StatusBadRequest},
		"absurd guess":               {`{"protocol":"ramp","date":"` + replanFriday + `","estimatedFtp":5000}`, http.StatusBadRequest},
		"not JSON":                   {`{`, http.StatusBadRequest},
	} {
		if resp, _ := h.scheduleFTPTest("wilant", c.body); resp.StatusCode != c.want {
			t.Errorf("%s: status = %d, want %d", name, resp.StatusCode, c.want)
		}
	}
	list, _ := h.training.ListWorkouts(ctx, "wilant")
	if len(list) != 0 {
		t.Errorf("a rejected request left %d workouts behind", len(list))
	}
}

func TestATypedFTPGuessBuildsTheRampAndIsNotSaved(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	h.scheduleSetup(0)

	resp, out := h.scheduleFTPTest("wilant", `{"protocol":"ramp","date":"`+replanFriday+`","estimatedFtp":200}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var first float64
	for _, s := range out.Steps {
		if s.Target == "power" {
			first = s.TargetLow
			break
		}
	}
	if first != 100 {
		t.Errorf("first step = %v W, want 100 (50%% of the 200 W guess)", first)
	}
	profile, _, _ := h.training.GetProfile(ctx, "wilant")
	if profile.FTPWatts != 0 {
		t.Errorf("the guess was saved as FTP (%v W)", profile.FTPWatts)
	}
}

func TestAnEmptyBodyStillBuildsTheUnscheduledTwentyMinuteTest(t *testing.T) {
	h := newReplanHarness(t)
	resp, out := h.scheduleFTPTest("wilant", "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if out.Date != "" || out.Name != "FTP Test (20-minute)" || out.TestProtocol != "twenty_minute" {
		t.Errorf("workout = %+v, want the unscheduled 20-minute test", out)
	}
}

func TestAScheduledTestSurvivesReplanAndSilencesTheSuggestion(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	h.scheduleSetup(250)

	resp, test := h.scheduleFTPTest("wilant", `{"protocol":"twenty_minute","date":"`+replanFriday+`"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if r, _ := h.replan("wilant"); r.StatusCode != http.StatusOK {
		t.Fatalf("replan status = %d", r.StatusCode)
	}
	if _, err := h.training.GetWorkout(ctx, test.ID); err != nil {
		t.Errorf("replan removed the scheduled test: %v", err)
	}
	// Friday now has the test and nothing generated on top of it.
	list, _ := h.training.ListWorkouts(ctx, "wilant")
	for _, w := range list {
		if w.Date == replanFriday && w.ID != test.ID && w.GoalID != "" {
			t.Errorf("replan put %q on the test's day", w.Name)
		}
	}
	// After a test is on the calendar the banner stays away.
	tests := h.as("wilant", "cyclists", http.MethodGet, "/api/training/tests", "")
	var got ftpTestsOut
	if err := json.NewDecoder(tests.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Scheduled == nil || got.Scheduled.WorkoutID != test.ID || got.Suggestion != nil {
		t.Errorf("tests = scheduled %+v suggestion %+v", got.Scheduled, got.Suggestion)
	}
}
