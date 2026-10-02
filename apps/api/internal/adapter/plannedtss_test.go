package adapter

import (
	"math"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

func timed(name string, in workout.Intensity, secs float64, target workout.TargetType, lo, hi float64) workout.WorkoutStep {
	return workout.WorkoutStep{Name: name, Intensity: in, Duration: workout.DurationTime, Seconds: secs, Target: target, TargetLow: lo, TargetHigh: hi}
}

func ride(steps ...workout.WorkoutStep) workout.Workout {
	return workout.Workout{Sport: model.SportCycling, Steps: steps}
}

func rung(t *testing.T, zone string, level int, ftp float64) workout.Workout {
	t.Helper()
	l, ok := workoutlib.LadderFor(model.SportCycling, zone)
	if !ok {
		t.Fatalf("no ladder for %s", zone)
	}
	req := workoutlib.Instantiate(l, l.Rungs[level-1], workout.RiderProfile{FTPWatts: ftp})
	return workout.Workout{Sport: model.SportCycling, Steps: req.Steps}
}

func TestPlannedTSSHandComputedCase(t *testing.T) {
	// FTP 200: a 10 min warmup and cooldown with no power target (0.55 FTP =
	// 110 W) around 20 min at a 190-210 W target (midpoint 200 W).
	// NP = ((1200 * 110^4 + 1200 * 200^4) / 2400)^(1/4) = 171.90 W,
	// IF = 0.8595, TSS = (2400/3600) * IF^2 * 100 = 49.25.
	w := ride(
		timed("Warmup", workout.IntensityWarmup, 600, workout.TargetOpen, 0, 0),
		timed("Main", workout.IntensityActive, 1200, workout.TargetPower, 190, 210),
		timed("Cooldown", workout.IntensityCooldown, 600, workout.TargetOpen, 0, 0),
	)
	got, ok := PlannedTSS(w, 200)
	if !ok || math.Abs(got-49.25) > 0.01 {
		t.Errorf("PlannedTSS = %v (%v), want 49.25", got, ok)
	}
}

func TestPlannedTSSExpandsRepeatsAndMatchesTheFlatForm(t *testing.T) {
	work := timed("Work", workout.IntensityInterval, 300, workout.TargetPower, 300, 300)
	rest := timed("Recovery", workout.IntensityRecovery, 300, workout.TargetOpen, 0, 0) // 0.45 FTP = 90 W
	repeated := ride(workout.WorkoutStep{Name: "Block", Repeat: 2, Steps: []workout.WorkoutStep{work, rest}})
	flat := ride(work, rest, work, rest)

	a, okA := PlannedTSS(repeated, 200)
	b, okB := PlannedTSS(flat, 200)
	if !okA || !okB || math.Abs(a-b) > 1e-9 {
		t.Fatalf("repeated %v (%v) != flat %v (%v)", a, okA, b, okB)
	}
	// NP = ((300*300^4 + 300*90^4) / 600)^(1/4) = 252.78 W over 1200 s.
	want := (1200.0 / 3600) * (252.7782 / 200) * (252.7782 / 200) * 100
	if math.Abs(a-want) > 0.05 {
		t.Errorf("PlannedTSS = %v, want about %v", a, want)
	}
	// The repeat count matters: one rep is half the load of two at the same intensity.
	single := ride(work, rest)
	if one, _ := PlannedTSS(single, 200); math.Abs(one*2-a) > 0.05 {
		t.Errorf("one rep %v, two reps %v: TSS should double for the same intensity", one, a)
	}
}

func TestPlannedTSSPricesStructuredSessionsAboveEnduranceAndHarderRungsHigher(t *testing.T) {
	const ftp = 250
	endurance := workout.Workout{Sport: model.SportCycling, Steps: scheduler.BuildEnduranceSession(1, false, model.SportCycling, workout.RiderProfile{FTPWatts: ftp}).Steps}
	vo2, _ := PlannedTSS(rung(t, "vo2max", 5, ftp), ftp) // 60 minutes
	easy, _ := PlannedTSS(endurance, ftp)
	if vo2 <= easy*1.2 {
		t.Errorf("VO2max L5 = %v, endurance 60 min = %v: a 60-minute VO2max session must cost clearly more", vo2, easy)
	}
	t3, _ := PlannedTSS(rung(t, "threshold", 3, ftp), ftp)
	t5, _ := PlannedTSS(rung(t, "threshold", 5, ftp), ftp)
	if t5 <= t3 {
		t.Errorf("threshold L5 = %v, L3 = %v: the harder rung must cost more", t5, t3)
	}
}

func TestPlannedTSSIsNothingWithoutAnFTPOrTimeOrForARun(t *testing.T) {
	w := rung(t, "threshold", 4, 250)
	if got, ok := PlannedTSS(w, 0); ok || got != 0 {
		t.Errorf("no FTP = %v (%v), want 0 and not ok", got, ok)
	}
	run := w
	run.Sport = model.SportRunning
	if got, ok := PlannedTSS(run, 250); ok || got != 0 {
		t.Errorf("a run = %v (%v), want 0 and not ok", got, ok)
	}
	distance := ride(workout.WorkoutStep{Name: "Ride", Duration: workout.DurationDistance, Meters: 20000, Target: workout.TargetOpen})
	if got, ok := PlannedTSS(distance, 250); ok || got != 0 {
		t.Errorf("distance-only = %v (%v), want 0 and not ok", got, ok)
	}
}

func TestPlannedTSSUsesTheIntensityFractionsForStepsWithoutPower(t *testing.T) {
	// One hour at each fraction gives IF = fraction: 0.65^2 * 100 = 42.25.
	w := ride(timed("Ride", workout.IntensityActive, 3600, workout.TargetHeartRate, 120, 140))
	if got, _ := PlannedTSS(w, 250); math.Abs(got-42.25) > 1e-9 {
		t.Errorf("active = %v, want 42.25", got)
	}
	w = ride(timed("Rest", workout.IntensityRest, 3600, workout.TargetOpen, 0, 0))
	if got, _ := PlannedTSS(w, 250); math.Abs(got-20.25) > 1e-9 {
		t.Errorf("rest = %v, want 20.25", got)
	}
	w = ride(timed("Warmup", workout.IntensityWarmup, 3600, workout.TargetOpen, 0, 0))
	if got, _ := PlannedTSS(w, 250); math.Abs(got-30.25) > 1e-9 {
		t.Errorf("warmup = %v, want 30.25", got)
	}
}

// EstimatePlannedTSS reads the first power target (the warm-up) as the whole
// session's intensity; PlannedTSS sizes every step. For a structured session
// the season sum needs the second, so it must come out higher.
func TestPlannedTSSExceedsTheFirstTargetEstimateForAStructuredSession(t *testing.T) {
	const ftp = 250
	w := rung(t, "threshold", 4, ftp)
	full, ok := PlannedTSS(w, ftp)
	if !ok {
		t.Fatal("threshold session not sized")
	}
	if first := EstimatePlannedTSS(w, ftp); full <= first {
		t.Errorf("PlannedTSS %v <= EstimatePlannedTSS %v: the step-by-step estimate must exceed the first-target one for a structured session", full, first)
	}
}
