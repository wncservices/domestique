package fitnesstest

import (
	"fmt"
	"math"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The FTP test protocol ids. They are stored on a workout (test_protocol) and
// sent over the API, so they are wire values, not display text.
const (
	ProtocolRamp         = "ramp"
	ProtocolTwentyMinute = "twenty_minute"
	ProtocolTwoByEight   = "two_by_eight"
)

// Trainer modes. ERG holds an absolute wattage, which is exactly right for a
// ramp's fixed steps and exactly wrong for an all-out effort: a trainer that
// locks the watts caps the rider at the target, so the test would measure the
// target and not the rider. Efforts that are ridden to a maximum therefore
// ask for resistance (level or slope) mode.
const (
	TrainerModeERG        = "erg"
	TrainerModeResistance = "resistance"
)

// Power-curve keys (seconds) each protocol's formula reads. They are
// rideanalysis.PowerCurve's own windows.
const (
	curveKeyMinute       = 60
	curveKeyEightMinutes = 480
	curveKeyTwentyMin    = 1200
)

// Protocol is one entry in the test menu the rider chooses from.
type Protocol struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	DurationMinutes int    `json:"durationMinutes"`
	Difficulty      string `json:"difficulty"`
	TrainerMode     string `json:"trainerMode"`
	Formula         string `json:"formula"`
	ForWhom         string `json:"forWhom"`
	Prerequisites   string `json:"prerequisites"`
}

// Protocols is the test menu, default (ramp) first.
func Protocols() []Protocol {
	return []Protocol{
		{
			ID: ProtocolRamp, Name: "Ramp test", DurationMinutes: 35, Difficulty: "moderate",
			TrainerMode: TrainerModeERG, Formula: "0.75 x best 1-minute power",
			ForWhom:       "The default. Best for a first test or if you pace badly: it is short and there is nothing to pace.",
			Prerequisites: "A power meter or smart trainer, and a rough FTP to start from.",
		},
		{
			ID: ProtocolTwentyMinute, Name: "20-minute test", DurationMinutes: 60, Difficulty: "hard",
			TrainerMode: TrainerModeResistance, Formula: "0.95 x best 20-minute power",
			ForWhom:       "Experienced riders who can pace an all-out effort. Needs no FTP guess.",
			Prerequisites: "A power meter or smart trainer.",
		},
		{
			ID: ProtocolTwoByEight, Name: "2 x 8-minute test", DurationMinutes: 65, Difficulty: "hard",
			TrainerMode: TrainerModeResistance, Formula: "0.90 x the higher 8-minute power",
			ForWhom:       "Riders who fade in a 20-minute effort. The second effort checks the first.",
			Prerequisites: "A power meter or smart trainer.",
		},
	}
}

// ValidProtocol reports whether id names a protocol in the menu.
func ValidProtocol(id string) bool {
	for _, p := range Protocols() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// FTPFromTest is the single implementation of the three test formulas: it
// turns a test ride's power curve into an FTP. ok is false when the curve
// lacks the window the protocol needs (a ride cut short, or no power), or
// the protocol is unknown, so a missing result is never a 0 W FTP.
func FTPFromTest(protocol string, curve map[int]float64) (watts float64, ok bool) {
	var key int
	var factor float64
	switch protocol {
	case ProtocolRamp:
		key, factor = curveKeyMinute, 0.75
	case ProtocolTwentyMinute:
		key, factor = curveKeyTwentyMin, 0.95
	case ProtocolTwoByEight:
		key, factor = curveKeyEightMinutes, 0.90
	default:
		return 0, false
	}
	best, has := curve[key]
	if !has || best <= 0 {
		return 0, false
	}
	return best * factor, true
}

// Ramp shape: start at half the estimate, add 6 % of it every minute, and
// never ask for more than 170 % (which nobody reaches: failure comes first).
// Expressed in whole percent so the step count does not depend on float
// rounding of 0.06 increments.
const (
	rampStartPct = 50
	rampStepPct  = 6
	rampCapPct   = 170
)

func roundTo5(w float64) float64 { return math.Round(w/5) * 5 }

// RampWorkout builds the ramp test for a rider whose FTP is estimated at
// ftp: 5 minutes easy, one-minute power steps from 50 % of the estimate
// rising 6 % of it each minute, ridden to failure, then 5 minutes easy.
// Absolute watts rounded to 5 W. This is the only test with power targets,
// and it is meant for ERG, where each step is a wattage the trainer holds.
func RampWorkout(ftp float64) workout.CreateWorkoutRequest {
	steps := []workout.WorkoutStep{openStep("Warmup", workout.IntensityWarmup, 5*60)}
	for pct := rampStartPct; pct <= rampCapPct; pct += rampStepPct {
		w := roundTo5(ftp * float64(pct) / 100)
		steps = append(steps, workout.WorkoutStep{
			Name:      fmt.Sprintf("Ramp %d W", int(w)),
			Intensity: workout.IntensityInterval, Duration: workout.DurationTime, Seconds: 60,
			Target: workout.TargetPower, TargetLow: w, TargetHigh: w,
		})
	}
	steps = append(steps, openStep("Cooldown", workout.IntensityCooldown, 5*60))
	return workout.CreateWorkoutRequest{
		Sport: model.SportCycling,
		Name:  "FTP Test (ramp)",
		Description: "The ramp test. After the warmup the power rises a little every minute, " +
			"starting easy. Keep pedalling until you cannot hold the step any more, then " +
			"spin down. Your FTP is 0.75 x your best 1-minute power from the ride: the app " +
			"reads it from the ride for you, and if you ride without the app take that " +
			"best minute yourself. Best on a smart trainer in ERG mode, which holds each " +
			"step for you.",
		Steps: steps,
	}
}

// TwoByEightWorkout builds the 2 x 8-minute test: a long warmup with
// openers, an 8-minute all-out effort, a real recovery, and a second
// 8-minute effort. FTP is 0.90 x the higher of the two, so the second effort
// checks the first. Every step is open-target: an ERG trainer would lock the
// effort to a wattage and the test would measure the target, not the rider.
func TwoByEightWorkout() workout.CreateWorkoutRequest {
	return workout.CreateWorkoutRequest{
		Sport: model.SportCycling,
		Name:  "FTP Test (2 x 8-minute)",
		Description: "The 2 x 8-minute test. After the warmup and openers, ride 8 minutes as " +
			"hard as you can hold evenly, recover for 10 minutes, then do it again. Your " +
			"FTP is 0.90 x the higher of the two 8-minute average powers. Pace both the " +
			"same and don't go out too hard: a fade in the second one means the first was " +
			"too aggressive. On a smart trainer use resistance (level or slope) mode, " +
			"not ERG: ERG caps you at its target and the test would measure the target.",
		Steps: []workout.WorkoutStep{
			openStep("Warmup", workout.IntensityWarmup, 15*60),
			{
				Name:   "Openers",
				Repeat: 3,
				Steps: []workout.WorkoutStep{
					openStep("Hard", workout.IntensityInterval, 60),
					openStep("Easy", workout.IntensityRecovery, 60),
				},
			},
			openStep("Recovery", workout.IntensityRecovery, 5*60),
			openStep("Effort 1 (8 min, all-out steady)", workout.IntensityInterval, 8*60),
			openStep("Recovery", workout.IntensityRecovery, 10*60),
			openStep("Effort 2 (8 min, all-out steady)", workout.IntensityInterval, 8*60),
			openStep("Cooldown", workout.IntensityCooldown, 10*60),
		},
	}
}

// BuildTestWorkout returns the workout for a protocol. ftp is only read by the
// ramp; ok is false for an unknown protocol.
func BuildTestWorkout(protocol string, ftp float64) (workout.CreateWorkoutRequest, bool) {
	switch protocol {
	case ProtocolRamp:
		return RampWorkout(ftp), true
	case ProtocolTwentyMinute:
		return FTPTestWorkout(), true
	case ProtocolTwoByEight:
		return TwoByEightWorkout(), true
	}
	return workout.CreateWorkoutRequest{}, false
}
