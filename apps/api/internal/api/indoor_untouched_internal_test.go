package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// An indoor session is the rider's own choice. The season refresh rewrites
// untouched sessions in place; converting normally already makes a session
// touched (the note changes the description), but the guarantee must not rest
// on that.
func TestAnIndoorSessionIsNeverUntouched(t *testing.T) {
	w := workout.Workout{
		GoalID: "goal", Sport: model.SportCycling, Name: "Long ride",
		Description: scheduler.GeneratedDescription, CreatedAt: "2026-03-01T00:00:00Z", UpdatedAt: "2026-03-01T00:00:00Z",
	}
	if !untouchedPlanSession(w) {
		t.Fatal("the baseline session should be untouched, or this test proves nothing")
	}
	w.Indoor = true
	if untouchedPlanSession(w) {
		t.Error("an indoor session was offered to the season refresh")
	}
}
