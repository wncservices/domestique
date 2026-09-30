package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestIsPlanMadeIsFalseOnceTheRiderSwappedTheSession(t *testing.T) {
	wk := workout.Workout{GoalID: "g", Description: scheduler.GeneratedDescription}
	if !isPlanMade(wk) {
		t.Fatal("setup: a generated workout is plan-made")
	}
	wk.Description += " " + scheduler.AdjustedMarker + " moved from 2026-10-03."
	if !isPlanMade(wk) {
		t.Error("an automatically adjusted session is still plan-made")
	}
	wk.Description += " " + scheduler.SwappedMarker + " easier, was Threshold 5 (1h30)."
	if isPlanMade(wk) {
		t.Error("a swapped session must survive Re-plan this week")
	}
}
