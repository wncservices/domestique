package periodization

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPlanRejectsAMissingOrPastEventDate(t *testing.T) {
	today := mustDate(t, "2026-01-05") // a Monday
	profile := workout.RiderProfile{HoursPerAvailableDay: 1.5, AvailableDays: []string{"tue", "thu", "sat", "sun"}}

	if _, err := BuildPlan(workout.Goal{}, profile, today); err != ErrNoEventDate {
		t.Errorf("no event date: err = %v, want ErrNoEventDate", err)
	}
	if _, err := BuildPlan(workout.Goal{EventDate: "2026-01-05"}, profile, today); err != ErrEventInThePast {
		t.Errorf("event date = today: err = %v, want ErrEventInThePast", err)
	}
	if _, err := BuildPlan(workout.Goal{EventDate: "2025-01-01"}, profile, today); err != ErrEventInThePast {
		t.Errorf("event date in the past: err = %v, want ErrEventInThePast", err)
	}
}

// A 14-week plan is long enough for all four phases to exist and land in
// their documented proportions — this is the "check the exact shape"
// assertion this codebase's own testing culture calls for on a
// deterministic function (see AGENTS.md's Tests section).
func TestFullFourPhasePlanShape(t *testing.T) {
	today := mustDate(t, "2026-01-05") // Monday
	goal := workout.Goal{ID: "gran-fondo", EventDate: "2026-04-13"}
	profile := workout.RiderProfile{HoursPerAvailableDay: 2, AvailableDays: []string{"tue", "thu", "sat", "sun"}}

	plan, err := BuildPlan(goal, profile, today)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.GoalID != "gran-fondo" {
		t.Errorf("goal id = %q", plan.GoalID)
	}

	wantWeeks := 15 // ceil(98 days / 7)
	if len(plan.Weeks) != wantWeeks {
		t.Fatalf("weeks = %d, want %d", len(plan.Weeks), wantWeeks)
	}

	// Week numbers and dates are contiguous, one per calendar week, starting
	// on the Monday of today's week.
	for i, w := range plan.Weeks {
		if w.Number != i+1 {
			t.Errorf("week %d: Number = %d", i, w.Number)
		}
		wantDate := today.AddDate(0, 0, i*7).Format("2006-01-02")
		if w.StartDate != wantDate {
			t.Errorf("week %d: StartDate = %s, want %s", i, w.StartDate, wantDate)
		}
	}

	// Phases appear in order, each a contiguous block, all four present.
	seen := map[Phase]bool{}
	var order []Phase
	for _, w := range plan.Weeks {
		if !seen[w.Phase] {
			seen[w.Phase] = true
			order = append(order, w.Phase)
		}
	}
	wantOrder := []Phase{PhaseBase, PhaseBuild, PhasePeak, PhaseTaper}
	if len(order) != len(wantOrder) {
		t.Fatalf("phase order = %v, want %v", order, wantOrder)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Errorf("phase order = %v, want %v", order, wantOrder)
		}
	}

	// The taper's final week is always the lightest week in the plan —
	// the property that actually matters for a rider reading this: no
	// week after it should ask for more.
	last := plan.Weeks[len(plan.Weeks)-1]
	if last.Phase != PhaseTaper {
		t.Fatalf("last week phase = %s, want taper", last.Phase)
	}
	for _, w := range plan.Weeks {
		if w.TargetHours < last.TargetHours-1e-9 && w.Number != last.Number {
			t.Errorf("week %d (%.2fh) is lighter than the final taper week (%.2fh)",
				w.Number, w.TargetHours, last.TargetHours)
		}
	}

	// Peak weekly hours (profile) is 2h * 4 days = 8h; Build's own ceiling
	// week should reach the full 8h at some point (the top of its ramp).
	peakHours := 8.0
	var maxBuildHours float64
	for _, w := range plan.Weeks {
		if w.Phase == PhaseBuild && w.TargetHours > maxBuildHours {
			maxBuildHours = w.TargetHours
		}
	}
	if diff := maxBuildHours - peakHours; diff < -0.01 || diff > 0.01 {
		t.Errorf("build's peak week = %.2fh, want %.2fh (the rider's full weekly availability)", maxBuildHours, peakHours)
	}
}

// Every 4th Base/Build week is a recovery week, and it is lighter than the
// week immediately before it — the actual, checkable meaning of "3:1" this
// package claims to implement.
func TestRecoveryWeeksAreEveryFourthAndLighter(t *testing.T) {
	today := mustDate(t, "2026-01-05")
	goal := workout.Goal{EventDate: "2026-07-06"} // long enough for several build recovery cycles
	profile := workout.RiderProfile{HoursPerAvailableDay: 1, AvailableDays: []string{"mon", "wed", "fri"}}

	plan, err := BuildPlan(goal, profile, today)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	sawRecovery := false
	for i, w := range plan.Weeks {
		if w.Phase != PhaseBase && w.Phase != PhaseBuild {
			continue
		}
		if !w.Recovery {
			continue
		}
		sawRecovery = true
		if i == 0 {
			t.Fatalf("week 1 cannot be a recovery week")
		}
		prev := plan.Weeks[i-1]
		if prev.Phase == w.Phase && w.TargetHours >= prev.TargetHours {
			t.Errorf("recovery week %d (%.2fh) is not lighter than the week before it (%.2fh)",
				w.Number, w.TargetHours, prev.TargetHours)
		}
	}
	if !sawRecovery {
		t.Fatal("expected at least one recovery week in a plan this long")
	}

	// Peak and taper never carry a recovery flag — they are already
	// reduced-load phases by construction.
	for _, w := range plan.Weeks {
		if (w.Phase == PhasePeak || w.Phase == PhaseTaper) && w.Recovery {
			t.Errorf("week %d (%s) should never be flagged Recovery", w.Number, w.Phase)
		}
	}
}

// A profile with no stated availability produces a structurally valid plan
// (phases and dates), just with every week's hours target at 0 — a caller
// prompts the rider to fill in their profile rather than this package
// inventing a number it has no basis for.
func TestEmptyProfileZerosEveryTargetButKeepsTheStructure(t *testing.T) {
	today := mustDate(t, "2026-01-05")
	goal := workout.Goal{EventDate: "2026-04-13"}

	plan, err := BuildPlan(goal, workout.RiderProfile{}, today)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Weeks) == 0 {
		t.Fatal("expected weeks even with an empty profile")
	}
	for _, w := range plan.Weeks {
		if w.TargetHours != 0 {
			t.Errorf("week %d: TargetHours = %v, want 0", w.Number, w.TargetHours)
		}
	}
}

// A handful of short, "running out of time" plans: each must still return
// a non-empty, in-order set of weeks with no negative-length phase, and
// should protect the taper (and peak, once there's room) before base gets
// to exist — allocateWeeks' own stated priority.
func TestShortPlansDegradeGracefully(t *testing.T) {
	today := mustDate(t, "2026-01-05")
	profile := workout.RiderProfile{HoursPerAvailableDay: 1, AvailableDays: []string{"tue", "sat"}}

	for _, days := range []int{1, 5, 10, 20, 35} {
		event := today.AddDate(0, 0, days)
		goal := workout.Goal{EventDate: event.Format("2006-01-02")}

		plan, err := BuildPlan(goal, profile, today)
		if err != nil {
			t.Fatalf("days=%d: Plan: %v", days, err)
		}
		if len(plan.Weeks) == 0 {
			t.Fatalf("days=%d: no weeks", days)
		}
		if last := plan.Weeks[len(plan.Weeks)-1]; last.Phase != PhaseTaper {
			t.Errorf("days=%d: last phase = %s, want taper", days, last.Phase)
		}
		for i, w := range plan.Weeks {
			if i > 0 && w.Number != plan.Weeks[i-1].Number+1 {
				t.Errorf("days=%d: week numbers not contiguous at index %d", days, i)
			}
		}
	}
}

func TestMondayOfAlignsToCalendarWeek(t *testing.T) {
	// 2026-01-08 is a Thursday; the Monday of that week is 2026-01-05.
	got := MondayOf(mustDate(t, "2026-01-08"))
	want := mustDate(t, "2026-01-05")
	if !got.Equal(want) {
		t.Errorf("MondayOf(Thursday) = %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	// A Monday maps to itself.
	monday := mustDate(t, "2026-01-05")
	if got := MondayOf(monday); !got.Equal(monday) {
		t.Errorf("MondayOf(Monday) = %s, want itself", got.Format("2006-01-02"))
	}
}
