package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/pacingpush"
)

// A rider's pacing pushes are rider-keyed rows, so removing the rider removes
// them, and only theirs.
func TestPurgeRiderDataRemovesThePacingPushes(t *testing.T) {
	h := newPurgeHarness(t)
	pushes, err := pacingpush.UseDB(h.src.Conn(), h.src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.PacingPushes = pushes
	ctx := t.Context()
	for _, c := range [][3]string{{"leaver", "garmin", "pacing:a"}, {"leaver", "wahoo", "pacing:a"}, {"stayer", "garmin", "pacing:a"}} {
		if err := pushes.Put(ctx, c[0], c[1], c[2], "remote"); err != nil {
			t.Fatal(err)
		}
	}

	sum, err := h.srv.purgeRiderData(ctx, "Leaver")
	if err != nil {
		t.Fatal(err)
	}
	if sum.PacingPushes != 2 {
		t.Errorf("summary says %d pacing pushes removed, want 2", sum.PacingPushes)
	}
	if _, ok, _ := pushes.Get(ctx, "leaver", "garmin", "pacing:a"); ok {
		t.Error("the leaver's pacing push survived")
	}
	if _, ok, _ := pushes.Get(ctx, "stayer", "garmin", "pacing:a"); !ok {
		t.Error("another rider's pacing push was removed")
	}
	// Retry-safe, like every other step.
	if _, err := h.srv.purgeRiderData(ctx, "leaver"); err != nil {
		t.Errorf("a second purge: %v", err)
	}
}
