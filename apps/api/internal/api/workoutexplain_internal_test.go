package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
)

func explainPlan() periodization.Plan {
	return periodization.Plan{TotalWeeks: 12, Weeks: []periodization.Week{
		{Number: 2, StartDate: "2026-10-05", Phase: periodization.PhaseBuild},
		{Number: 3, StartDate: "2026-10-12", Phase: periodization.PhaseBuild},
		{Number: 4, StartDate: "2026-10-19", Phase: periodization.PhaseBuild, Recovery: true},
		{Number: 5, StartDate: "2026-10-26", Phase: periodization.PhasePeak},
		{Number: 6, StartDate: "2026-11-02", Phase: periodization.PhaseTaper},
	}}
}

func TestWhyLineForABuildWeek(t *testing.T) {
	// Wednesday of week 2 sits before a build week, so no suffix.
	if got, want := whyLine(explainPlan(), "Spring sportive", "2026-10-07"), "Build week 2 of 12 for Spring sportive"; got != want {
		t.Fatalf("whyLine = %q, want %q", got, want)
	}
}

func TestWhyLineBeforeARecoveryWeek(t *testing.T) {
	// The first and last day of the week both count.
	for _, day := range []string{"2026-10-12", "2026-10-18"} {
		if got, want := whyLine(explainPlan(), "Spring sportive", day), "Build week 3 of 12 for Spring sportive · recovery week next"; got != want {
			t.Errorf("%s: whyLine = %q, want %q", day, got, want)
		}
	}
}

func TestWhyLineBeforeTheTaper(t *testing.T) {
	if got, want := whyLine(explainPlan(), "Spring sportive", "2026-10-28"), "Peak week 5 of 12 for Spring sportive · taper next"; got != want {
		t.Fatalf("whyLine = %q, want %q", got, want)
	}
}

func TestWhyLineIsEmptyOutsideThePlan(t *testing.T) {
	for _, day := range []string{"2026-09-28", "2026-12-01", ""} {
		if got := whyLine(explainPlan(), "Spring sportive", day); got != "" {
			t.Errorf("%q: whyLine = %q, want empty", day, got)
		}
	}
}
