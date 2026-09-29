package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestPurgeRiderDataRemovesThePlanChangeReasons(t *testing.T) {
	h := newPurgeHarness(t)
	ctx := t.Context()
	training, err := workout.UseDB(h.src.Conn(), h.src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Training = training
	rec := why.NewRecord(why.MissedMoved, "moved", why.MissedMovedInputs{})
	for rider, id := range map[string]string{"gone": "w-gone", "stays": "w-stays"} {
		if err := training.RecordAdjustment(ctx, rider, workout.SubjectWorkout, id, rec, "2026-03-26"); err != nil {
			t.Fatal(err)
		}
	}
	if err := training.RecordAdjustment(ctx, "gone", workout.SubjectLevel, "cycling:threshold", why.NewRecord(why.LevelRecalibration, "lowered", why.LevelRecalibrationInputs{}), "2026-03-26"); err != nil {
		t.Fatal(err)
	}

	sum, err := h.srv.purgeRiderData(ctx, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if sum.AdjustmentsRemoved != 2 {
		t.Errorf("AdjustmentsRemoved = %d, want 2 (a workout and a level)", sum.AdjustmentsRemoved)
	}
	if got, _ := training.LatestAdjustments(ctx, "gone", workout.SubjectWorkout, []string{"w-gone"}); len(got) != 0 {
		t.Errorf("the departed rider's reasons survived: %v", got)
	}
	if got, _ := training.LatestAdjustments(ctx, "stays", workout.SubjectWorkout, []string{"w-stays"}); len(got) != 1 {
		t.Error("another rider's reasons were removed")
	}
}
