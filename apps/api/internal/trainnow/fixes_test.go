package trainnow

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
)

// A suggestion's TSS prices the work, so a 60-minute VO2max session costs
// clearly more than a 60-minute easy ride; without an FTP there is nothing to
// show (the UI prints "- TSS").
func TestSuggestionTSSPricesTheWorkNotTheWarmup(t *testing.T) {
	in := base(60)
	in.Today = planSession(t, model.SportCycling, "vo2max", 5)
	in.PlannedIsToday = true
	out := Suggest(in)
	p, okP := byKind(out, Planned)
	e, okE := byKind(out, Easy)
	if !okP || !okE || p.Seconds != 60*60 || e.Seconds != 60*60 {
		t.Fatalf("suggestions = %+v, want a 60-minute VO2max session and a 60-minute easy ride", out)
	}
	if p.TSS <= e.TSS*1.2 {
		t.Errorf("VO2max = %v TSS, easy = %v TSS: the structured session must cost clearly more", p.TSS, e.TSS)
	}

	in.Profile.FTPWatts = 0
	for _, s := range Suggest(in) {
		if s.TSS != 0 {
			t.Errorf("%s TSS = %v without an FTP, want 0", s.Kind, s.TSS)
		}
	}
}

// Where the 1.25 cap and the 30-minute floor disagree, the cap wins: a 20-minute
// stand-in gives 25 minutes, not 30. Only the fixed warmup and cooldown is a hard
// floor, and N still bounds everything.
func TestTheCapBeatsTheThirtyMinuteFloor(t *testing.T) {
	in := base(60)
	in.Today = enduranceSession(20, false)
	in.PlannedIsToday = true
	p, ok := byKind(Suggest(in), Planned)
	if !ok || p.Seconds != 25*60 {
		t.Fatalf("planned = %+v (%v), want 25 minutes (1.25 x 20), not the 30-minute floor", p, ok)
	}

	// N still bounds it: with only 22 minutes the ride is 22, under the cap.
	in.Minutes = 22
	if p, _ := byKind(Suggest(in), Planned); p.Seconds != 22*60 {
		t.Errorf("planned at N=22 = %v s, want 1320", p.Seconds)
	}
}
