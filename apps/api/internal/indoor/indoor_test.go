package indoor

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/fitnesstest"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

const ftp = 200.0

func ftpProfile() workout.RiderProfile { return workout.RiderProfile{FTPWatts: ftp} }

func hrProfile() workout.RiderProfile { return workout.RiderProfile{MaxHR: 190} }

func timeStep(name string, in workout.Intensity, secs float64, t workout.TargetType, lo, hi float64) workout.WorkoutStep {
	return workout.WorkoutStep{Name: name, Intensity: in, Duration: workout.DurationTime, Seconds: secs, Target: t, TargetLow: lo, TargetHigh: hi}
}

// road builds warmup + one main step + cooldown, the shape the scheduler
// builds for an endurance or long ride.
func road(name string, zone workout.Zone, mainSecs float64, t workout.TargetType, lo, hi float64) workout.Workout {
	return workout.Workout{
		Sport: model.SportCycling, Name: name, Zone: zone,
		Steps: []workout.WorkoutStep{
			timeStep("Warmup", workout.IntensityWarmup, 300, t, lo, hi),
			timeStep("Warmup", workout.IntensityWarmup, 300, t, lo, hi),
			timeStep(name, workout.IntensityActive, mainSecs, t, lo, hi),
			timeStep("Cooldown", workout.IntensityCooldown, 600, t, lo, hi),
		},
	}
}

func mustConvert(t *testing.T, w workout.Workout, p workout.RiderProfile, smart bool) Result {
	t.Helper()
	r, err := Convert(w, p, smart)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(r.Steps) == 0 {
		t.Fatalf("Convert returned no steps")
	}
	return r
}

func TestDistanceStepsBecomeTimeAtRoadPace(t *testing.T) {
	w := workout.Workout{Sport: model.SportCycling, Name: "Hand built", Steps: []workout.WorkoutStep{
		{Name: "Ride", Intensity: workout.IntensityOther, Duration: workout.DurationDistance, Meters: 7800, Target: workout.TargetPower, TargetLow: 150, TargetHigh: 150},
	}}
	r := mustConvert(t, w, ftpProfile(), false)
	got := r.Steps[0]
	if got.Duration != workout.DurationTime || got.Seconds != 1000 || got.Meters != 0 {
		t.Errorf("step = %+v, want 1000 s time step", got)
	}
}

func TestOpenStepsGetLibraryLengths(t *testing.T) {
	open := func(in workout.Intensity) workout.WorkoutStep {
		return workout.WorkoutStep{Name: string(in), Intensity: in, Duration: workout.DurationOpen}
	}
	w := workout.Workout{Sport: model.SportCycling, Name: "Hand built", Steps: []workout.WorkoutStep{
		open(workout.IntensityWarmup), open(workout.IntensityActive), open(workout.IntensityCooldown),
	}}
	r := mustConvert(t, w, ftpProfile(), false)
	want := []float64{workoutlib.WarmupCooldownSeconds / 2, 300, workoutlib.WarmupCooldownSeconds / 2}
	for i, s := range r.Steps {
		if s.Duration != workout.DurationTime || s.Seconds != want[i] {
			t.Errorf("step %d = %+v, want time %v", i, s, want[i])
		}
	}
}

func TestRepeatBlocksAreConvertedRecursively(t *testing.T) {
	w := workout.Workout{Sport: model.SportCycling, Name: "Hand built", Zone: workout.ZoneThreshold, Steps: []workout.WorkoutStep{
		{Name: "Block", Repeat: 3, Steps: []workout.WorkoutStep{
			{Name: "Work", Intensity: workout.IntensityInterval, Duration: workout.DurationDistance, Meters: 3900, Target: workout.TargetPower, TargetLow: 200, TargetHigh: 200},
			{Name: "Rest", Intensity: workout.IntensityRest, Duration: workout.DurationOpen},
		}},
	}}
	r := mustConvert(t, w, ftpProfile(), false)
	b := r.Steps[0]
	if b.Repeat != 3 || len(b.Steps) != 2 {
		t.Fatalf("block = %+v", b)
	}
	if b.Steps[0].Duration != workout.DurationTime || b.Steps[0].Seconds != 500 {
		t.Errorf("nested work = %+v", b.Steps[0])
	}
	if b.Steps[1].Duration != workout.DurationTime || b.Steps[1].Seconds != 300 {
		t.Errorf("nested open rest = %+v", b.Steps[1])
	}
}

func TestHeartRateToPowerForEndurance(t *testing.T) {
	w := road("Endurance ride", workout.ZoneEndurance, 3600, workout.TargetHeartRate, 110, 140)
	r := mustConvert(t, w, ftpProfile(), false)
	main := r.Steps[2]
	if main.Target != workout.TargetPower || math.Abs(main.TargetLow-110) > 1e-9 || math.Abs(main.TargetHigh-150) > 1e-9 {
		t.Errorf("main = %+v, want power 55-75%% of FTP", main)
	}
	if !r.ERG {
		t.Errorf("ERG should be true when every non-warmup step has power")
	}
}

func TestHeartRateToPowerForEachStructuredZone(t *testing.T) {
	for _, z := range []workout.Zone{workout.ZoneTempo, workout.ZoneSweetSpot, workout.ZoneThreshold, workout.ZoneVO2Max, workout.ZoneAnaerobic} {
		t.Run(string(z), func(t *testing.T) {
			ladder, ok := workoutlib.LadderFor(model.SportCycling, string(z))
			if !ok {
				t.Fatalf("no ladder for %s", z)
			}
			w := workout.Workout{Sport: model.SportCycling, Name: "x", Zone: z, Steps: []workout.WorkoutStep{
				timeStep("Work", workout.IntensityInterval, 600, workout.TargetHeartRate, 150, 160),
			}}
			r := mustConvert(t, w, ftpProfile(), false)
			s := r.Steps[0]
			if s.Target != workout.TargetPower || s.TargetLow != ftp*ladder.LowPct || s.TargetHigh != ftp*ladder.HighPct {
				t.Errorf("step = %+v, want %v-%v of FTP", s, ladder.LowPct, ladder.HighPct)
			}
		})
	}
}

func TestHeartRateWarmupCooldownAndRestUseLibraryBands(t *testing.T) {
	w := workout.Workout{Sport: model.SportCycling, Name: "x", Zone: workout.ZoneThreshold, Steps: []workout.WorkoutStep{
		timeStep("Warmup", workout.IntensityWarmup, 600, workout.TargetHeartRate, 100, 120),
		timeStep("Rest", workout.IntensityRest, 120, workout.TargetHeartRate, 100, 110),
		timeStep("Recovery", workout.IntensityRecovery, 120, workout.TargetHeartRate, 100, 110),
		timeStep("Cooldown", workout.IntensityCooldown, 600, workout.TargetHeartRate, 100, 110),
	}}
	r := mustConvert(t, w, ftpProfile(), false)
	want := [][2]float64{{0.50, 0.65}, {0.45, 0.55}, {0.45, 0.55}, {0.45, 0.55}}
	for i, s := range r.Steps {
		if s.Target != workout.TargetPower || s.TargetLow != ftp*want[i][0] || s.TargetHigh != ftp*want[i][1] {
			t.Errorf("step %d = %+v, want %v of FTP", i, s, want[i])
		}
	}
}

func TestHeartRateInUnknownZoneStaysHeartRate(t *testing.T) {
	w := workout.Workout{Sport: model.SportCycling, Name: "Hand built", Steps: []workout.WorkoutStep{
		timeStep("Ride", workout.IntensityActive, 1800, workout.TargetHeartRate, 140, 150),
	}}
	r := mustConvert(t, w, ftpProfile(), false)
	if s := r.Steps[0]; s.Target != workout.TargetHeartRate || s.TargetLow != 140 {
		t.Errorf("step = %+v, want the HR target kept (zone unknown)", s)
	}
	if r.ERG {
		t.Errorf("ERG must be false with an HR step left")
	}
}

func TestNoFTPKeepsHeartRateAndSaysSo(t *testing.T) {
	w := road("Endurance ride", workout.ZoneEndurance, 3600, workout.TargetHeartRate, 110, 140)
	r := mustConvert(t, w, hrProfile(), true)
	for i, s := range r.Steps {
		if s.Target != workout.TargetHeartRate || s.TargetLow != 110 || s.TargetHigh != 140 {
			t.Errorf("step %d = %+v, want HR untouched", i, s)
		}
	}
	if r.ERG {
		t.Errorf("ERG must be false without FTP")
	}
	if !strings.Contains(r.Note, "No FTP set, so the trainer cannot control resistance. Ride by heart rate.") {
		t.Errorf("note = %q", r.Note)
	}
}

func TestCadenceAndOpenTargetsAreLeftAlone(t *testing.T) {
	w := workout.Workout{Sport: model.SportCycling, Name: "Hand built", Steps: []workout.WorkoutStep{
		timeStep("Spin", workout.IntensityInterval, 600, workout.TargetCadence, 90, 100),
		timeStep("Free", workout.IntensityInterval, 600, workout.TargetOpen, 0, 0),
	}}
	r := mustConvert(t, w, ftpProfile(), true)
	if !reflect.DeepEqual(r.Steps, w.Steps) {
		t.Errorf("steps changed: %+v", r.Steps)
	}
}

func TestMidpointCollapseOnlyForSmartTrainer(t *testing.T) {
	w := workout.Workout{Sport: model.SportCycling, Name: "Hand built", Zone: workout.ZoneThreshold, Steps: []workout.WorkoutStep{
		timeStep("Work", workout.IntensityInterval, 600, workout.TargetPower, 190, 210),
	}}
	plain := mustConvert(t, w, ftpProfile(), false)
	if s := plain.Steps[0]; s.TargetLow != 190 || s.TargetHigh != 210 {
		t.Errorf("without smart trainer the range stays: %+v", s)
	}
	smart := mustConvert(t, w, ftpProfile(), true)
	if s := smart.Steps[0]; s.TargetLow != 200 || s.TargetHigh != 200 {
		t.Errorf("with smart trainer the range collapses to 200: %+v", s)
	}
	if !smart.Changed {
		t.Errorf("collapse is a change")
	}
}

func TestEnduranceIsShortenedByThreeQuartersRoundedToFiveMinutes(t *testing.T) {
	// 60 min main -> 45 min.
	r := mustConvert(t, road("Endurance ride", workout.ZoneEndurance, 3600, workout.TargetPower, 110, 150), ftpProfile(), false)
	if got := r.Steps[2].Seconds; got != 2700 {
		t.Errorf("main = %v, want 2700", got)
	}
	// 100 min main -> 75 min exactly.
	r = mustConvert(t, road("Endurance ride", workout.ZoneEndurance, 6000, workout.TargetPower, 110, 150), ftpProfile(), false)
	if got := r.Steps[2].Seconds; got != 4500 {
		t.Errorf("main = %v, want 4500", got)
	}
	// 65 min main = 48.75 -> rounds to 50 min.
	r = mustConvert(t, road("Endurance ride", workout.ZoneEndurance, 3900, workout.TargetPower, 110, 150), ftpProfile(), false)
	if got := r.Steps[2].Seconds; got != 3000 {
		t.Errorf("main = %v, want 3000", got)
	}
	if !strings.Contains(r.Note, "ridden outside on another day") {
		t.Errorf("note should mention riding the rest outside: %q", r.Note)
	}
}

func TestShorteningKeepsWarmupAndCooldown(t *testing.T) {
	r := mustConvert(t, road("Endurance ride", workout.ZoneEndurance, 3600, workout.TargetPower, 110, 150), ftpProfile(), false)
	if r.Steps[0].Seconds != 300 || r.Steps[1].Seconds != 300 || r.Steps[3].Seconds != 600 {
		t.Errorf("warmup/cooldown changed: %+v", r.Steps)
	}
}

func TestShorteningIsCappedAtTwoHoursAndAFourHourRideEndsAtAbout2h20(t *testing.T) {
	// 4 h total = 13200 s of main work.
	r := mustConvert(t, road("Long ride", workout.ZoneEndurance, 13200, workout.TargetPower, 110, 150), ftpProfile(), false)
	if got := r.Steps[2].Seconds; got != MaxMainSeconds {
		t.Errorf("main = %v, want capped at %v", got, MaxMainSeconds)
	}
	if total := workout.PlannedSeconds(r.Steps); total != 8400 {
		t.Errorf("total = %v, want 8400 (2h20)", total)
	}
	if !strings.Contains(r.Note, "Long ride (4h00)") || !strings.Contains(r.Note, "2h20 on the trainer") {
		t.Errorf("note = %q", r.Note)
	}
}

func TestShorteningIsFlooredAtThirtyMinutesButNeverLongerThanOriginal(t *testing.T) {
	// 40 min main: 0.75 -> 30 min, exactly the floor.
	r := mustConvert(t, road("Endurance ride", workout.ZoneEndurance, 2400, workout.TargetPower, 110, 150), ftpProfile(), false)
	if got := r.Steps[2].Seconds; got != 1800 {
		t.Errorf("main = %v, want 1800", got)
	}
	// 35 min main: 0.75 -> 26 -> 25 min, floor lifts it to 30.
	r = mustConvert(t, road("Endurance ride", workout.ZoneEndurance, 2100, workout.TargetPower, 110, 150), ftpProfile(), false)
	if got := r.Steps[2].Seconds; got != 1800 {
		t.Errorf("main = %v, want floored at 1800", got)
	}
	// 20 min main is already under the floor: never longer than the original.
	r = mustConvert(t, road("Endurance ride", workout.ZoneEndurance, 1200, workout.TargetPower, 110, 150), ftpProfile(), false)
	if got := r.Steps[2].Seconds; got != 1200 {
		t.Errorf("main = %v, want 1200 unchanged", got)
	}
}

func TestHardSessionsAreNeverScaled(t *testing.T) {
	for _, z := range []workout.Zone{workout.ZoneTempo, workout.ZoneSweetSpot, workout.ZoneThreshold, workout.ZoneVO2Max, workout.ZoneAnaerobic} {
		w := road("Session", z, 3600, workout.TargetPower, 190, 210)
		r := mustConvert(t, w, ftpProfile(), false)
		if got := r.Steps[2].Seconds; got != 3600 {
			t.Errorf("%s: main = %v, want unscaled 3600", z, got)
		}
	}
	// The legacy name fallback: no zone, but a hard name.
	w := road("Tempo ride", "", 3600, workout.TargetPower, 190, 210)
	if got := mustConvert(t, w, ftpProfile(), false).Steps[2].Seconds; got != 3600 {
		t.Errorf("legacy Tempo ride main = %v, want unscaled", got)
	}
}

func TestHandBuiltIntervalsWithNoZoneAreNotScaled(t *testing.T) {
	w := workout.Workout{Sport: model.SportCycling, Name: "My hill repeats", Steps: []workout.WorkoutStep{
		{Name: "Hills", Repeat: 4, Steps: []workout.WorkoutStep{
			timeStep("Work", workout.IntensityActive, 600, workout.TargetPower, 250, 250),
			timeStep("Recovery", workout.IntensityRecovery, 300, workout.TargetPower, 100, 100),
		}},
	}}
	r := mustConvert(t, w, ftpProfile(), false)
	if r.Steps[0].Steps[0].Seconds != 600 || r.Steps[0].Repeat != 4 {
		t.Errorf("hand-built intervals were scaled: %+v", r.Steps[0])
	}
}

func TestFTPTestWorkoutStepsAreUntouched(t *testing.T) {
	for _, proto := range []string{"ramp", "twenty_minute", "two_by_eight"} {
		t.Run(proto, func(t *testing.T) {
			req, ok := fitnesstest.BuildTestWorkout(proto, ftp)
			if !ok {
				t.Fatalf("no workout for %s", proto)
			}
			w := workout.Workout{Sport: req.Sport, Name: req.Name, Steps: req.Steps, TestProtocol: proto}
			want := workout.Workout{Steps: req.Steps}
			r, err := Convert(w, ftpProfile(), true)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.Steps, want.Steps) {
				t.Errorf("test steps changed")
			}
			if r.Changed {
				t.Errorf("Changed must be false for a test workout")
			}
		})
	}
}

func TestFTPTestERGFollowsTheProtocolsTrainerMode(t *testing.T) {
	ramp, _ := fitnesstest.BuildTestWorkout("ramp", ftp)
	r, _ := Convert(workout.Workout{Sport: ramp.Sport, Steps: ramp.Steps, TestProtocol: "ramp"}, ftpProfile(), true)
	if !r.ERG {
		t.Errorf("the ramp is ERG")
	}
	for _, proto := range []string{"twenty_minute", "two_by_eight"} {
		req, _ := fitnesstest.BuildTestWorkout(proto, ftp)
		r, _ := Convert(workout.Workout{Sport: req.Sport, Steps: req.Steps, TestProtocol: proto}, ftpProfile(), true)
		if r.ERG {
			t.Errorf("%s is resistance mode, ERG must be false", proto)
		}
	}
}

func TestRunningIsRejected(t *testing.T) {
	w := workout.Workout{Sport: model.SportRunning, Name: "Easy run", Steps: []workout.WorkoutStep{timeStep("Run", workout.IntensityActive, 1800, workout.TargetOpen, 0, 0)}}
	if _, err := Convert(w, ftpProfile(), false); !errors.Is(err, ErrNotCycling) {
		t.Errorf("err = %v, want ErrNotCycling", err)
	}
}

func TestConvertingAnIndoorWorkoutAgainIsANoOp(t *testing.T) {
	w := road("Long ride", workout.ZoneEndurance, 13200, workout.TargetPower, 110, 150)
	first := mustConvert(t, w, ftpProfile(), true)
	w.Steps, w.Indoor = first.Steps, true
	second := mustConvert(t, w, ftpProfile(), true)
	if !reflect.DeepEqual(second.Steps, first.Steps) {
		t.Errorf("second conversion changed steps")
	}
	if second.Changed {
		t.Errorf("second conversion reports Changed")
	}
}

func TestNoteNamesTheOriginalAndERGFollowsTheSteps(t *testing.T) {
	w := road("Endurance ride", workout.ZoneEndurance, 3600, workout.TargetPower, 110, 150)
	r := mustConvert(t, w, ftpProfile(), false)
	if !strings.HasPrefix(r.Note, "Indoor version of Endurance ride (1h20), 1h05 on the trainer.") {
		t.Errorf("note = %q", r.Note)
	}
	if !r.ERG {
		t.Errorf("ERG false")
	}
	// Only-open workout: nothing for ERG to drive.
	open := workout.Workout{Sport: model.SportCycling, Name: "Free", Steps: []workout.WorkoutStep{timeStep("Ride", workout.IntensityActive, 3600, workout.TargetOpen, 0, 0)}}
	if mustConvert(t, open, ftpProfile(), false).ERG {
		t.Errorf("all-open workout must not claim ERG")
	}
}

func TestConversionDoesNotMutateItsInput(t *testing.T) {
	w := road("Long ride", workout.ZoneEndurance, 13200, workout.TargetPower, 110, 150)
	before := road("Long ride", workout.ZoneEndurance, 13200, workout.TargetPower, 110, 150)
	_ = mustConvert(t, w, ftpProfile(), true)
	if !reflect.DeepEqual(w.Steps, before.Steps) {
		t.Errorf("input mutated")
	}
}
