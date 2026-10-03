package projection

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/pacing"
)

// The race-day projection and the pacing plan size an event at the same
// intensity factor, from the one implementation in pacing.EventIF: at every
// duration, including the band edges.
func TestBandIntensityIsThePacingEventIF(t *testing.T) {
	for _, hours := range []float64{0.5, 1.99, 2, 2.5, 4, 4.01, 8} {
		if got, want := BandFor(hours).IF, pacing.EventIF(hours); got != want {
			t.Errorf("BandFor(%v).IF = %v, pacing.EventIF = %v", hours, got, want)
		}
	}
}
