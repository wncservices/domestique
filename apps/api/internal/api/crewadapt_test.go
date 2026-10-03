package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A rider on Wednesday/Friday/Saturday has the week's one hard session on
// Wednesday, today. A long crew ride on Thursday makes that session the day
// before it.
func (h *crewPlanHarness) wedFriSat(rider string) workout.Goal {
	h.t.Helper()
	ctx := context.Background()
	goal, err := h.training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: rider, Name: "Stay Fit"})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: rider, HoursPerAvailableDay: 3, AvailableDays: []string{"wed", "fri", "sat"},
	}); err != nil {
		h.t.Fatal(err)
	}
	return goal
}

func TestEasingTheDayBeforeASurvivesReplanAndTheTick(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	// An endurance-length ride (about 1.7 h): it leaves room for the week's hard
	// session, which a long one would squeeze out of a short week.
	short := h.seedRoute("Flat Loop", "wilant", 45, 0, s.crewID)
	ride := h.seedRide(s.crewID, short.Slug, cpThursday, "wilant")
	h.wedFriSat("wilant")
	h.mustGo("wilant", ride.ID)

	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replan: status = %d", resp.StatusCode)
	}
	wed := h.generatedIn("wilant", cpToday, cpToday)
	var eased workout.Workout
	for _, w := range h.stored("wilant") {
		if w.Date == cpToday && w.CrewRideID == "" {
			eased = w
		}
	}
	if eased.ID == "" {
		t.Fatalf("replan made nothing on Wednesday (generated: %v)", wed)
	}
	if !strings.Contains(eased.Description, "the day before your crew ride") || eased.Zone != workout.ZoneEndurance {
		t.Fatalf("Wednesday = %s %q, want the hard session eased the day before the ride", eased.Zone, eased.Description)
	}
	if got := strings.Count(eased.Description, scheduler.AdjustedMarker); got != 1 {
		t.Errorf("adjusted %d times, want exactly once", got)
	}

	// The tick, run again and again, changes nothing further and never touches
	// the crew ride row.
	row, _ := h.fixedRow("wilant", ride.ID)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		h.srv.AutoScheduleTick(context.Background())
		h.srv.AdaptWorkouts(context.Background())
	}
	after, _ := h.get("wilant", eased.ID)
	if strings.Count(after.Description, scheduler.AdjustedMarker) != 1 {
		t.Errorf("the tick adjusted Wednesday again: %q", after.Description)
	}
	rowAfter, ok := h.fixedRow("wilant", ride.ID)
	if !ok || rowAfter.UpdatedAt != row.UpdatedAt || rowAfter.Date != row.Date || rowAfter.Description != row.Description {
		t.Error("automation touched the crew ride row")
	}
	if got := h.generatedIn("wilant", cpThursday, cpThursday); len(got) != 0 {
		t.Errorf("a generated session came back on the ride's day: %v", got)
	}
}

func TestNothingAutomaticEverMovesEasesOrSkipsACrewRideRow(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	today := h.seedRide(s.crewID, s.route, cpToday, "wilant")
	h.wedFriSat("wilant")
	h.mustGo("wilant", today.ID)
	// A rest verdict would ease any generated hard session today.
	if err := h.training.SaveWellness(context.Background(), workout.DailyWellness{
		Rider: "wilant", Date: cpToday, HRVLastNight: 20, HRVWeeklyAvg: 60, HRVStatus: "LOW", SleepSeconds: 14000, SleepScore: 20, RestingHR: 70,
	}); err != nil {
		t.Fatal(err)
	}
	row, _ := h.fixedRow("wilant", today.ID)

	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(context.Background())
	h.srv.AdaptWorkouts(context.Background())
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replan: status = %d", resp.StatusCode)
	}

	after, ok := h.fixedRow("wilant", today.ID)
	if !ok {
		t.Fatal("automation deleted the crew ride row")
	}
	if after.UpdatedAt != row.UpdatedAt || after.Date != row.Date || after.Description != row.Description ||
		workout.PlannedSeconds(after.Steps) != workout.PlannedSeconds(row.Steps) {
		t.Error("automation rewrote the crew ride row")
	}
}

// A crew ride on a day a life event covers is never deleted automatically: the
// removal the preview offers is unticked by default and only an explicit
// include applies it.
func TestACrewRideOnABlackoutDayIsNeverDeletedWithoutTheRiderTickingIt(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	h.mustGo("wilant", jw.ride)
	row, _ := h.fixedRow("wilant", jw.ride)

	post := func(body string) lifeResult {
		resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/life-events", body)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			t.Fatalf("life event: status = %d", resp.StatusCode)
		}
		var out lifeResult
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	event := `"kind":"travel","option":"no_bike","startDate":"` + cpSaturday + `","endDate":"` + cpSaturday + `"`

	preview := post(`{` + event + `,"dryRun":true}`)
	offered := false
	var removalID string
	for _, c := range preview.Diff.Changes {
		if c.WorkoutID == row.ID {
			if c.Op != "remove" {
				t.Errorf("the crew ride is offered a %q, want only a removal", c.Op)
			}
			if c.Default {
				t.Error("the crew ride's removal is ticked by default")
			}
			offered, removalID = true, c.ID
		}
	}
	if !offered {
		t.Fatalf("changes = %+v, want the removal of the crew ride offered unticked", preview.Diff.Changes)
	}

	post(`{` + event + `}`)
	if _, ok := h.fixedRow("wilant", jw.ride); !ok {
		t.Fatal("a life event deleted the crew ride without the rider ticking it")
	}
	// The tick and a replan do not delete it either.
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(context.Background())
	h.as("wilant", "cyclists", http.MethodPost, "/api/training/replan", "")
	if _, ok := h.fixedRow("wilant", jw.ride); !ok {
		t.Fatal("automation deleted a crew ride on a blackout day")
	}
	_ = removalID
}

// The preview of another rider's join shows only their own sessions.
func TestAPeersJoinPreviewShowsNothingOfAnotherRider(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	h.seedPrivateData("wilant")
	h.mustGo("wilant", jw.ride)

	resp := h.as("sam", "cyclists", http.MethodPut, "/api/training/crew-rides/"+jw.ride+"/going", `{"going":true,"dryRun":true}`)
	body := bodyOf(t, resp)
	assertNothingPrivate(t, "dry-run going", body)
	for _, w := range h.stored("wilant") {
		if w.CrewRideID == "" && strings.Contains(body, w.ID) && !strings.Contains(w.ID, "crew-ride") {
			t.Errorf("sam's preview names wilant's workout %s", w.ID)
		}
	}
	assertOnlyKeys(t, "dry-run going", body, "going", "diff", "applied", "workout")
}
