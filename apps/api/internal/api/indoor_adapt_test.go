package api_test

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/indoor"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

const adaptFTP = 250.0

func adaptProfile(smart bool) workout.RiderProfile {
	return workout.RiderProfile{Rider: "wilant", FTPWatts: adaptFTP, SmartTrainer: smart}
}

// markerCount is how many automatic adjustments a description records.
func markerCount(w workout.Workout) int {
	return strings.Count(w.Description, scheduler.AdjustedMarker)
}

// makeIndoor flags an existing workout indoor the way the convert endpoint
// does, keeping its steps as the outdoor original. The steps are already
// time-based, so this stands in for a conversion without depending on one.
func makeIndoor(t *testing.T, store *workout.DB, id string) workout.Workout {
	t.Helper()
	ctx := context.Background()
	w, err := store.GetWorkout(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	original := append([]workout.WorkoutStep{}, w.Steps...)
	got, err := store.UpdateWorkout(ctx, id, workout.UpdateWorkoutRequest{Indoor: &yes, OutdoorSteps: &original})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// assertTrainerShape checks the steps are what an indoor session is: every
// leaf time-based, and (with an FTP) power-targeted where it has a target.
func assertTrainerShape(t *testing.T, steps []workout.WorkoutStep) {
	t.Helper()
	for _, s := range steps {
		if s.Repeat >= 2 {
			assertTrainerShape(t, s.Steps)
			continue
		}
		if s.Duration != workout.DurationTime {
			t.Errorf("step %q is %s, want time-based", s.Name, s.Duration)
		}
		if s.Target != workout.TargetPower && s.Target != workout.TargetOpen {
			t.Errorf("step %q targets %s, want power", s.Name, s.Target)
		}
	}
}

// longHard is a 3 h plan-made hard session, long enough that its easy
// variant is still long enough to be shortened indoors.
func tomorrowLongHard(h *tomorrowHarness, date string) workout.Workout {
	h.t.Helper()
	w, err := h.store.CreateWorkout(context.Background(), workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: h.goalID, Sport: model.SportCycling, Name: "Threshold 3x12",
		Date: date, Description: scheduler.GeneratedDescription,
		Zone: workout.ZoneThreshold, Level: 5, Steps: longRideSteps(9600),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

func (h *tomorrowHarness) saveProfile(smart bool) {
	h.t.Helper()
	if _, err := h.store.SaveProfile(context.Background(), adaptProfile(smart)); err != nil {
		h.t.Fatal(err)
	}
}

func TestEasingAnIndoorSessionKeepsItIndoorAndItsOutdoorStepsAreTheEasedOutdoorForm(t *testing.T) {
	h := newTomorrowHarness(t)
	h.saveProfile(true)
	orig := tomorrowLongHard(h, tmTomorrow)
	makeIndoor(t, h.store, orig.ID)
	h.snapshot(40, 100)

	if status, out := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusOK {
		t.Fatalf("status = %d (%v)", status, out)
	}
	got, _ := h.store.GetWorkout(context.Background(), orig.ID)

	easy := scheduler.EasyVariant(orig, adaptProfile(true))
	want, err := indoor.Convert(workout.Workout{Sport: model.SportCycling, Name: easy.Name, Zone: easy.Zone, Steps: easy.Steps}, adaptProfile(true))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Indoor {
		t.Fatal("an eased indoor session came back outdoor")
	}
	if !reflect.DeepEqual(got.Steps, want.Steps) {
		t.Errorf("steps = %+v, want the eased ride converted indoors", got.Steps)
	}
	assertTrainerShape(t, got.Steps)
	if workout.PlannedSeconds(got.Steps) >= workout.PlannedSeconds(easy.Steps) {
		t.Errorf("indoor eased ride is %v s, want it shorter than the outdoor %v s", workout.PlannedSeconds(got.Steps), workout.PlannedSeconds(easy.Steps))
	}
	if got.OutdoorSteps == nil || !reflect.DeepEqual(*got.OutdoorSteps, easy.Steps) {
		t.Errorf("outdoor steps = %+v, want the eased outdoor replacement, not the pre-easing session", got.OutdoorSteps)
	}
	if markerCount(got) != 1 || scheduler.IsGenerated(got) {
		t.Errorf("description = %q, want exactly one adjustment marker", got.Description)
	}

	// Back to outdoor gives the eased outdoor session.
	resp := h.as("wilant", "cyclists", http.MethodDelete, "/api/training/workouts/"+orig.ID+"/indoor?today="+tmToday, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revert status = %d", resp.StatusCode)
	}
	back, _ := h.store.GetWorkout(context.Background(), orig.ID)
	if back.Indoor || !reflect.DeepEqual(back.Steps, easy.Steps) || back.Name != easy.Name {
		t.Errorf("after revert: indoor=%v name=%q, want the eased outdoor ride", back.Indoor, back.Name)
	}
}

func TestEasingAnOutdoorSessionIsUnchangedByTheIndoorWork(t *testing.T) {
	h := newTomorrowHarness(t)
	h.saveProfile(true)
	orig := tomorrowLongHard(h, tmTomorrow)
	h.snapshot(40, 100)

	if status, _ := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	got, _ := h.store.GetWorkout(context.Background(), orig.ID)
	easy := scheduler.EasyVariant(orig, adaptProfile(true))
	if got.Indoor || got.OutdoorSteps != nil || !reflect.DeepEqual(got.Steps, easy.Steps) {
		t.Errorf("an outdoor session was changed by the indoor path: indoor=%v outdoor=%v", got.Indoor, got.OutdoorSteps)
	}
}

func TestSteppingDownAnIndoorSessionKeepsItIndoor(t *testing.T) {
	h := newTomorrowHarness(t)
	h.saveProfile(true)
	h.hard(tmToday)
	h.hard("2026-03-18")
	h.rode("2026-03-18")
	orig := tomorrowLongHard(h, tmTomorrow)
	makeIndoor(t, h.store, orig.ID)

	if status, out := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusOK {
		t.Fatalf("status = %d (%v)", status, out)
	}
	got, _ := h.store.GetWorkout(context.Background(), orig.ID)

	ladder, ok := workoutlib.LadderFor(model.SportCycling, string(workout.ZoneThreshold))
	if !ok {
		t.Fatal("no threshold ladder")
	}
	rung := ladder.Rungs[3] // level 4, one below the workout's 5
	lower := workoutlib.Instantiate(ladder, rung, adaptProfile(true))
	if got.Level != 4 || !got.Indoor {
		t.Fatalf("level=%v indoor=%v, want the lower rung, still indoor", got.Level, got.Indoor)
	}
	if got.OutdoorSteps == nil || !reflect.DeepEqual(*got.OutdoorSteps, lower.Steps) {
		t.Errorf("outdoor steps = %+v, want the lower rung's outdoor steps", got.OutdoorSteps)
	}
	assertTrainerShape(t, got.Steps)
	// A smart-trainer rider's ranges collapse to one ERG number.
	var collapsed func([]workout.WorkoutStep) bool
	collapsed = func(steps []workout.WorkoutStep) bool {
		for _, s := range steps {
			if s.Repeat >= 2 {
				if !collapsed(s.Steps) {
					return false
				}
			} else if s.Target == workout.TargetPower && s.TargetLow != s.TargetHigh {
				return false
			}
		}
		return true
	}
	if !collapsed(got.Steps) {
		t.Errorf("steps = %+v, want power ranges collapsed to a midpoint", got.Steps)
	}
	if markerCount(got) != 1 {
		t.Errorf("description = %q, want one marker", got.Description)
	}
}

func TestEaseThenConvertAndConvertThenEaseBothEndIndoorWithOneMarker(t *testing.T) {
	h := newTomorrowHarness(t)
	h.saveProfile(false)
	orig := tomorrowLongHard(h, tmTomorrow)
	h.snapshot(40, 100)

	if status, _ := h.ease("wilant", "cyclists", "?today="+tmToday); status != http.StatusOK {
		t.Fatalf("ease status = %d", status)
	}
	eased, _ := h.store.GetWorkout(context.Background(), orig.ID)
	resp := h.as("wilant", "cyclists", http.MethodPost, "/api/training/workouts/"+orig.ID+"/indoor?today="+tmToday, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("convert after easing = %d", resp.StatusCode)
	}
	got, _ := h.store.GetWorkout(context.Background(), orig.ID)
	if !got.Indoor || markerCount(got) != 1 {
		t.Errorf("indoor=%v markers=%d, want indoor with one marker", got.Indoor, markerCount(got))
	}
	if got.OutdoorSteps == nil || !reflect.DeepEqual(*got.OutdoorSteps, eased.Steps) {
		t.Errorf("converting after easing must start from the eased steps")
	}
}

// The fatigue swap through adaptRider (today's readiness), not the tomorrow click.
func TestAFatigueSwapKeepsAnIndoorSessionIndoor(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	goal, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, adaptProfile(true)); err != nil {
		t.Fatal(err)
	}
	for d := 1; d <= 6; d++ {
		if _, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
			Rider: "wilant", Provider: "garmin", ExternalID: fmt.Sprintf("s%d", d), Sport: "cycling",
			Date: time.Now().AddDate(0, 0, -d).Format("2006-01-02"), DurationSeconds: 5400, TrainingLoad: 300,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.RecomputeFitnessSnapshots(ctx, "wilant"); err != nil {
		t.Fatal(err)
	}
	req := generated("wilant", goal.ID, "VO2max intervals", time.Now().Format("2006-01-02"), 3600)
	req.Steps = longRideSteps(9600)
	hard, err := h.store.CreateWorkout(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	makeIndoor(t, h.store, hard.ID)

	h.srv.AdaptWorkouts(ctx)

	got, _ := h.store.GetWorkout(ctx, hard.ID)
	if got.Name != "Endurance ride" || !got.Indoor {
		t.Fatalf("name=%q indoor=%v, want the easy ride, still indoor", got.Name, got.Indoor)
	}
	easy := scheduler.EasyVariant(hard, adaptProfile(true))
	if got.OutdoorSteps == nil || !reflect.DeepEqual(*got.OutdoorSteps, easy.Steps) {
		t.Errorf("outdoor steps = %+v, want the eased outdoor replacement", got.OutdoorSteps)
	}
	assertTrainerShape(t, got.Steps)
	if markerCount(got) != 1 {
		t.Errorf("markers = %d, want 1", markerCount(got))
	}

	// A second pass changes nothing: one automatic change per workout.
	before := got
	h.srv.AdaptWorkouts(ctx)
	if after, _ := h.store.GetWorkout(ctx, hard.ID); !reflect.DeepEqual(before, after) {
		t.Error("a second adaptation pass changed the workout again")
	}
}

func TestAStruggledSessionStepDownKeepsTheNextIndoorSessionIndoor(t *testing.T) {
	h := newAutoScheduleHarness(t)
	ctx := context.Background()
	h.srv.Clock = func() time.Time { return time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC) }
	goal, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Stay Fit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SaveProfile(ctx, adaptProfile(false)); err != nil {
		t.Fatal(err)
	}
	struggled, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal.ID, Sport: model.SportCycling, Name: "Threshold 3x12", Date: "2026-03-17",
		Description: scheduler.GeneratedDescription, Zone: workout.ZoneThreshold, Level: 5,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3600}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: "6400", Sport: "cycling", Date: "2026-03-17", DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{SessionID: sess.ID, Rider: "wilant", WorkoutID: struggled.ID, Outcome: "struggled"}); err != nil {
		t.Fatal(err)
	}
	next, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", GoalID: goal.ID, Sport: model.SportCycling, Name: "Threshold 3x8", Date: "2026-03-21",
		Description: scheduler.GeneratedDescription, Zone: workout.ZoneThreshold, Level: 4,
		Steps: []workout.WorkoutStep{{Name: "Main", Duration: workout.DurationTime, Seconds: 3000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	makeIndoor(t, h.store, next.ID)

	h.srv.AdaptWorkouts(ctx)

	got, _ := h.store.GetWorkout(ctx, next.ID)
	ladder, _ := workoutlib.LadderFor(model.SportCycling, string(workout.ZoneThreshold))
	lower := workoutlib.Instantiate(ladder, ladder.Rungs[2], adaptProfile(false)) // level 3
	if got.Level != 3 || !got.Indoor {
		t.Fatalf("level=%v indoor=%v, want stepped down and still indoor", got.Level, got.Indoor)
	}
	if got.OutdoorSteps == nil || !reflect.DeepEqual(*got.OutdoorSteps, lower.Steps) {
		t.Errorf("outdoor steps = %+v, want the lower rung's outdoor steps", got.OutdoorSteps)
	}
	assertTrainerShape(t, got.Steps)
	if markerCount(got) != 1 {
		t.Errorf("markers = %d, want 1", markerCount(got))
	}
}

func TestEasingTheDayBeforeAnFTPTestKeepsAnIndoorSessionIndoor(t *testing.T) {
	h := newReplanHarness(t)
	ctx := context.Background()
	goal := h.scheduleSetup(adaptFTP)
	if _, err := h.training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", FTPWatts: adaptFTP, SmartTrainer: true, HoursPerAvailableDay: 1.5, AvailableDays: []string{"wed", "fri", "sat"},
	}); err != nil {
		t.Fatal(err)
	}
	req := hardGenerated("wilant", goal.ID, "Threshold intervals", replanThursday)
	req.Steps = longRideSteps(9600)
	hard, err := h.training.CreateWorkout(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	makeIndoor(t, h.training, hard.ID)

	if resp, _ := h.scheduleFTPTest("wilant", `{"protocol":"twenty_minute","date":"`+replanFriday+`"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	got := h.stored(hard.ID)
	if !got.Indoor || got.Zone != workout.ZoneEndurance {
		t.Fatalf("indoor=%v zone=%q, want the easy ride, still indoor", got.Indoor, got.Zone)
	}
	easy := scheduler.EasyVariant(hard, adaptProfile(true))
	if got.OutdoorSteps == nil || !reflect.DeepEqual(*got.OutdoorSteps, easy.Steps) {
		t.Errorf("outdoor steps = %+v, want the eased outdoor replacement", got.OutdoorSteps)
	}
	assertTrainerShape(t, got.Steps)
	if markerCount(got) != 1 {
		t.Errorf("markers = %d, want 1", markerCount(got))
	}
}

// A replan deletes and rebuilds plan-made sessions this week, which would turn
// a rider's explicit indoor choice back into a road session.
func TestReplanKeepsAnIndoorConvertedSession(t *testing.T) {
	h := newReplanHarness(t)
	goal := h.scheduleSetup(adaptFTP)
	kept := h.indoorRide(goal.ID, replanFriday, 7200)
	if resp, _ := h.convert("wilant", kept.ID, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("convert status = %d", resp.StatusCode)
	}

	if resp, _ := h.replan("wilant"); resp.StatusCode != http.StatusOK {
		t.Fatalf("replan status = %d", resp.StatusCode)
	}

	got, err := h.training.GetWorkout(context.Background(), kept.ID)
	if err != nil || !got.Indoor {
		t.Errorf("the indoor session did not survive a replan: %+v err %v", got, err)
	}
}
