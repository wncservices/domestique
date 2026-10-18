package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestIsPlanMadeIsFalseOnceTheRiderSwappedTheSession(t *testing.T) {
	wk := withLegacyFlags(workout.Workout{GoalID: "g", Description: scheduler.GeneratedDescription})
	if !scheduler.IsPlanMade(wk) {
		t.Fatal("setup: a generated workout is plan-made")
	}
	wk.Description += " " + scheduler.AdjustedMarker + " moved from 2026-10-03."
	wk = withLegacyFlags(wk)
	if !scheduler.IsPlanMade(wk) {
		t.Error("an automatically adjusted session is still plan-made")
	}
	wk.Description += " " + scheduler.SwappedMarker + " easier, was Threshold 5 (1h30)."
	wk = withLegacyFlags(wk)
	if scheduler.IsPlanMade(wk) {
		t.Error("a swapped session must survive Re-plan this week")
	}
}
