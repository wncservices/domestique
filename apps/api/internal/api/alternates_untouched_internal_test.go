package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A swap is the rider's choice. The season refresh rewrites untouched sessions
// in place; a swap already fails the description and timestamp tests, but the
// guarantee must not rest on those: a revert-then-edit or a clock skew could
// leave both looking untouched, and only the marker and the snapshot remain.
func TestASwappedSessionIsNeverUntouched(t *testing.T) {
	base := workout.Workout{
		GoalID: "goal", Sport: model.SportCycling, Name: "Threshold 3×12",
		Description: scheduler.GeneratedDescription, CreatedAt: "2026-03-01T00:00:00Z", UpdatedAt: "2026-03-01T00:00:00Z",
	}
	if !untouchedPlanSession(base) {
		t.Fatal("the baseline session should be untouched, or this test proves nothing")
	}

	withSnapshot := base
	withSnapshot.PlannedSnapshot = &workout.PlannedSnapshot{Name: "Threshold 4×12"}
	if untouchedPlanSession(withSnapshot) {
		t.Error("a session holding a planned snapshot was offered to the season refresh")
	}

	// The description alone, as if the timestamps had been reset.
	marked := base
	marked.Description = scheduler.GeneratedDescription + " " + scheduler.SwappedMarker + " harder, was Threshold 3×12 (1h20)."
	if untouchedPlanSession(marked) {
		t.Error("a session carrying the swap marker was offered to the season refresh")
	}
}
