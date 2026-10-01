package fitnesstest

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// With an FTP, every easy step of every test has a power range a smart
// trainer can hold, and every effort step stays open so the test measures
// the rider, not the trainer.
func TestTestWorkoutsGiveEasyStepsPowerAndLeaveEffortsOpen(t *testing.T) {
	for _, protocol := range []string{ProtocolRamp, ProtocolTwentyMinute, ProtocolTwoByEight} {
		req, ok := BuildTestWorkout(protocol, 250)
		if !ok {
			t.Fatalf("%s: unknown", protocol)
		}
		var walk func([]workout.WorkoutStep)
		walk = func(steps []workout.WorkoutStep) {
			for _, s := range steps {
				if s.Repeat >= 2 {
					walk(s.Steps)
					continue
				}
				switch {
				case isEasy(s.Intensity):
					if s.Target != workout.TargetPower || s.TargetLow != 100 || s.TargetHigh != 140 {
						t.Errorf("%s %q: %s %v-%v, want power 100-140 W", protocol, s.Name, s.Target, s.TargetLow, s.TargetHigh)
					}
				case protocol != ProtocolRamp && s.Target != workout.TargetOpen:
					t.Errorf("%s effort %q has a %s target, want open", protocol, s.Name, s.Target)
				}
			}
		}
		walk(req.Steps)
	}
}

// Without an FTP there is nothing to base a wattage on: the steps stay open.
func TestTestWorkoutsWithoutFTPStayOpen(t *testing.T) {
	req, _ := BuildTestWorkout(ProtocolTwentyMinute, 0)
	for _, s := range req.Steps {
		if s.Repeat < 2 && s.Target != workout.TargetOpen {
			t.Errorf("%q has a %s target with no FTP", s.Name, s.Target)
		}
	}
}
