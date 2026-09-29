package api_test

import (
	"context"
	"net/http"
	"testing"
)

// If the replaced session cannot be removed, the test that was just created is
// taken back out: a retry must not leave a second test on the day.
func TestSchedulingRollsTheTestBackWhenTheReplacedSessionCannotBeDeleted(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	goal := h.scheduleSetup(250)
	if _, err := h.training.CreateWorkout(ctx, generated("wilant", goal.ID, "Endurance ride", replanFriday, 3600)); err != nil {
		t.Fatal(err)
	}
	// DeleteWorkout removes the row and then its push rows; without that table
	// it reports an error (after the row is gone), which is the failure this
	// simulates. The rollback's own delete errors the same way but takes the
	// test row out all the same.
	if _, err := h.db.Conn().Exec("DROP TABLE workout_pushes"); err != nil {
		t.Fatal(err)
	}

	resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"twenty_minute","date":"`+replanFriday+`"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	list, err := h.training.ListWorkouts(ctx, "wilant")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range list {
		if w.TestProtocol != "" {
			t.Errorf("a test was left behind after the failed schedule: %+v", w)
		}
	}
}
