package fitnesstest

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestEstimateFTPWithNoSessionsIsNotOK(t *testing.T) {
	if _, ok := EstimateFTP(nil); ok {
		t.Error("expected ok=false with no sessions")
	}
}

func TestEstimateFTPIgnoresNonCyclingAndPowerlessSessions(t *testing.T) {
	sessions := []workout.CompletedSession{
		{Sport: "running", DurationSeconds: 20 * 60, AvgPowerWatts: 300},
		{Sport: "cycling", DurationSeconds: 20 * 60, AvgPowerWatts: 0},
	}
	if _, ok := EstimateFTP(sessions); ok {
		t.Error("expected ok=false: no qualifying cycling-with-power session")
	}
}

func TestEstimateFTPIgnoresSessionsOfTheWrongDuration(t *testing.T) {
	sessions := []workout.CompletedSession{
		{Sport: "cycling", DurationSeconds: 5 * 60, AvgPowerWatts: 400},   // a sprint, not a threshold effort
		{Sport: "cycling", DurationSeconds: 4 * 3600, AvgPowerWatts: 150}, // an all-day endurance ride
	}
	if _, ok := EstimateFTP(sessions); ok {
		t.Error("expected ok=false: neither session is a usable FTP proxy duration")
	}
}

func TestEstimateFTPFromA20MinuteEffort(t *testing.T) {
	sessions := []workout.CompletedSession{
		{Sport: "cycling", DurationSeconds: 20 * 60, AvgPowerWatts: 280},
	}
	watts, ok := EstimateFTP(sessions)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if want := 280 * 0.95; watts != want {
		t.Errorf("watts = %v, want %v (280 * 0.95)", watts, want)
	}
}

func TestEstimateFTPFromA60MinuteEffortUsesTheFullAverage(t *testing.T) {
	sessions := []workout.CompletedSession{
		{Sport: "cycling", DurationSeconds: 60 * 60, AvgPowerWatts: 250},
	}
	watts, ok := EstimateFTP(sessions)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if watts != 250 {
		t.Errorf("watts = %v, want 250 (60-minute effort used directly)", watts)
	}
}

// The rider's best qualifying effort wins, not the most recent or the
// average of several — a single true best-effort is what an FTP estimate
// should reflect, the same reasoning a real FTP test only needs one clean
// attempt.
func TestEstimateFTPTakesTheBestQualifyingEffort(t *testing.T) {
	sessions := []workout.CompletedSession{
		{Sport: "cycling", DurationSeconds: 20 * 60, AvgPowerWatts: 200}, // -> 190
		{Sport: "cycling", DurationSeconds: 60 * 60, AvgPowerWatts: 230}, // -> 230, wins
		{Sport: "cycling", DurationSeconds: 20 * 60, AvgPowerWatts: 210}, // -> 199.5
	}
	watts, ok := EstimateFTP(sessions)
	if !ok || watts != 230 {
		t.Errorf("watts = %v, ok = %v, want 230, true", watts, ok)
	}
}

func TestFTPTestWorkoutIsCyclingAndEveryStepIsOpenTargeted(t *testing.T) {
	req := FTPTestWorkout()
	if req.Sport != model.SportCycling {
		t.Errorf("sport = %q, want cycling", req.Sport)
	}
	if len(req.Steps) == 0 {
		t.Fatal("expected steps")
	}
	for _, step := range flatten(req.Steps) {
		if step.Target != workout.TargetOpen {
			t.Errorf("%s: target = %q, want open — FTP is exactly what this test doesn't know yet", step.Name, step.Target)
		}
	}
	// The 20-minute block itself must actually be 20 minutes — that's the
	// number the description tells the rider to read their average power
	// back from.
	var found bool
	for _, step := range req.Steps {
		if step.Seconds == 20*60 {
			found = true
		}
	}
	if !found {
		t.Error("expected a 20-minute (1200s) step")
	}
}

func TestMaxHRTestWorkoutMentionsTheSportAndCarriesASafetyNote(t *testing.T) {
	cycling := MaxHRTestWorkout(model.SportCycling)
	if cycling.Sport != model.SportCycling {
		t.Errorf("sport = %q, want cycling", cycling.Sport)
	}
	if !strings.Contains(cycling.Description, "ride") {
		t.Error("cycling test description should say 'ride'")
	}
	if !strings.Contains(cycling.Description, "doctor") {
		t.Error("expected a safety note mentioning a doctor")
	}

	running := MaxHRTestWorkout(model.SportRunning)
	if !strings.Contains(running.Description, "run") {
		t.Error("running test description should say 'run'")
	}

	for _, step := range flatten(running.Steps) {
		if step.Target != workout.TargetOpen {
			t.Errorf("%s: target = %q, want open — max HR is exactly what this test doesn't know yet", step.Name, step.Target)
		}
	}
	// The final sprint must immediately follow the third effort with no
	// recovery step between them — that adjacency is the whole point of
	// the protocol.
	steps := running.Steps
	for i, step := range steps {
		if step.Name == "Effort 3 (near max)" {
			if i+1 >= len(steps) || steps[i+1].Name != "All-out sprint (no recovery before this one)" {
				t.Error("the all-out sprint must come immediately after Effort 3, with nothing between them")
			}
		}
	}
}

func flatten(steps []workout.WorkoutStep) []workout.WorkoutStep {
	var out []workout.WorkoutStep
	for _, s := range steps {
		if s.Repeat > 1 {
			out = append(out, flatten(s.Steps)...)
			continue
		}
		out = append(out, s)
	}
	return out
}
