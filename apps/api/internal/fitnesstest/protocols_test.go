package fitnesstest

import (
	"math"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestFTPFromTestFormulas(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		curve    map[int]float64
		want     float64
	}{
		{"ramp is 75% of the best minute", ProtocolRamp, map[int]float64{60: 416, 1200: 999}, 312},
		{"twenty minute is 95% of the best 20 minutes", ProtocolTwentyMinute, map[int]float64{60: 999, 1200: 300}, 285},
		{"2x8 is 90% of the 8-minute power", ProtocolTwoByEight, map[int]float64{480: 300, 1200: 999}, 270},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := FTPFromTest(c.protocol, c.curve)
			if !ok || math.Abs(got-c.want) > 1e-9 {
				t.Errorf("got %v ok=%v, want %v", got, ok, c.want)
			}
		})
	}
}

func TestFTPFromTestFalseWhenTheNeededKeyIsAbsentOrZero(t *testing.T) {
	for protocol, wrong := range map[string]map[int]float64{
		ProtocolRamp:         {480: 300, 1200: 300},
		ProtocolTwentyMinute: {60: 400, 480: 300},
		ProtocolTwoByEight:   {60: 400, 1200: 300},
	} {
		if _, ok := FTPFromTest(protocol, wrong); ok {
			t.Errorf("%s: ok with the needed key missing", protocol)
		}
	}
	if _, ok := FTPFromTest(ProtocolRamp, map[int]float64{60: 0}); ok {
		t.Error("a zero best minute is not a result")
	}
	if _, ok := FTPFromTest("bogus", map[int]float64{60: 400}); ok {
		t.Error("unknown protocol must not produce a value")
	}
}

func TestRampWorkoutStepsForAGivenFTP(t *testing.T) {
	req := RampWorkout(300)
	if req.Sport != model.SportCycling {
		t.Fatalf("sport = %q", req.Sport)
	}
	steps := req.Steps
	first, last := steps[0], steps[len(steps)-1]
	if first.Intensity != workout.IntensityWarmup || first.Seconds != 5*60 || first.Target != workout.TargetOpen {
		t.Errorf("first step = %+v, want 5 min open warmup", first)
	}
	if last.Intensity != workout.IntensityCooldown || last.Seconds != 5*60 {
		t.Errorf("last step = %+v, want 5 min cooldown", last)
	}
	var ramp []workout.WorkoutStep
	for _, s := range steps {
		if s.Target == workout.TargetPower {
			ramp = append(ramp, s)
		}
	}
	// 50% + 6% per minute of 300 W: 150, 168, 186 ... capped at 170% = 510.
	// Steps are 150 + 18k until the cap: k = 0..20 (150 + 18*20 = 510).
	if len(ramp) != 21 {
		t.Fatalf("got %d power steps, want 21", len(ramp))
	}
	if ramp[0].TargetLow != 150 || ramp[0].TargetHigh != 150 {
		t.Errorf("first step = %v-%v, want 150", ramp[0].TargetLow, ramp[0].TargetHigh)
	}
	if ramp[1].TargetLow != 170 { // 168 -> 170
		t.Errorf("second step = %v, want 170 (168 rounded to 5 W)", ramp[1].TargetLow)
	}
	if ramp[len(ramp)-1].TargetLow != 510 {
		t.Errorf("last step = %v, want the 170%% cap of 510", ramp[len(ramp)-1].TargetLow)
	}
	for i, s := range ramp {
		if s.Seconds != 60 || s.Duration != workout.DurationTime {
			t.Errorf("step %d not one minute: %+v", i, s)
		}
		if math.Mod(s.TargetLow, 5) != 0 {
			t.Errorf("step %d = %v W, not a multiple of 5", i, s.TargetLow)
		}
		if s.TargetLow > 510 {
			t.Errorf("step %d = %v W over the cap", i, s.TargetLow)
		}
		if i > 0 && s.TargetLow < ramp[i-1].TargetLow {
			t.Errorf("ramp goes down at step %d", i)
		}
	}
	if !strings.Contains(req.Description, "0.75") || !strings.Contains(req.Description, "best 1-minute") {
		t.Errorf("description must carry the formula: %q", req.Description)
	}
}

func TestRampWorkoutCapsAtOneSeventyPercent(t *testing.T) {
	var max float64
	for _, s := range RampWorkout(100).Steps {
		if s.Target == workout.TargetPower && s.TargetHigh > max {
			max = s.TargetHigh
		}
	}
	if max != 170 {
		t.Errorf("max step = %v, want 170", max)
	}
}

func TestTwoByEightWorkoutShape(t *testing.T) {
	req := TwoByEightWorkout()
	var efforts int
	for _, s := range flatten(req.Steps) {
		if s.Seconds == 8*60 && s.Intensity == workout.IntensityInterval {
			efforts++
		}
	}
	if efforts != 2 {
		t.Errorf("got %d 8-minute efforts, want 2", efforts)
	}
	if !strings.Contains(req.Description, "0.90") {
		t.Errorf("description must carry the formula: %q", req.Description)
	}
}

func TestEveryEffortStepButTheRampsIsOpenTarget(t *testing.T) {
	for name, req := range map[string]workout.CreateWorkoutRequest{
		"twenty_minute": FTPTestWorkout(),
		"two_by_eight":  TwoByEightWorkout(),
	} {
		for _, s := range flatten(req.Steps) {
			if s.Target != workout.TargetOpen {
				t.Errorf("%s: step %q has target %q; ERG would lock the effort to the target", name, s.Name, s.Target)
			}
		}
	}
}

func TestProtocolsMenu(t *testing.T) {
	menu := Protocols()
	ids := []string{}
	for _, p := range menu {
		ids = append(ids, p.ID)
		if p.Name == "" || p.DurationMinutes <= 0 || p.Difficulty == "" || p.Formula == "" || p.ForWhom == "" || p.Prerequisites == "" {
			t.Errorf("%s: incomplete %+v", p.ID, p)
		}
	}
	if strings.Join(ids, ",") != "ramp,twenty_minute,two_by_eight" {
		t.Fatalf("ids = %v", ids)
	}
	for _, p := range menu {
		want := "resistance"
		if p.ID == ProtocolRamp {
			want = "erg"
		}
		if p.TrainerMode != want {
			t.Errorf("%s trainer mode = %q, want %q", p.ID, p.TrainerMode, want)
		}
	}
	if !ValidProtocol(ProtocolRamp) || ValidProtocol("x") {
		t.Error("ValidProtocol wrong")
	}
}
