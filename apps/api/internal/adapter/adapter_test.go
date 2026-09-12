package adapter

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
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

// A goal far enough out that a lookbackWeeks-deep reconstruction always
// lands comfortably inside Base, with plenty of Peak/Taper weeks left in
// the forward plan too.
var (
	fixtureGoal    = workout.Goal{ID: "gran-fondo", EventDate: "2026-12-01"}
	fixtureProfile = workout.RiderProfile{HoursPerAvailableDay: 1, AvailableDays: []string{"mon", "wed", "fri"}}
)

// sessionOn builds one completed session dated d with the given duration —
// Reconcile only ever reads Date and DurationSeconds.
func sessionOn(date string, hours float64) workout.CompletedSession {
	return workout.CompletedSession{Date: date, DurationSeconds: hours * 3600}
}

// historicalWeeks reconstructs the same lookbackWeeks-deep plan Reconcile
// itself builds internally (see its own doc comment for why this needs to
// be one continuously-anchored plan, not one fresh BuildPlan call per
// week), so tests can seed sessions against the exact dates and targets
// Reconcile will compare against.
func historicalWeeks(t *testing.T, today time.Time) []periodization.Week {
	t.Helper()
	pastAnchor := periodization.MondayOf(today).AddDate(0, 0, -7*lookbackWeeks)
	plan, err := periodization.BuildPlan(fixtureGoal, fixtureProfile, pastAnchor)
	if err != nil {
		t.Fatalf("BuildPlan(pastAnchor): %v", err)
	}
	if len(plan.Weeks) < lookbackWeeks {
		t.Fatalf("historical plan too short: %d weeks, want >= %d", len(plan.Weeks), lookbackWeeks)
	}
	return plan.Weeks[:lookbackWeeks]
}

func TestReconcileNoOpWithoutAnySessionsEver(t *testing.T) {
	today := mustDate(t, "2026-01-05") // a Monday
	plan, err := periodization.BuildPlan(fixtureGoal, fixtureProfile, today)
	if err != nil {
		t.Fatal(err)
	}

	out := Reconcile(fixtureGoal, fixtureProfile, plan, nil, today)

	if out.Adjustment != 1 {
		t.Errorf("Adjustment = %v, want 1 (no completed sessions at all yet, not proof of zero training)", out.Adjustment)
	}
	for i, w := range out.Weeks {
		if w.Adjusted {
			t.Errorf("week %d: Adjusted = true, want false", i+1)
		}
		if w.TargetHours != plan.Weeks[i].TargetHours {
			t.Errorf("week %d: TargetHours = %v, want unchanged %v", i+1, w.TargetHours, plan.Weeks[i].TargetHours)
		}
	}
}

func TestReconcilePerfectComplianceMeansNoChange(t *testing.T) {
	today := mustDate(t, "2026-01-05")
	plan, err := periodization.BuildPlan(fixtureGoal, fixtureProfile, today)
	if err != nil {
		t.Fatal(err)
	}

	var sessions []workout.CompletedSession
	for _, wk := range historicalWeeks(t, today) {
		sessions = append(sessions, sessionOn(wk.StartDate, wk.TargetHours))
	}

	out := Reconcile(fixtureGoal, fixtureProfile, plan, sessions, today)

	if out.Adjustment != 1 {
		t.Errorf("Adjustment = %v, want 1 (trained exactly on target every week)", out.Adjustment)
	}
	for i, w := range out.Weeks {
		if w.Adjusted {
			t.Errorf("week %d: Adjusted = true, want false", i+1)
		}
	}
}

// Three untrained weeks in a row (a ratio of 0, clamped to perWeekMin 0.4)
// must ease future weeks off, floored at minAdjustment rather than
// collapsing toward 0 — and must never touch the current week (index 0).
// A dummy session dated today establishes that the rider does have a real
// data feed (see TestReconcileNoOpWithoutAnySessionsEver for the case
// where they don't) — the three historical weeks themselves get nothing.
func TestReconcileScalesDownFutureWeeksAfterUndertrainedHistory(t *testing.T) {
	today := mustDate(t, "2026-01-05")
	plan, err := periodization.BuildPlan(fixtureGoal, fixtureProfile, today)
	if err != nil {
		t.Fatal(err)
	}

	sessions := []workout.CompletedSession{sessionOn(today.Format("2006-01-02"), 0.5)}
	out := Reconcile(fixtureGoal, fixtureProfile, plan, sessions, today)

	if diff := out.Adjustment - minAdjustment; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("Adjustment = %v, want the floor %v", out.Adjustment, minAdjustment)
	}
	if out.Weeks[0].Adjusted || out.Weeks[0].TargetHours != plan.Weeks[0].TargetHours {
		t.Errorf("current week changed: %+v, want unchanged %+v", out.Weeks[0], plan.Weeks[0])
	}
	assertFutureBaseBuildScaled(t, plan, out, minAdjustment)
	assertPeakAndTaperUntouched(t, plan, out)
}

// Three heavily-overtrained weeks (a ratio of 2, clamped to perWeekMax 1.3)
// must raise future weeks, capped at maxAdjustment rather than compounding
// an overreach.
func TestReconcileScalesUpFutureWeeksAfterOvertrainedHistory(t *testing.T) {
	today := mustDate(t, "2026-01-05")
	plan, err := periodization.BuildPlan(fixtureGoal, fixtureProfile, today)
	if err != nil {
		t.Fatal(err)
	}

	var sessions []workout.CompletedSession
	for _, wk := range historicalWeeks(t, today) {
		sessions = append(sessions, sessionOn(wk.StartDate, wk.TargetHours*2))
	}

	out := Reconcile(fixtureGoal, fixtureProfile, plan, sessions, today)

	if diff := out.Adjustment - maxAdjustment; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("Adjustment = %v, want the ceiling %v", out.Adjustment, maxAdjustment)
	}
	assertFutureBaseBuildScaled(t, plan, out, maxAdjustment)
	assertPeakAndTaperUntouched(t, plan, out)
}

// A rider's actual training for one week can be more than one session —
// Reconcile must sum them, not just look at the last one seen.
func TestReconcileSumsMultipleSessionsInTheSameWeek(t *testing.T) {
	today := mustDate(t, "2026-01-05")
	plan, err := periodization.BuildPlan(fixtureGoal, fixtureProfile, today)
	if err != nil {
		t.Fatal(err)
	}

	var sessions []workout.CompletedSession
	for _, wk := range historicalWeeks(t, today) {
		half := wk.TargetHours / 2
		sessions = append(sessions, sessionOn(wk.StartDate, half), sessionOn(wk.StartDate, half))
	}

	out := Reconcile(fixtureGoal, fixtureProfile, plan, sessions, today)

	if out.Adjustment != 1 {
		t.Errorf("Adjustment = %v, want 1 (two half-target sessions per week still sum to full compliance)", out.Adjustment)
	}
}

func assertFutureBaseBuildScaled(t *testing.T, plan, out periodization.Plan, factor float64) {
	t.Helper()
	found := false
	for i := 1; i < len(out.Weeks); i++ {
		if out.Weeks[i].Phase != periodization.PhaseBase && out.Weeks[i].Phase != periodization.PhaseBuild {
			continue
		}
		found = true
		want := plan.Weeks[i].TargetHours * factor
		if diff := out.Weeks[i].TargetHours - want; diff < -1e-9 || diff > 1e-9 {
			t.Errorf("week %d: TargetHours = %v, want %v (%.2fx)", i+1, out.Weeks[i].TargetHours, want, factor)
		}
		if !out.Weeks[i].Adjusted {
			t.Errorf("week %d: Adjusted = false, want true", i+1)
		}
	}
	if !found {
		t.Fatal("no future Base/Build week found to assert against")
	}
}

func assertPeakAndTaperUntouched(t *testing.T, plan, out periodization.Plan) {
	t.Helper()
	found := false
	for i, w := range out.Weeks {
		if w.Phase != periodization.PhasePeak && w.Phase != periodization.PhaseTaper {
			continue
		}
		found = true
		if w.Adjusted {
			t.Errorf("week %d (%s): Adjusted = true, want false", i+1, w.Phase)
		}
		if w.TargetHours != plan.Weeks[i].TargetHours {
			t.Errorf("week %d (%s): TargetHours = %v, want unchanged %v", i+1, w.Phase, w.TargetHours, plan.Weeks[i].TargetHours)
		}
	}
	if !found {
		t.Fatal("fixture plan has no Peak/Taper week to assert against")
	}
}
