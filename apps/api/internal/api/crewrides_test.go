package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/crewplan"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// listedRide is a GET /api/training/crew-rides row.
type listedRide struct {
	ID        string   `json:"id"`
	CrewID    string   `json:"crewId"`
	CrewName  string   `json:"crewName"`
	Slug      string   `json:"slug"`
	RouteName string   `json:"routeName"`
	Date      string   `json:"date"`
	Going     []string `json:"going"`
	Mine      bool     `json:"mine"`
	Km        float64  `json:"km"`
	AscentM   float64  `json:"ascentM"`
	Minutes   float64  `json:"minutes"`
	TSS       float64  `json:"tss"`
	Kind      string   `json:"kind"`
}

func (h *crewPlanHarness) listRides(rider string) []listedRide {
	h.t.Helper()
	resp := h.as(rider, "cyclists", http.MethodGet, "/api/training/crew-rides?from="+cpToday, "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("list crew rides: status = %d, want 200", resp.StatusCode)
	}
	var out []listedRide
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// cpSetup is a crew of wilant (owner), sam and alex with one long ride on
// Saturday, on a 100 km route with about 1000 m of climbing.
type cpSetup struct {
	crewID string
	route  string // slug
	ride   string // ride id
	est    crewplan.Estimate
}

func (h *crewPlanHarness) setup() cpSetup {
	h.t.Helper()
	crewID := h.seedCrew("Sunday Club", "wilant", "sam", "alex")
	route := h.seedRoute("Hill Loop", "wilant", 100, 1000, crewID)
	ride := h.seedRide(crewID, route.Slug, cpSaturday, "wilant")
	return cpSetup{crewID: crewID, route: route.Slug, ride: ride.ID, est: crewplan.EstimateRoute(route.Stats)}
}

func TestCrewRideListCarriesTheEstimateAndWhoIsGoing(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()

	rides := h.listRides("wilant")
	if len(rides) != 1 {
		t.Fatalf("rides = %d, want 1", len(rides))
	}
	r := rides[0]
	if r.ID != s.ride || r.CrewName != "Sunday Club" || r.RouteName != "Hill Loop" || r.Date != cpSaturday {
		t.Errorf("ride = %+v", r)
	}
	if r.Kind != "long" || r.Mine || len(r.Going) != 0 {
		t.Errorf("kind/mine/going = %q/%v/%v, want long, not mine, nobody", r.Kind, r.Mine, r.Going)
	}
	if r.Km < 99 || r.Km > 101 || r.TSS < 150 || r.Minutes < 240 {
		t.Errorf("estimate = %+v, want about 100 km, 4.3 h, TSS 180", r)
	}

	h.mustGo("wilant", s.ride)
	h.mustGo("sam", s.ride)
	mine := h.listRides("wilant")[0]
	if !mine.Mine || len(mine.Going) != 2 {
		t.Errorf("after going: mine = %v going = %v, want mine and two names", mine.Mine, mine.Going)
	}
	if peer := h.listRides("alex")[0]; peer.Mine {
		t.Error("alex is not going but the list says mine")
	}
}

func TestCrewRideListIsOnlyTheCallersOwnCrews(t *testing.T) {
	h := newCrewPlanHarness(t)
	h.setup()
	if rides := h.listRides("outsider"); len(rides) != 0 {
		t.Errorf("a rider in no crew sees %d rides, want none", len(rides))
	}
}

func TestGoingCreatesTheFixedSessionInTheRidersOwnPlan(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	goal, err := h.training.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}

	out := h.mustGo("wilant", s.ride)
	if !out.Going || out.Workout == nil {
		t.Fatalf("response = %+v, want going with the fixed session", out)
	}

	row, ok := h.fixedRow("wilant", s.ride)
	if !ok {
		t.Fatal("no fixed row stored")
	}
	if row.Name != "Crew ride: Hill Loop" || row.Date != cpSaturday || row.Sport != "cycling" || row.Zone != workout.ZoneEndurance {
		t.Errorf("row = %q on %s (%s, %s)", row.Name, row.Date, row.Sport, row.Zone)
	}
	if row.GoalID != goal.ID {
		t.Errorf("goal = %q, want it linked to the focus goal %q", row.GoalID, goal.ID)
	}
	if len(row.Steps) != 1 || row.Steps[0].Intensity != workout.IntensityActive || row.Steps[0].Seconds != s.est.Seconds() {
		t.Errorf("steps = %+v, want one active step of the estimated %v s", row.Steps, s.est.Seconds())
	}
	if workout.PlannedSeconds(row.Steps) != s.est.Seconds() {
		t.Error("PlannedSeconds does not read the crew ride's length")
	}
	if scheduler.IsGenerated(row) || strings.HasPrefix(row.Description, scheduler.GeneratedDescription) {
		t.Error("the fixed row must never read as generated")
	}
	if !scheduler.IsKeySession(row) || scheduler.IsHardSession(row) {
		t.Error("a long crew ride is a key session and not a hard one")
	}
	if !strings.HasPrefix(row.Description, "Crew ride with Sunday Club: Hill Loop, ") ||
		!strings.Contains(row.Description, fmt.Sprintf("estimated TSS %.0f.", s.est.TSS)) {
		t.Errorf("description = %q", row.Description)
	}
	for _, name := range []string{"sam", "alex"} {
		if strings.Contains(row.Description, name) {
			t.Errorf("the description names %s: no other rider's name belongs in it", name)
		}
	}
}

func TestGoingWithoutAGoalStillCreatesTheRowUnlinked(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("wilant", s.ride)
	row, ok := h.fixedRow("wilant", s.ride)
	if !ok || row.GoalID != "" {
		t.Errorf("row = %+v, ok = %v, want a goal-less row", row, ok)
	}
}

func TestGoingTwiceKeepsOneRowAndOneGoingEntry(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("wilant", s.ride)
	h.mustGo("wilant", s.ride)

	n := 0
	for _, w := range h.stored("wilant") {
		if w.CrewRideID == s.ride {
			n++
		}
	}
	if n != 1 {
		t.Errorf("rows = %d, want exactly one per rider and ride", n)
	}
	if going, _ := h.sched.Going(context.Background(), s.ride); len(going) != 1 {
		t.Errorf("going = %v, want one entry", going)
	}
}

func TestGoingIsWrittenOnlyForTheCaller(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	// A body naming another rider is ignored: the rider comes from the session.
	if resp, _ := h.setGoing("wilant", s.ride, `{"going":true,"rider":"sam"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	going, _ := h.sched.Going(context.Background(), s.ride)
	if len(going) != 1 || going[0] != "wilant" {
		t.Errorf("going = %v, want only wilant", going)
	}
	if _, ok := h.fixedRow("sam", s.ride); ok {
		t.Error("sam got a fixed session written by someone else")
	}
}

func TestGoingRules(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()

	t.Run("a non-member is told there is no such ride", func(t *testing.T) {
		if resp, _ := h.setGoing("outsider", s.ride, `{"going":true}`); resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})
	t.Run("a pending member is not a member", func(t *testing.T) {
		if _, err := h.crews.RequestJoin(context.Background(), s.crewID, "pending"); err != nil {
			t.Fatal(err)
		}
		if resp, _ := h.setGoing("pending", s.ride, `{"going":true}`); resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})
	t.Run("an unknown ride is 404", func(t *testing.T) {
		if resp, _ := h.setGoing("wilant", "nope", `{"going":true}`); resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})
	t.Run("a ride that has passed cannot be joined", func(t *testing.T) {
		past := h.seedRide(s.crewID, s.route, "2026-10-01", "wilant")
		if resp, _ := h.setGoing("wilant", past.ID, `{"going":true}`); resp.StatusCode != http.StatusConflict {
			t.Errorf("status = %d, want 409", resp.StatusCode)
		}
		if _, ok := h.fixedRow("wilant", past.ID); ok {
			t.Error("a fixed session was made for a ride in the past")
		}
	})
	t.Run("a ride whose route is gone cannot be joined", func(t *testing.T) {
		gone := h.seedRide(s.crewID, "no-such-route", cpSunday, "wilant")
		if resp, _ := h.setGoing("wilant", gone.ID, `{"going":true}`); resp.StatusCode != http.StatusConflict {
			t.Errorf("status = %d, want 409", resp.StatusCode)
		}
	})
	t.Run("today is not in the past", func(t *testing.T) {
		today := h.seedRide(s.crewID, s.route, cpToday, "wilant")
		if resp, _ := h.setGoing("wilant", today.ID, `{"going":true}`); resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})
	t.Run("a viewer cannot", func(t *testing.T) {
		resp := h.as("wilant", "guests", http.MethodPut, "/api/training/crew-rides/"+s.ride+"/going", `{"going":true}`)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})
	t.Run("a malformed body is 400", func(t *testing.T) {
		if resp, _ := h.setGoing("wilant", s.ride, `{`); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})
}

func TestThePlanPageReadsAFixedSessionAsACrewRide(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("wilant", s.ride)
	h.mustGo("sam", s.ride)

	var found *crewWorkoutOut
	for _, w := range h.workoutsOf("wilant") {
		if w.CrewRide != nil {
			w := w
			found = &w
		}
	}
	if found == nil || found.CrewRide == nil {
		t.Fatal("the fixed session carries no crewRide")
	}
	c := found.CrewRide
	if c.RideID != s.ride || c.CrewID != s.crewID || c.CrewName != "Sunday Club" || c.RouteName != "Hill Loop" {
		t.Errorf("crewRide = %+v", c)
	}
	if !c.Going || c.Kind != "long" || c.Orphaned != "" {
		t.Errorf("going/kind/orphaned = %v/%q/%q", c.Going, c.Kind, c.Orphaned)
	}
	if got := int(c.EstimatedTSS + 0.5); got != int(s.est.TSS+0.5) {
		t.Errorf("estimatedTss = %v, want %v", c.EstimatedTSS, s.est.TSS)
	}
	sort.Strings(c.GoingNames)
	if strings.Join(c.GoingNames, ",") != "sam,wilant" {
		t.Errorf("goingNames = %v", c.GoingNames)
	}
}

func TestCancellingTheRideOrphansTheSessionWithoutTouchingIt(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("wilant", s.ride)
	before, _ := h.fixedRow("wilant", s.ride)

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+s.crewID+"/rides/"+s.ride, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete ride: status = %d", resp.StatusCode)
	}

	after, ok := h.fixedRow("wilant", s.ride)
	if !ok {
		t.Fatal("deleting the crew's ride deleted a rider's session")
	}
	if after.UpdatedAt != before.UpdatedAt || after.Date != before.Date || after.Description != before.Description {
		t.Error("deleting the crew's ride rewrote the rider's session")
	}
	var orphan string
	for _, w := range h.workoutsOf("wilant") {
		if w.CrewRide != nil {
			orphan = w.CrewRide.Orphaned
			if w.CrewRide.Going {
				t.Error("a cancelled ride still reads as going")
			}
		}
	}
	if orphan != "cancelled" {
		t.Errorf("orphaned = %q, want cancelled", orphan)
	}
}

func TestRemovingAMemberOrphansTheirSessionAndDropsTheirGoingRow(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("sam", s.ride)
	h.mustGo("alex", s.ride)

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/crews/"+s.crewID+"/members/sam", "")
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove member: status = %d", resp.StatusCode)
	}

	if _, ok := h.fixedRow("sam", s.ride); !ok {
		t.Fatal("removing a member deleted their session")
	}
	var orphan string
	for _, w := range h.workoutsOf("sam") {
		if w.CrewRide != nil {
			orphan = w.CrewRide.Orphaned
		}
	}
	if orphan != "left" {
		t.Errorf("orphaned = %q, want left", orphan)
	}
	if going, _ := h.sched.Going(context.Background(), s.ride); len(going) != 1 || going[0] != "alex" {
		t.Errorf("going = %v, want only alex", going)
	}
	if _, ok := h.fixedRow("alex", s.ride); !ok {
		t.Error("another member's session was touched")
	}
}

func TestDeletingTheFixedSessionByHandAlsoLeavesTheRide(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("wilant", s.ride)
	row, _ := h.fixedRow("wilant", s.ride)

	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/workouts/"+row.ID, "")
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete workout: status = %d", resp.StatusCode)
	}
	if is, _ := h.sched.IsGoing(context.Background(), s.ride, "wilant"); is {
		t.Error("the going row survived the rider deleting their own session")
	}
}

func TestLeavingDeletesTheFixedSessionAndTheGoingRow(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	h.mustGo("wilant", s.ride)
	out := h.mustLeave("wilant", s.ride)
	if out.Going {
		t.Error("response says still going")
	}
	if _, ok := h.fixedRow("wilant", s.ride); ok {
		t.Error("the fixed session survived leaving")
	}
	if is, _ := h.sched.IsGoing(context.Background(), s.ride, "wilant"); is {
		t.Error("the going row survived leaving")
	}
	// Leaving when not going is a no-op, not an error.
	h.mustLeave("wilant", s.ride)
}

func TestAutoPushNeverSendsACrewRide(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	today := h.seedRide(s.crewID, s.route, cpToday, "wilant")
	goal, err := h.training.CreateGoal(context.Background(), workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	h.mustGo("wilant", today.ID)
	// A plain plan-made session today is the control: it is pushed.
	if _, err := h.training.CreateWorkout(context.Background(), generated("wilant", goal.ID, "Control ride", cpToday, 3600)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(context.Background(), workout.RiderProfile{Rider: "wilant", AutoPushWorkouts: true, HoursPerAvailableDay: 1, AvailableDays: []string{"wed"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}

	h.srv.AutoScheduleTick(context.Background())

	if n := len(h.garmin.remoteWorkouts); n != 1 {
		t.Errorf("the account holds %d workouts, want only the control session", n)
	}
	for _, name := range h.garmin.remoteWorkouts {
		if strings.Contains(name, "Crew ride") {
			t.Errorf("a crew ride was auto-pushed: %q", name)
		}
	}
}
