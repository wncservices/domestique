package api_test

import (
	"net/http"
	"reflect"
	"testing"
)

// Swapping and going indoor are independent axes: a swapped session can be
// converted afterwards, and reverting the swap restores the state the plan had
// (outdoor), not the indoor one the rider added later.
func TestRevertingASwapUndoesAnIndoorConversionMadeAfterIt(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	orig := h.indoorRide(goal, indoorFuture, 13200)
	h.altSwap("wilant", "cyclists", orig.ID, "shorter")
	if resp, out := h.convert("wilant", orig.ID, ""); resp.StatusCode != http.StatusOK || !out.Indoor {
		t.Fatalf("convert = %d indoor=%v", resp.StatusCode, out.Indoor)
	}

	if resp, _ := h.altRevert("wilant", "cyclists", orig.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("revert = %d", resp.StatusCode)
	}
	got := h.stored(orig.ID)
	if got.Indoor || got.OutdoorSteps != nil || !reflect.DeepEqual(got.Steps, orig.Steps) {
		t.Errorf("reverted indoor=%v outdoor=%v; want the plan's outdoor session exactly", got.Indoor, got.OutdoorSteps)
	}
}
