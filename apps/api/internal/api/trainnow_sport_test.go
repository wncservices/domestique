package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A suggestion is built for its own sport. Replacing today's session in place
// with one for another sport must change the workout's sport with its steps, and
// "Back to planned version" must bring the sport back.
func TestApplyReplacingASessionOfAnotherSportChangesItsSportAndRevertRestoresIt(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", FTPWatts: 250, HoursPerAvailableDay: 1.5, AvailableDays: []string{"wed", "fri", "sat"},
	}); err != nil {
		t.Fatal(err)
	}
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Run Fit", Sport: model.SportRunning})
	if err != nil {
		t.Fatal(err)
	}
	orig := h.altEndurance(goal.ID, indoorToday, 1) // a cycling session on a running goal's day

	_, out := h.tnGet("wilant", "cyclists", "?minutes=90")
	easy, ok := out.get("easy")
	if !ok {
		t.Fatalf("suggestions = %s, want an easy one", out.kinds())
	}
	resp, dto := h.tnApply("wilant", "cyclists", 90, easy)
	if resp.StatusCode != http.StatusOK || dto.ID != orig.ID {
		t.Fatalf("apply = %d id=%s, want the session replaced in place", resp.StatusCode, dto.ID)
	}
	got := h.stored(orig.ID)
	if got.Sport != model.SportRunning {
		t.Errorf("sport = %s, want running: the steps are a run's", got.Sport)
	}
	if got.PlannedSnapshot == nil || got.PlannedSnapshot.Sport != model.SportCycling {
		t.Fatalf("snapshot = %+v, want the cycling sport recorded", got.PlannedSnapshot)
	}

	if resp, _ := h.altRevert("wilant", "cyclists", orig.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("revert = %d", resp.StatusCode)
	}
	if back := h.stored(orig.ID); back.Sport != model.SportCycling {
		t.Errorf("reverted sport = %s, want cycling", back.Sport)
	}
}
