package garmin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/fitworkout"
)

func exampleSteps() []fitworkout.Step {
	return []fitworkout.Step{
		{Name: "Warmup", Intensity: "warmup", Duration: fitworkout.DurationTime, Seconds: 600, Target: fitworkout.TargetOpen},
		{
			Name: "Intervals", Repeat: 3,
			Steps: []fitworkout.Step{
				{Name: "On", Intensity: "interval", Duration: fitworkout.DurationTime, Seconds: 180,
					Target: fitworkout.TargetPower, TargetLow: 280, TargetHigh: 300},
				{Name: "Off", Intensity: "recovery", Duration: fitworkout.DurationTime, Seconds: 120,
					Target: fitworkout.TargetPower, TargetLow: 100, TargetHigh: 150},
			},
		},
		{Name: "Cooldown", Intensity: "cooldown", Duration: fitworkout.DurationOpen, Target: fitworkout.TargetOpen},
	}
}

func TestBuildWorkoutShape(t *testing.T) {
	dto, err := buildWorkout("Threshold 6x3", "cycling", exampleSteps())
	if err != nil {
		t.Fatalf("buildWorkout: %v", err)
	}
	if dto.WorkoutName != "Threshold 6x3" {
		t.Errorf("name = %q", dto.WorkoutName)
	}
	if dto.SportType.ID != 2 || dto.SportType.Key != "cycling" {
		t.Errorf("sport type = %+v", dto.SportType)
	}
	if len(dto.WorkoutSegments) != 1 {
		t.Fatalf("segments = %d, want 1", len(dto.WorkoutSegments))
	}

	steps := dto.WorkoutSegments[0].WorkoutSteps
	if len(steps) != 3 {
		t.Fatalf("top-level steps = %d, want 3 (warmup, repeat block, cooldown)", len(steps))
	}

	warmup := steps[0]
	if warmup.Type != "ExecutableStepDTO" {
		t.Errorf("warmup type = %q", warmup.Type)
	}
	if warmup.StepType == nil || warmup.StepType.Key != "warmup" {
		t.Errorf("warmup step type = %+v", warmup.StepType)
	}
	if warmup.EndCondition == nil || warmup.EndCondition.Key != "time" || warmup.EndConditionValue != 600 {
		t.Errorf("warmup end condition = %+v value=%v", warmup.EndCondition, warmup.EndConditionValue)
	}
	if warmup.TargetType == nil || warmup.TargetType.Key != "no.target" {
		t.Errorf("warmup target type = %+v", warmup.TargetType)
	}
	if warmup.TargetValueOne != nil || warmup.TargetValueTwo != nil {
		t.Errorf("warmup targets should be null: one=%v two=%v", warmup.TargetValueOne, warmup.TargetValueTwo)
	}

	repeat := steps[1]
	if repeat.Type != "RepeatGroupDTO" {
		t.Fatalf("repeat type = %q", repeat.Type)
	}
	if repeat.NumberOfIterations != 3 {
		t.Errorf("iterations = %d, want 3", repeat.NumberOfIterations)
	}
	if repeat.EndCondition == nil || repeat.EndCondition.Key != "iterations" {
		t.Errorf("repeat end condition = %+v", repeat.EndCondition)
	}
	if repeat.SmartRepeat == nil || *repeat.SmartRepeat != false {
		t.Errorf("smartRepeat = %v, want false", repeat.SmartRepeat)
	}
	if len(repeat.WorkoutSteps) != 2 {
		t.Fatalf("repeat children = %d, want 2", len(repeat.WorkoutSteps))
	}

	on := repeat.WorkoutSteps[0]
	if on.TargetType == nil || on.TargetType.Key != "power.zone" {
		t.Errorf("on target type = %+v", on.TargetType)
	}
	if on.TargetValueOne == nil || *on.TargetValueOne != 280 {
		t.Errorf("on target low = %v, want 280 (plain watts)", on.TargetValueOne)
	}
	if on.TargetValueTwo == nil || *on.TargetValueTwo != 300 {
		t.Errorf("on target high = %v, want 300", on.TargetValueTwo)
	}

	cooldown := steps[2]
	if cooldown.EndCondition == nil || cooldown.EndCondition.Key != "lap.button" {
		t.Errorf("cooldown end condition = %+v, want lap.button (open)", cooldown.EndCondition)
	}

	// Step order is sequential across the whole flattened tree: warmup(1),
	// on(2), off(3), the repeat marker(4), cooldown(5) — matching the order
	// a rider built them in, repeat block's own order coming after its
	// children the same way internal/fitworkout's message_index scheme
	// does for FIT.
	wantOrder := []int{1, 4, 5}
	for i, want := range wantOrder {
		if steps[i].StepOrder != want {
			t.Errorf("top-level step %d: StepOrder = %d, want %d", i, steps[i].StepOrder, want)
		}
	}
	if repeat.WorkoutSteps[0].StepOrder != 2 || repeat.WorkoutSteps[1].StepOrder != 3 {
		t.Errorf("repeat children order = %d, %d, want 2, 3",
			repeat.WorkoutSteps[0].StepOrder, repeat.WorkoutSteps[1].StepOrder)
	}
}

func TestBuildWorkoutDefaultsTheName(t *testing.T) {
	dto, err := buildWorkout("", "running", exampleSteps())
	if err != nil {
		t.Fatal(err)
	}
	if dto.WorkoutName != "Workout" {
		t.Errorf("name = %q, want default", dto.WorkoutName)
	}
	if dto.SportType.Key != "running" {
		t.Errorf("sport = %q", dto.SportType.Key)
	}
}

func TestBuildWorkoutRejectsNoSteps(t *testing.T) {
	if _, err := buildWorkout("Empty", "cycling", nil); err == nil {
		t.Fatal("expected an error for no steps")
	}
}

func TestBuildWorkoutRejectsAnEmptyRepeatBlock(t *testing.T) {
	_, err := buildWorkout("Bad", "cycling", []fitworkout.Step{{Name: "Intervals", Repeat: 4}})
	if err == nil {
		t.Fatal("expected an error for an empty repeat block")
	}
}

func TestBuildWorkoutRejectsATargetWithNoRange(t *testing.T) {
	_, err := buildWorkout("Bad", "cycling",
		[]fitworkout.Step{{Name: "Bad", Duration: fitworkout.DurationOpen, Target: fitworkout.TargetPower}})
	if err == nil {
		t.Fatal("expected an error for a power target with no low/high")
	}
}

// ---------- HTTP-level: CreateWorkout/UpdateWorkout/DeleteWorkout ----------

type workoutRecord struct {
	method      string
	path        string
	auth        string
	accept      string
	contentType string
	body        []byte
}

func workoutFake(t *testing.T, status int, response string) (*Client, *workoutRecord) {
	t.Helper()
	rec := &workoutRecord{}

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-service/oauth/exchange/user/2.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"bearer-1","expires_in":3600}`)
	})
	mux.HandleFunc(workoutPath, func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.auth = r.Header.Get("Authorization")
		rec.accept = r.Header.Get("Accept")
		rec.contentType = r.Header.Get("Content-Type")
		rec.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		fmt.Fprint(w, response)
	})
	mux.HandleFunc(workoutPath+"/", func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.auth = r.Header.Get("Authorization")
		rec.accept = r.Header.Get("Accept")
		rec.contentType = r.Header.Get("Content-Type")
		rec.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		fmt.Fprint(w, response)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c := New()
	c.APIBase = server.URL
	c.SetConsumer(testKey, testSecret)
	c.Resume(Session{OAuth1Token: "tok-1", OAuth1Secret: "sec-1"})
	return c, rec
}

func TestCreateWorkoutSendsJSONAndReadsTheId(t *testing.T) {
	c, rec := workoutFake(t, http.StatusOK, `{"workoutId":987654}`)

	id, err := c.CreateWorkout(t.Context(), "Threshold 6x3", "cycling", exampleSteps())
	if err != nil {
		t.Fatalf("CreateWorkout: %v", err)
	}
	if id != "987654" {
		t.Errorf("id = %q, want 987654", id)
	}
	if rec.method != http.MethodPost {
		t.Errorf("method = %s, want POST", rec.method)
	}
	if rec.contentType != "application/json" {
		t.Errorf("content type = %q, want application/json", rec.contentType)
	}
	if rec.auth != "Bearer bearer-1" {
		t.Errorf("authorization = %q", rec.auth)
	}

	var sent workoutDTO
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("sent body is not valid JSON: %v (%s)", err, rec.body)
	}
	if sent.WorkoutName != "Threshold 6x3" {
		t.Errorf("sent workout name = %q", sent.WorkoutName)
	}
}

func TestCreateWorkoutSurfacesRejection(t *testing.T) {
	c, _ := workoutFake(t, http.StatusBadRequest, `{"errors":["sportType is required"]}`)
	if _, err := c.CreateWorkout(t.Context(), "Bad", "cycling", exampleSteps()); err == nil {
		t.Fatal("expected an error for a rejected workout")
	}
}

func TestUpdateWorkoutSendsTheIdInTheBody(t *testing.T) {
	c, rec := workoutFake(t, http.StatusOK, `{"workoutId":"42"}`)

	if err := c.UpdateWorkout(t.Context(), "42", "Renamed", "cycling", exampleSteps()); err != nil {
		t.Fatalf("UpdateWorkout: %v", err)
	}
	if rec.method != http.MethodPut {
		t.Errorf("method = %s, want PUT", rec.method)
	}
	if rec.path != workoutPath+"/42" {
		t.Errorf("path = %q, want %s/42", rec.path, workoutPath)
	}

	var sent workoutDTO
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("sent body is not valid JSON: %v", err)
	}
	if sent.WorkoutID != "42" {
		t.Errorf("sent workoutId = %q, want 42", sent.WorkoutID)
	}
}

func TestDeleteWorkout(t *testing.T) {
	c, rec := workoutFake(t, http.StatusNoContent, "")
	if err := c.DeleteWorkout(t.Context(), "42"); err != nil {
		t.Fatalf("DeleteWorkout: %v", err)
	}
	if rec.method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", rec.method)
	}
	if rec.path != workoutPath+"/42" {
		t.Errorf("path = %q", rec.path)
	}
}

func TestDeleteWorkoutRefusesAnEmptyId(t *testing.T) {
	c, _ := workoutFake(t, http.StatusOK, "{}")
	if err := c.DeleteWorkout(t.Context(), ""); err == nil {
		t.Fatal("expected an error for an empty id")
	}
}
