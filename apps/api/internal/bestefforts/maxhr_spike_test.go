package bestefforts

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/thresholds"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A single sensor spike must not become the all-time max: Efforts applies the
// same ceiling threshold detection does (thresholds.MaxHRSpikeThreshold).
func TestAMaxHeartRateSpikeIsNotAnEffort(t *testing.T) {
	spike := workout.SessionAnalysis{MaxHR: thresholds.MaxHRSpikeThreshold + 1}
	if got := Efforts(spike, "cycling"); len(got) != 0 {
		t.Errorf("a %d bpm spike became an effort: %v", spike.MaxHR, got)
	}
	edge := workout.SessionAnalysis{MaxHR: thresholds.MaxHRSpikeThreshold}
	if got := Efforts(edge, "cycling"); got["max_hr"] != float64(thresholds.MaxHRSpikeThreshold) {
		t.Errorf("the ceiling itself is plausible: %v", got)
	}
}
