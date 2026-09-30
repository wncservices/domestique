package api_test

import (
	"context"
	"math"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/adapter"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The menu's TSS is the step-by-step estimate of the option's own steps, so a
// structured rung is priced by its work and not by its warm-up.
func TestAlternatesTSSIsThePlannedTSSOfTheOptionsSteps(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("vo2max", 10)
	w := h.altRung(goal, indoorFuture, "vo2max", 4)

	_, out := h.altGet("wilant", "cyclists", w.ID, "")
	harder, ok := out.find("harder") // rung 5, 60 minutes
	if !ok {
		t.Fatalf("kinds = %v, want a harder option", out.kinds())
	}
	want, _ := adapter.PlannedTSS(workout.Workout{Sport: model.SportCycling, Steps: altSteps(t, "vo2max", 5)}, 250)
	if math.Abs(harder.TSS-want) > 1e-6 {
		t.Errorf("harder TSS = %v, want %v", harder.TSS, want)
	}
	endurance := h.altEndurance(goal, "2026-04-01", 1)
	easy, _ := adapter.PlannedTSS(endurance, 250)
	if harder.TSS <= easy*1.2 {
		t.Errorf("VO2max L5 = %v, 60-minute endurance = %v: it must cost clearly more", harder.TSS, easy)
	}
}

// Revert restores the sport with the rest of the snapshot.
func TestRevertRestoresTheSnapshotsSport(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	h.altLevel("threshold", 5.2)
	orig := h.altRung(goal, indoorFuture, "threshold", 4)
	h.altSwap("wilant", "cyclists", orig.ID, "easier")

	got := h.stored(orig.ID)
	if got.PlannedSnapshot == nil || got.PlannedSnapshot.Sport != model.SportCycling {
		t.Fatalf("snapshot sport = %+v, want cycling recorded", got.PlannedSnapshot)
	}
	running := model.SportRunning
	if _, err := h.training.UpdateWorkout(context.Background(), orig.ID, workout.UpdateWorkoutRequest{Sport: &running}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.altRevert("wilant", "cyclists", orig.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("revert = %d", resp.StatusCode)
	}
	if back := h.stored(orig.ID); back.Sport != model.SportCycling {
		t.Errorf("reverted sport = %s, want cycling", back.Sport)
	}
}

// An indoor session capped at two hours of main work looks the same on the
// trainer whether the outdoor ride is 5 or 6 hours, so a Longer or Shorter that
// changes nothing on the trainer is not offered (and not applicable).
func TestAnIndoorOptionThatChangesNothingOnTheTrainerIsHidden(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	long := h.indoorRide(goal, indoorFuture, 16800) // 5 h outdoor: 2h20 on the trainer
	if resp, out := h.convert("wilant", long.ID, ""); resp.StatusCode != http.StatusOK || !out.Indoor {
		t.Fatalf("convert = %d indoor=%v", resp.StatusCode, out.Indoor)
	}
	_, opts := h.altGet("wilant", "cyclists", long.ID, "")
	for _, k := range []string{"shorter", "longer"} {
		if o, ok := opts.find(k); ok {
			t.Errorf("%s offered at %d min, the same as the trainer session has now", k, o.Minutes)
		}
	}
	if resp, _ := h.altSwap("wilant", "cyclists", long.ID, "longer"); resp.StatusCode != http.StatusConflict {
		t.Errorf("applying a hidden option = %d, want 409", resp.StatusCode)
	}

	// A 3 h ride is under the cap, so shorter really is shorter on the trainer.
	mid := h.indoorRide(goal, "2026-04-01", 9600)
	h.convert("wilant", mid.ID, "")
	_, opts = h.altGet("wilant", "cyclists", mid.ID, "")
	if _, ok := opts.find("shorter"); !ok {
		t.Errorf("kinds = %v, want shorter offered when the trainer minutes differ", opts.kinds())
	}
}
