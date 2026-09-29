package api_test

import (
	"context"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestEasingTheDayBeforeAnFTPTestRecordsTheTest(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	goal := h.scheduleSetup(250)
	hard, err := h.training.CreateWorkout(ctx, hardGenerated("wilant", goal.ID, "Threshold intervals", replanThursday))
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"twenty_minute","date":"`+replanFriday+`"}`); resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	got, err := h.training.LatestAdjustments(ctx, "wilant", workout.SubjectWorkout, []string{hard.ID})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := got[hard.ID]
	if !ok || a.Rule != why.FTPTestEve {
		t.Fatalf("adjustment = %+v (found %v), want ftp_test_eve", a, ok)
	}
	in := decodeInputs[why.FTPTestEveInputs](t, a)
	if in.TestDate != replanFriday || in.Protocol != "twenty_minute" {
		t.Errorf("inputs = %+v, want the test's date and protocol", in)
	}
}
