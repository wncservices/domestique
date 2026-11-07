package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
)

func TestReachedFitnessStopsBeforeTheTaperOrTheLastWeek(t *testing.T) {
	week := func(p periodization.Phase, ctl float64, capped bool) periodization.Week {
		return periodization.Week{Phase: p, PlannedCTL: ctl, LoadCapped: capped}
	}
	// A taper: the CTL at its start, the taper's own cap flag ignored.
	r, c := reachedFitness([]periodization.Week{
		week(periodization.PhaseBuild, 50, false), week(periodization.PhasePeak, 60, false), week(periodization.PhaseTaper, 65, true)})
	if r != 65 || c {
		t.Errorf("taper plan: reached %v capped %v, want 65 and not capped", r, c)
	}
	// No taper: the last week's start, and its cap flag is not part of the build.
	r, c = reachedFitness([]periodization.Week{
		week(periodization.PhaseBuild, 50, false), week(periodization.PhasePeak, 60, false), week(periodization.PhasePeak, 65, true)})
	if r != 65 || c {
		t.Errorf("no-taper plan: reached %v capped %v, want 65 and not capped", r, c)
	}
	_, c = reachedFitness([]periodization.Week{week(periodization.PhaseBuild, 50, true), week(periodization.PhasePeak, 60, false)})
	if !c {
		t.Error("an earlier capped week must count")
	}
}
