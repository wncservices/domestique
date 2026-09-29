package workout

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

func TestDeviceNameSuffixesAnIndoorWorkoutOnly(t *testing.T) {
	if got := DeviceName("Long ride", false); got != "Long ride" {
		t.Errorf("outdoor = %q", got)
	}
	if got := DeviceName("Long ride", true); got != "Long ride (indoor)" {
		t.Errorf("indoor = %q, want the suffix", got)
	}
}

func TestContentHashChangesWhenOnlyTheIndoorFlagDoes(t *testing.T) {
	w := Workout{Sport: model.SportCycling, Name: "FTP Test (ramp)", Steps: exampleSteps()}
	flagged := w
	flagged.Indoor = true
	if ContentHash(w) == ContentHash(flagged) {
		t.Error("flagging a workout indoor without touching its steps (an FTP test) must change what is pushed")
	}
	// The date is still not part of it.
	moved := w
	moved.Date = "2026-04-01"
	if ContentHash(w) != ContentHash(moved) {
		t.Error("the date must not be part of the hash")
	}
}
