package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const easedReason = "eased the day before your FTP test"

func hardGenerated(rider, goal, name, date string) workout.CreateWorkoutRequest {
	req := generated(rider, goal, name, date, 3600)
	req.Zone, req.Level = workout.ZoneThreshold, 3
	return req
}

func TestSchedulingATestEasesTheGeneratedHardSessionTheDayBefore(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	goal := h.scheduleSetup(250)

	hard, err := h.training.CreateWorkout(ctx, hardGenerated("wilant", goal.ID, "Threshold intervals", replanThursday))
	if err != nil {
		t.Fatal(err)
	}
	mine, err := h.training.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "My intervals", Date: replanToday,
		Zone: workout.ZoneVO2Max, Description: "my own",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, test := h.scheduleFTPTest("wilant", `{"protocol":"twenty_minute","date":"`+replanFriday+`"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	eased, err := h.training.GetWorkout(ctx, hard.ID)
	if err != nil {
		t.Fatal(err)
	}
	if eased.Zone != workout.ZoneEndurance || eased.Name == "Threshold intervals" {
		t.Errorf("hard session = %q/%q, want an endurance replacement", eased.Name, eased.Zone)
	}
	if !strings.Contains(eased.Description, "Adjusted automatically: "+easedReason) {
		t.Errorf("description = %q, want the marker and reason", eased.Description)
	}
	if got, _ := h.training.GetWorkout(ctx, mine.ID); got.Zone != workout.ZoneVO2Max || strings.Contains(got.Description, "Adjusted") {
		t.Errorf("a rider-built session was touched: %+v", got)
	}
	if got, _ := h.training.GetWorkout(ctx, test.ID); strings.Contains(got.Description, "Adjusted") {
		t.Errorf("the test itself was eased: %q", got.Description)
	}
}

func TestEasingIsNotAppliedTwiceAndNotToARiddenSession(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	goal := h.scheduleSetup(250)

	// Wednesday (today) is the day before a Thursday test, and already ridden.
	today, err := h.training.CreateWorkout(ctx, hardGenerated("wilant", goal.ID, "Threshold intervals", replanToday))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "ridden", Sport: "cycling", Date: replanToday, DurationSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"twenty_minute","date":"`+replanThursday+`"}`); resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got, _ := h.training.GetWorkout(ctx, today.ID); got.Zone != workout.ZoneThreshold || strings.Contains(got.Description, "Adjusted") {
		t.Errorf("a session already ridden was eased: %+v", got)
	}
}

func TestTheEasingSurvivesAReplan(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	h.scheduleSetup(250)

	// Wednesday, today, is where the profile's one structured slot lands (see
	// baseProfile); a Thursday test makes it the day before.
	if resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"twenty_minute","date":"`+replanThursday+`"}`); resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if r, _ := h.replan("wilant"); r.StatusCode != 200 {
		t.Fatalf("replan status = %d", r.StatusCode)
	}

	list, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range list {
		if w.Date != replanToday || w.GoalID == "" {
			continue
		}
		found = true
		if w.Zone != workout.ZoneEndurance || !strings.Contains(w.Description, easedReason) {
			t.Errorf("the day before the test after a replan = %q zone %q %q, want it eased", w.Name, w.Zone, w.Description)
		}
	}
	if !found {
		t.Fatal("the replan built nothing on the day before the test: the assertion above was vacuous")
	}
}
