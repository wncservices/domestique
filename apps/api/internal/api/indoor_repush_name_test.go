package api_test

import (
	"net/http"
	"testing"
)

// The same-request push after a conversion (indoor_wave_test.go) sends the
// device name, which carries the indoor suffix.
func TestTheSameRequestPushCarriesTheIndoorNameAndRevertRestoresIt(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.indoorSetup(false)
	id := h.indoorRide(goal, indoorToday, 13200).ID
	h.push(id)

	if resp, _ := h.convert("wilant", id, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("convert status = %d", resp.StatusCode)
	}
	if name := h.garmin.remoteWorkouts["garmin-workout-1"]; name != "Long ride (indoor)" {
		t.Errorf("remote name after convert = %q", name)
	}
	if resp, _ := h.revert("wilant", id); resp.StatusCode != http.StatusOK {
		t.Fatalf("revert status = %d", resp.StatusCode)
	}
	if name := h.garmin.remoteWorkouts["garmin-workout-1"]; name != "Long ride" {
		t.Errorf("remote name after revert = %q", name)
	}
}
