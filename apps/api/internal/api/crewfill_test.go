package api_test

import (
	"context"
	"math"
	"net/http"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const cpNextMonday = "2026-10-12"

// planFor gives rider a Stay Fit goal and a four-day week, 2 h a day.
func (h *crewPlanHarness) planFor(rider string) workout.Goal {
	h.t.Helper()
	ctx := context.Background()
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: rider, Name: "Stay Fit"})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: rider, FTPWatts: 250, HoursPerAvailableDay: 2, AvailableDays: []string{"tue", "thu", "sat", "sun"},
	}); err != nil {
		h.t.Fatal(err)
	}
	return goal
}

func (h *crewPlanHarness) fillWeek(rider string, goal workout.Goal, weekStart string) {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodPost, "/api/training/goals/"+goal.ID+"/schedule", `{"weekStart":"`+weekStart+`"}`)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("fill %s: status = %d", weekStart, resp.StatusCode)
	}
}

// generated is the rider's plan-made sessions in a week, by date.
func (h *crewPlanHarness) generatedIn(rider, from, to string) map[string]workout.Workout {
	h.t.Helper()
	out := map[string]workout.Workout{}
	for _, w := range h.stored(rider) {
		if w.Date >= from && w.Date <= to && scheduler.IsGenerated(w) {
			out[w.Date] = w
		}
	}
	return out
}

func hoursIn(ws map[string]workout.Workout) float64 {
	total := 0.0
	for _, w := range ws {
		total += workout.PlannedSeconds(w.Steps) / 3600
	}
	return total
}

func TestAWeekFilledAfterJoiningIsBuiltAroundTheRide(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	ride := h.seedRide(s.crewID, s.route, cpNextSat, "wilant")
	wilant, sam := h.planFor("wilant"), h.planFor("sam")
	h.mustGo("wilant", ride.ID)

	h.fillWeek("wilant", wilant, cpNextMonday)
	h.fillWeek("sam", sam, cpNextMonday)

	mine := h.generatedIn("wilant", cpNextMonday, "2026-10-18")
	theirs := h.generatedIn("sam", cpNextMonday, "2026-10-18")
	if _, ok := mine[cpNextSat]; ok {
		t.Error("the ride's day holds a generated session")
	}
	for _, w := range mine {
		if scheduler.IsLongRideName(w.Name) {
			t.Errorf("a generated long ride %q remains: the crew ride is the week's long ride", w.Name)
		}
	}
	if len(theirs) <= len(mine) {
		t.Errorf("sam has %d sessions and wilant %d: wilant's week should be smaller", len(theirs), len(mine))
	}
	// The ride's hours come off the week, but never more than half of it.
	want := math.Max(hoursIn(theirs)-s.est.Hours, hoursIn(theirs)/2)
	if got := hoursIn(mine); math.Abs(got-want) > 0.2 {
		t.Errorf("generated volume = %.2f h, want about %.2f (sam's %.2f less the ride, floored at half)", got, want, hoursIn(theirs))
	}
	if _, ok := h.fixedRow("wilant", ride.ID); !ok {
		t.Error("the fixed row went missing")
	}
}

func TestARideJoinedBeforeAnyGoalStillTakesItsDay(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	ride := h.seedRide(s.crewID, s.route, cpNextSat, "wilant")
	h.mustGo("wilant", ride.ID) // no goal yet: the row is not linked to one
	if row, _ := h.fixedRow("wilant", ride.ID); row.GoalID != "" {
		t.Fatalf("setup: the row is linked to %q", row.GoalID)
	}
	goal := h.planFor("wilant")
	h.fillWeek("wilant", goal, cpNextMonday)
	if _, ok := h.generatedIn("wilant", cpNextMonday, "2026-10-18")[cpNextSat]; ok {
		t.Error("a goal-less crew ride did not block its day")
	}
}

func TestAWeekHoldingOnlyACrewRideIsStillFilledByTheTick(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	ride := h.seedRide(s.crewID, s.route, cpNextSat, "wilant")
	h.planFor("wilant")
	h.mustGo("wilant", ride.ID)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(context.Background())

	if got := h.generatedIn("wilant", cpNextMonday, "2026-10-18"); len(got) == 0 {
		t.Error("next week was recorded as filled because it held a crew ride, and nothing was planned")
	}
}

func TestFillingAgainNeverPutsASessionBackOnTheRidesDay(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	ride := h.seedRide(s.crewID, s.route, cpNextSat, "wilant")
	goal := h.planFor("wilant")
	h.mustGo("wilant", ride.ID)
	h.fillWeek("wilant", goal, cpNextMonday)
	before := len(h.generatedIn("wilant", cpNextMonday, "2026-10-18"))
	h.fillWeek("wilant", goal, cpNextMonday)
	got := h.generatedIn("wilant", cpNextMonday, "2026-10-18")
	if len(got) != before {
		t.Errorf("a second fill changed the week from %d to %d sessions", before, len(got))
	}
	if _, ok := got[cpNextSat]; ok {
		t.Error("a session came back on the ride's day")
	}
}
