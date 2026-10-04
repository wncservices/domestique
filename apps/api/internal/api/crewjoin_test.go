package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// genZone is a plan-made session in a zone, as the scheduler would make it.
func genZone(rider, goal, name, date string, zone workout.Zone, seconds float64) workout.CreateWorkoutRequest {
	req := generated(rider, goal, name, date, seconds)
	req.Zone = zone
	return req
}

// joinWeek is wilant's week around the Saturday ride: a hard session on Friday,
// a plan-made endurance ride on the ride's own day, a hard one on Sunday and a
// short endurance ride on Thursday.
type joinWeek struct {
	cpSetup
	goal               workout.Goal
	fri, sat, sun, thu workout.Workout
}

func (h *crewPlanHarness) joinWeek() joinWeek {
	h.t.Helper()
	jw := joinWeek{cpSetup: h.setup()}
	jw.goal = h.planFor("wilant")
	ctx := context.Background()
	mk := func(req workout.CreateWorkoutRequest) workout.Workout {
		w, err := h.training.CreateWorkout(ctx, req)
		if err != nil {
			h.t.Fatal(err)
		}
		return w
	}
	jw.thu = mk(genZone("wilant", jw.goal.ID, "Endurance ride", cpThursday, workout.ZoneEndurance, 3600))
	jw.fri = mk(genZone("wilant", jw.goal.ID, "Threshold", cpFriday, workout.ZoneThreshold, 5400))
	jw.sat = mk(genZone("wilant", jw.goal.ID, "Endurance ride", cpSaturday, workout.ZoneEndurance, 5400))
	jw.sun = mk(genZone("wilant", jw.goal.ID, "Sweet spot", cpSunday, workout.ZoneSweetSpot, 7200))
	return jw
}

func (h *crewPlanHarness) get(rider, id string) (workout.Workout, bool) {
	h.t.Helper()
	for _, w := range h.stored(rider) {
		if w.ID == id {
			return w, true
		}
	}
	return workout.Workout{}, false
}

func changeIDs(out goingOut) map[string]bool {
	ids := map[string]bool{}
	for _, c := range out.Diff.Changes {
		ids[c.ID] = true
	}
	return ids
}

func TestJoiningPreviewListsTheChangesAndWritesNothing(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	before := h.stored("wilant")

	resp, out := h.setGoing("wilant", jw.ride, `{"going":true,"dryRun":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	ids := changeIDs(out)
	for _, want := range []string{"remove:" + jw.sat.ID, "ease:" + jw.fri.ID, "ease:" + jw.sun.ID} {
		if !ids[want] {
			t.Errorf("changes = %+v, want %s", out.Diff.Changes, want)
		}
	}
	for _, c := range out.Diff.Changes {
		if c.Reason == "" || c.Date == "" || c.Name == "" {
			t.Errorf("change %+v lacks a reason, date or name", c)
		}
	}
	after := h.stored("wilant")
	if len(after) != len(before) {
		t.Fatalf("a dry run changed the number of workouts: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].UpdatedAt != after[i].UpdatedAt || before[i].Description != after[i].Description {
			t.Errorf("a dry run wrote %s", before[i].ID)
		}
	}
	if _, ok := h.fixedRow("wilant", jw.ride); ok {
		t.Error("a dry run made the fixed session")
	}
}

func TestJoiningAppliesTheRecomputedDiffAndRecordsWhy(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	h.push(jw.sat.ID) // its copy is on the rider's Garmin account
	h.garmin.workoutCalls = nil

	out := h.mustGo("wilant", jw.ride)
	if out.Applied == nil || out.Applied.Removed != 1 || out.Applied.Eased != 2 {
		t.Fatalf("applied = %+v, want 1 removed and 2 eased", out.Applied)
	}
	if _, ok := h.get("wilant", jw.sat.ID); ok {
		t.Error("the plan-made session on the ride's day was not removed")
	}
	if !strings.Contains(h.calls(), "delete garmin-workout-1") {
		t.Errorf("calls = %q, want the removed session's Garmin copy taken off", h.calls())
	}
	fri, _ := h.get("wilant", jw.fri.ID)
	if fri.Zone != workout.ZoneEndurance || !strings.Contains(fri.Description, scheduler.AdjustedMarker) ||
		!strings.Contains(fri.Description, "the day before your crew ride") {
		t.Errorf("Friday = %s %q, want an easy session noting why", fri.Zone, fri.Description)
	}
	sun, _ := h.get("wilant", jw.sun.ID)
	if got := workout.PlannedSeconds(sun.Steps); got > 3600 {
		t.Errorf("the day after a long ride = %v s, want at most an hour", got)
	}
	if _, ok := h.fixedRow("wilant", jw.ride); !ok {
		t.Error("no fixed session")
	}
	if _, ok := h.get("wilant", jw.thu.ID); !ok {
		t.Error("an unrelated session was removed")
	}

	// The reasons are recorded under their own rules, with the ride as a fact.
	rules := map[string]string{}
	for _, w := range h.workoutsOf("wilant") {
		if w.Why != nil {
			rules[w.ID] = w.Why.Rule
			if len(w.Why.Facts) == 0 || w.Why.Facts[0].Label != "Crew ride" {
				t.Errorf("why facts for %s = %+v, want a Crew ride fact", w.ID, w.Why.Facts)
			}
		}
	}
	if rules[jw.fri.ID] != "crew_ride_eve" || rules[jw.sun.ID] != "crew_ride_after" {
		t.Errorf("why rules = %v, want crew_ride_eve and crew_ride_after", rules)
	}
}

func TestSkippingACrewRideChangeLeavesThatSessionAlone(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()

	resp, out := h.setGoing("wilant", jw.ride,
		`{"going":true,"skip":["remove:`+jw.sat.ID+`","ease:`+jw.fri.ID+`","remove:no-such-session"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (an unknown skip id must be ignored)", resp.StatusCode)
	}
	if out.Applied.Removed != 0 || out.Applied.Eased != 1 {
		t.Errorf("applied = %+v, want nothing removed and only Sunday eased", out.Applied)
	}
	if _, ok := h.get("wilant", jw.sat.ID); !ok {
		t.Error("a skipped removal was applied")
	}
	fri, _ := h.get("wilant", jw.fri.ID)
	if fri.Zone != workout.ZoneThreshold {
		t.Errorf("a skipped ease was applied: Friday is %s", fri.Zone)
	}

	// The easing rule runs after every plan change; a skip has to stick.
	h.srv.AdaptWorkouts(context.Background())
	again, _ := h.get("wilant", jw.fri.ID)
	if again.Zone != workout.ZoneThreshold || strings.Contains(again.Description, scheduler.AdjustedMarker) {
		t.Errorf("the next adaptation pass eased what the rider kept: %q", again.Description)
	}
}

func TestApplyRecomputesSoAStalePreviewIsHarmless(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	_, preview := h.setGoing("wilant", jw.ride, `{"going":true,"dryRun":true}`)
	if !changeIDs(preview)["ease:"+jw.sun.ID] {
		t.Fatal("setup: the preview does not ease Sunday")
	}

	// The plan changes between the preview and the confirmation.
	if err := h.training.DeleteWorkout(context.Background(), jw.sun.ID); err != nil {
		t.Fatal(err)
	}
	resp, out := h.setGoing("wilant", jw.ride, `{"going":true,"skip":["ease:`+jw.sun.ID+`"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if changeIDs(out)["ease:"+jw.sun.ID] {
		t.Error("the apply used a diff that named a session that no longer exists")
	}
	if _, ok := h.fixedRow("wilant", jw.ride); !ok {
		t.Error("no fixed session after a stale preview")
	}
}

func TestOnlyWhatThePlanMadeIsChangedAndTheRestIsListed(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	built, err := h.training.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "My own ride", Date: cpSaturday, Description: "mine", Steps: step(3600),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, out := h.setGoing("wilant", jw.ride, `{"going":true,"dryRun":true}`)
	listed := false
	for _, n := range out.Diff.LeftAlone {
		if n.WorkoutID == built.ID {
			listed = true
		}
	}
	if !listed {
		t.Errorf("leftAlone = %+v, want the session the rider built", out.Diff.LeftAlone)
	}
	h.mustGo("wilant", jw.ride)
	if _, ok := h.get("wilant", built.ID); !ok {
		t.Error("a rider-built session was removed")
	}
}

func step(seconds float64) []workout.WorkoutStep {
	return []workout.WorkoutStep{{Name: "Main", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: seconds, Target: workout.TargetOpen}}
}

func TestJoiningAnAwayDayWarnsAndStillJoins(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	if _, err := h.training.CreateLifeEvent(context.Background(), workout.LifeEvent{Rider: "wilant", Kind: "busy", Start: cpSaturday, End: cpSaturday}); err != nil {
		t.Fatal(err)
	}
	_, out := h.setGoing("wilant", jw.ride, `{"going":true,"dryRun":true}`)
	warned := false
	for _, w := range out.Diff.Warnings {
		if strings.Contains(strings.ToLower(w), "away") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want one about being away", out.Diff.Warnings)
	}
	h.mustGo("wilant", jw.ride)
	if _, ok := h.fixedRow("wilant", jw.ride); !ok {
		t.Error("the warning blocked the join")
	}
}

func TestLeavingPutsTheDaysPlanBackAndDoesNotRestoreWhatWasEased(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	h.mustGo("wilant", jw.ride)

	_, preview := h.setGoing("wilant", jw.ride, `{"going":false,"dryRun":true}`)
	hasAdd, notRestored := false, false
	for _, c := range preview.Diff.Changes {
		if c.Op == "add" && c.Date == cpSaturday {
			hasAdd = true
		}
	}
	for _, w := range preview.Diff.Warnings {
		if strings.Contains(w, "not restored") {
			notRestored = true
		}
	}
	if !hasAdd || !notRestored {
		t.Fatalf("preview = %+v, want an add on Saturday and the not-restored warning", preview.Diff)
	}
	if _, ok := h.fixedRow("wilant", jw.ride); !ok {
		t.Fatal("a dry run of leaving deleted the fixed session")
	}

	out := h.mustLeave("wilant", jw.ride)
	if out.Going || out.Applied == nil || out.Applied.Added != 1 {
		t.Fatalf("leave = %+v", out)
	}
	if _, ok := h.fixedRow("wilant", jw.ride); ok {
		t.Error("the fixed session survived leaving")
	}
	if is, _ := h.sched.IsGoing(context.Background(), jw.ride, "wilant"); is {
		t.Error("the going row survived leaving")
	}
	if got := h.generatedIn("wilant", cpSaturday, cpSaturday); len(got) != 1 {
		t.Errorf("Saturday holds %d plan sessions, want the plan's own back", len(got))
	}
	fri, _ := h.get("wilant", jw.fri.ID)
	if fri.Zone != workout.ZoneEndurance {
		t.Error("leaving restored a session that was eased")
	}
}

func TestAnOrphanedSessionCanBeUpdatedByLeaving(t *testing.T) {
	t.Run("the crew cancelled the ride", func(t *testing.T) {
		h := newCrewPlanHarness(t)
		jw := h.joinWeek()
		h.mustGo("wilant", jw.ride)
		if err := h.sched.Delete(context.Background(), jw.ride); err != nil {
			t.Fatal(err)
		}
		_, preview := h.setGoing("wilant", jw.ride, `{"going":false,"dryRun":true}`)
		if len(preview.Diff.Warnings) == 0 {
			t.Error("the leave preview of a cancelled ride has no warnings")
		}
		h.mustLeave("wilant", jw.ride)
		if _, ok := h.fixedRow("wilant", jw.ride); ok {
			t.Error("the orphaned session survived leaving")
		}
	})
	t.Run("the rider was removed from the crew", func(t *testing.T) {
		h := newCrewPlanHarness(t)
		jw := h.joinWeek()
		h.mustGo("sam", jw.ride)
		if err := h.crews.Remove(context.Background(), jw.crewID, "sam"); err != nil {
			t.Fatal(err)
		}
		if err := h.sched.RemoveRider(context.Background(), jw.crewID, "sam"); err != nil {
			t.Fatal(err)
		}
		h.mustLeave("sam", jw.ride)
		if _, ok := h.fixedRow("sam", jw.ride); ok {
			t.Error("the orphaned session survived leaving")
		}
	})
	t.Run("a stranger with no session is told there is no such ride", func(t *testing.T) {
		h := newCrewPlanHarness(t)
		jw := h.joinWeek()
		if resp, _ := h.setGoing("outsider", jw.ride, `{"going":false}`); resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})
}

// A failure part-way puts everything back: the second write fails and the
// first is undone.
func TestAFailedJoinLeavesTheRiderAsTheyWere(t *testing.T) {
	for _, failAt := range []int{1, 2, 3, 4, 5, 6} {
		h := newCrewPlanHarness(t)
		jw := h.joinWeek()
		before := h.stored("wilant")
		calls := 0
		h.srv.AfterCrewRideWrite = func() error {
			calls++
			if calls == failAt {
				return errors.New("boom")
			}
			return nil
		}

		resp, _ := h.setGoing("wilant", jw.ride, `{"going":true}`)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("failAt %d: status = 200, want the failure reported", failAt)
		}
		if _, ok := h.fixedRow("wilant", jw.ride); ok {
			t.Errorf("failAt %d: the fixed session was left behind", failAt)
		}
		if is, _ := h.sched.IsGoing(context.Background(), jw.ride, "wilant"); is {
			t.Errorf("failAt %d: the going row was left behind", failAt)
		}
		after := h.stored("wilant")
		if len(after) != len(before) {
			t.Errorf("failAt %d: %d workouts, had %d", failAt, len(after), len(before))
			continue
		}
		byKey := map[string]workout.Workout{}
		for _, w := range after {
			byKey[w.Date+"|"+w.Name] = w
		}
		for _, b := range before {
			a, ok := byKey[b.Date+"|"+b.Name]
			if !ok {
				t.Errorf("failAt %d: %s on %s is gone", failAt, b.Name, b.Date)
				continue
			}
			if a.Description != b.Description || a.Zone != b.Zone || workout.PlannedSeconds(a.Steps) != workout.PlannedSeconds(b.Steps) {
				t.Errorf("failAt %d: %s on %s was not restored (%q, %s)", failAt, b.Name, b.Date, a.Description, a.Zone)
			}
		}
	}
}

func TestAFailedLeaveKeepsTheFixedSession(t *testing.T) {
	h := newCrewPlanHarness(t)
	jw := h.joinWeek()
	h.mustGo("wilant", jw.ride)
	calls := 0
	h.srv.AfterCrewRideWrite = func() error {
		calls++
		if calls == 1 {
			return errors.New("boom")
		}
		return nil
	}
	resp, _ := h.setGoing("wilant", jw.ride, `{"going":false}`)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("status = 200, want the failure reported")
	}
	if _, ok := h.fixedRow("wilant", jw.ride); !ok {
		t.Error("a failed leave lost the fixed session")
	}
	if got := h.generatedIn("wilant", cpSaturday, cpSaturday); len(got) != 0 {
		t.Errorf("a failed leave left %d added sessions on the ride's day", len(got))
	}
}
