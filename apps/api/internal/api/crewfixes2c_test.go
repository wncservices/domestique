package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A route with no distance has nothing to estimate: the estimate would be zero
// hours and the "ride" a zero-length session. Joining it is refused.
func TestAZeroDistanceRouteCannotBeJoined(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	if _, err := h.db.Conn().Exec(`UPDATE routes SET distance_m = 0 WHERE slug = ?`, s.route); err != nil {
		if _, err := h.db.Conn().Exec(`UPDATE routes SET distance_m = 0`); err != nil {
			t.Fatal(err)
		}
	}
	for _, dry := range []string{"true", "false"} {
		resp, _ := h.setGoing("wilant", s.ride, `{"going":true,"dryRun":`+dry+`}`)
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("dryRun %s: status = %d, want 409", dry, resp.StatusCode)
		}
	}
	if _, ok := h.fixedRow("wilant", s.ride); ok {
		t.Error("a zero-length crew ride was planned")
	}
	resp := h.as("wilant", "cyclists", http.MethodPut, "/api/training/crew-rides/"+s.ride+"/going", `{"going":true}`)
	if body := bodyOf(t, resp); !strings.Contains(body, "this route has no distance yet") {
		t.Errorf("body = %s, want the no-distance message", body)
	}
}

// Rolling back puts a session back as the rider had it: an indoor session stays
// indoor and keeps the outdoor steps it can be reverted to.
func TestARolledBackIndoorSessionStaysIndoor(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	outdoor := []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3 * 3600, Target: workout.TargetOpen}}
	indoorSteps := []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 7200, Target: workout.TargetOpen}}
	yes := true
	if _, err := h.training.UpdateWorkout(context.Background(), jw.sat.ID, workout.UpdateWorkoutRequest{Indoor: &yes, OutdoorSteps: &outdoor, Steps: &indoorSteps}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.srv.AfterCrewRideWrite = func() error {
		calls++
		if calls == 6 { // after the removals, which come last
			return errors.New("boom")
		}
		return nil
	}
	if resp, _ := h.setGoing("wilant", jw.ride, `{"going":true}`); resp.StatusCode == http.StatusOK {
		t.Fatal("setup: the failure was not reached")
	}
	var back workout.Workout
	for _, w := range h.stored("wilant") {
		if w.Date == cpSaturday && w.CrewRideID == "" {
			back = w
		}
	}
	if back.ID == "" {
		t.Fatal("the removed session was not put back")
	}
	if !back.Indoor || back.OutdoorSteps == nil || workout.PlannedSeconds(*back.OutdoorSteps) != 3*3600 {
		t.Errorf("restored session: indoor %v, outdoor steps %v, want indoor with its outdoor original", back.Indoor, back.OutdoorSteps)
	}
}

// Joining the day before an FTP test warns: a long ride the day before reads low.
func TestALongCrewRideTheDayBeforeAnFTPTestWarns(t *testing.T) {
	h := newCrewPlanHarness(t)
	s := h.setup()
	goal := h.planFor("wilant")
	test, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Name: "FTP Test (ramp)", GoalID: goal.ID, Date: cpSunday, TestProtocol: "ramp",
		Description: "A test.", Steps: step(2400),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = scheduler.GeneratedDescription
	_, out := h.setGoing("wilant", s.ride, `{"going":true,"dryRun":true}`)
	warned := false
	for _, w := range out.Diff.Warnings {
		if strings.Contains(w, "FTP test") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want one about the FTP test the next day (%s)", out.Diff.Warnings, test.ID)
	}
	// An endurance-length ride is not long enough to matter.
	short := h.seedRoute("Short", "wilant", 30, 0, s.crewID)
	r2 := h.seedRide(s.crewID, short.Slug, cpSaturday, "wilant")
	_, out = h.setGoing("wilant", r2.ID, `{"going":true,"dryRun":true}`)
	for _, w := range out.Diff.Warnings {
		if strings.Contains(w, "FTP test") {
			t.Errorf("a short ride warned about the test: %q", w)
		}
	}
}
