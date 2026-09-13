// Package fitnesstest turns a rider's own recent completed sessions into an
// FTP estimate, and builds the two structured "go measure this properly"
// workouts a rider profile with no FTP or max HR on file needs: a 20-minute
// FTP test and a max-heart-rate field test.
//
// This is deliberately narrower than it might sound. Neither provider
// reliably exposes FTP or a true max HR (see docs/training-plan.md and
// workout.RiderProfile's own field comments — that is why those fields are
// rider-entered, never inferred straight from an account). What this
// package infers instead is a *training-load-shaped* estimate from the
// rider's own execution data — the same "best recent effort of about the
// right duration" idea TrainingPeaks, Strava and TrainerRoad's own
// auto-detected FTP already use — and only ever as a starting number a
// rider can see is an estimate and override, never a silent fact.
//
// Max HR gets no such estimate at all. internal/workout.CompletedSession
// only ever stores a session's *average* HR (Garmin's activity-list summary
// endpoint, not the per-second detail stream — see internal/garmin/
// activities.go's own doc comment for why), and average HR is always at or
// below true max, often by a wide margin during anything but an all-out
// effort. Guessing max HR from it would be more likely to mislead than
// help, so the only path this package offers for max HR is the field test.
package fitnesstest

import (
	"fmt"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// ftpWindow is one duration band a completed session's average power can
// fall in to count as an FTP proxy, and the factor that turns that average
// into an FTP estimate. Both bands are well-worn field-test durations: a
// ~20-minute best effort is conventionally multiplied by 0.95 (the number
// every 20-minute FTP test protocol uses), and a ~60-minute effort is
// already close enough to FTP's own definition ("best hour power") to use
// directly. A session outside both bands is not a good FTP proxy either
// way — too short and anaerobic capacity inflates it, too long and pacing
// for endurance deflates it — so it is simply not considered.
type ftpWindow struct {
	minSeconds, maxSeconds float64
	factor                 float64
}

var ftpWindows = []ftpWindow{
	{minSeconds: 15 * 60, maxSeconds: 25 * 60, factor: 0.95},
	{minSeconds: 50 * 60, maxSeconds: 70 * 60, factor: 1.00},
}

// EstimateFTP looks for the rider's best qualifying cycling effort among
// sessions and returns an FTP estimate from it. Pure and stateless — the
// same "give it data, get an answer, no memory of any estimate before it"
// shape internal/periodization.BuildPlan and internal/scheduler.NextWorkouts
// already take, deliberately: a detraining rider's FTP estimate can go back
// down as easily as up, which is the honest answer, not something worth a
// one-way ratchet to avoid.
//
// ok is false when no session qualifies (no power data at all, or nothing
// of a usable duration) — the caller's cue to fall back to FTPTestWorkout
// instead of guessing.
func EstimateFTP(sessions []workout.CompletedSession) (watts float64, ok bool) {
	for _, s := range sessions {
		if s.Sport != string(model.SportCycling) || s.AvgPowerWatts <= 0 {
			continue
		}
		for _, win := range ftpWindows {
			if s.DurationSeconds < win.minSeconds || s.DurationSeconds > win.maxSeconds {
				continue
			}
			if candidate := s.AvgPowerWatts * win.factor; candidate > watts {
				watts = candidate
			}
			break
		}
	}
	return watts, watts > 0
}

// openStep is the one shape almost every step in both test workouts below
// takes: no numeric target, because the entire point of either test is
// that the number it would target (FTP, max HR) is not known yet — a rider
// paces every step by feel or a fixed effort description, not a zone.
func openStep(name string, intensity workout.Intensity, seconds float64) workout.WorkoutStep {
	return workout.WorkoutStep{
		Name: name, Intensity: intensity, Duration: workout.DurationTime, Seconds: seconds,
		Target: workout.TargetOpen,
	}
}

// FTPTestWorkout builds the classic 20-minute FTP test: a long warmup with
// three short openers to prime the legs, a hard-but-not-maximal primer
// effort, a recovery, then the 20-minute effort itself, ridden as close to
// an even, sustainable-but-hard pace as the rider can hold. Cycling only —
// see this package's own doc comment for the running case.
func FTPTestWorkout() workout.CreateWorkoutRequest {
	return workout.CreateWorkoutRequest{
		Sport: model.SportCycling,
		Name:  "FTP Test (20-minute)",
		Description: "The classic 20-minute FTP test. After a real warmup and a hard " +
			"but not all-out primer effort, ride the 20-minute block as close to your " +
			"maximum sustainable, even pace as you can — steady, not a fade from going " +
			"out too hard. Afterward, take your average power for just that 20-minute " +
			"block and multiply by 0.95: that is your estimated FTP. Enter it in your " +
			"rider profile once you have it.",
		Steps: []workout.WorkoutStep{
			openStep("Warmup", workout.IntensityWarmup, 10*60),
			{
				Name:   "Openers",
				Repeat: 3,
				Steps: []workout.WorkoutStep{
					openStep("Hard", workout.IntensityInterval, 60),
					openStep("Easy", workout.IntensityRecovery, 60),
				},
			},
			openStep("Recovery", workout.IntensityRecovery, 5*60),
			openStep("Primer (hard, not all-out)", workout.IntensityInterval, 5*60),
			openStep("Recovery", workout.IntensityRecovery, 10*60),
			openStep("20-Minute Test (steady, all-out)", workout.IntensityInterval, 20*60),
			openStep("Cooldown", workout.IntensityCooldown, 10*60),
		},
	}
}

// MaxHRTestWorkout builds a field test for max heart rate: a long
// progressive warmup, three efforts of rising intensity with recovery
// between them, then a short all-out sprint immediately after the third —
// no recovery before it on purpose, since a maximal short effort stacked
// on top of an already-hard one is what reliably elicits true peak HR,
// more reliably than the third effort alone. Works for either sport; only
// the wording changes.
//
// This is a maximal-effort protocol. The description below carries a
// safety note deliberately — this package is not the place to skip it just
// because the workout is otherwise "just another structured session."
func MaxHRTestWorkout(sport model.Sport) workout.CreateWorkoutRequest {
	verb := "ride"
	if sport == model.SportRunning {
		verb = "run"
	}
	return workout.CreateWorkoutRequest{
		Sport: sport,
		Name:  "Max Heart Rate Test",
		Description: fmt.Sprintf(
			"A field test for max heart rate. Warm up thoroughly, then %s each of the "+
				"three efforts harder than the last, with full recovery between them — "+
				"the third should feel close to your limit. Immediately after the third "+
				"effort, with no recovery, go all-out for 30 seconds. Your highest heart "+
				"rate reading during or just after that final sprint is your estimated max "+
				"HR — enter it in your rider profile once you have it.\n\n"+
				"This is a maximal-effort test. If you are new to structured training, "+
				"have any heart condition, or have not exercised at high intensity "+
				"recently, talk to a doctor before attempting it, and stop immediately if "+
				"you feel chest pain, dizziness, or unusual discomfort.", verb),
		Steps: []workout.WorkoutStep{
			openStep("Warmup", workout.IntensityWarmup, 15*60),
			openStep("Effort 1 (hard)", workout.IntensityInterval, 3*60),
			openStep("Recovery", workout.IntensityRecovery, 3*60),
			openStep("Effort 2 (harder)", workout.IntensityInterval, 3*60),
			openStep("Recovery", workout.IntensityRecovery, 3*60),
			openStep("Effort 3 (near max)", workout.IntensityInterval, 3*60),
			openStep("All-out sprint (no recovery before this one)", workout.IntensityInterval, 30),
			openStep("Cooldown", workout.IntensityCooldown, 10*60),
		},
	}
}
