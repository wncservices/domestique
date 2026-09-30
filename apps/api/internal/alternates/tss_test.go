package alternates

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

// The menu's TSS prices the session's work, not its warm-up: a 60-minute
// VO2max rung must not cost what a 60-minute easy ride does.
func TestOptionTSSPricesTheWorkNotTheWarmup(t *testing.T) {
	w := planned(t, model.SportCycling, "vo2max", 4)
	harder, ok := find(Options(w, 10, false, profile), Harder) // rung 5, 60 minutes
	if !ok || harder.Seconds != 60*60 {
		t.Fatalf("harder = %+v (%v), want the 60-minute rung 5", harder, ok)
	}
	easy := TSS(model.SportCycling, endurance(model.SportCycling, 3600, false).Steps, profile.FTPWatts)
	if harder.TSS <= easy*1.2 {
		t.Errorf("VO2max L5 TSS = %v, 60-minute endurance = %v: it must cost clearly more", harder.TSS, easy)
	}
	if got := TSS(model.SportCycling, harder.Steps, 0); got != 0 {
		t.Errorf("TSS without an FTP = %v, want 0 (the UI shows '- TSS')", got)
	}
}
