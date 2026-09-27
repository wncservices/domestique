package rideanalysis

import (
	"bytes"
	"math"
	"testing"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/filedef"

	"github.com/wncservices/domestique/apps/api/internal/fitworkout"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// ---- Step 1: verify FlattenSteps matches internal/fitworkout's own Encode
// order, rather than assuming the spec prose describes it correctly. ----

func TestFlattenMatchesFITEncode(t *testing.T) {
	steps := []workout.WorkoutStep{
		{Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
		{
			Name: "Intervals", Repeat: 4,
			Steps: []workout.WorkoutStep{
				{Name: "On", Intensity: workout.IntensityInterval, Duration: workout.DurationTime, Seconds: 180, Target: workout.TargetPower, TargetLow: 260, TargetHigh: 280},
				{Name: "Off", Intensity: workout.IntensityRecovery, Duration: workout.DurationTime, Seconds: 120, Target: workout.TargetOpen},
			},
		},
		{Name: "Cooldown", Intensity: workout.IntensityCooldown, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
	}

	fitBytes, err := fitworkout.Encode(workout.FITSteps(steps), fitworkout.Options{Name: "Test"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := decoder.New(bytes.NewReader(fitBytes)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wkt := filedef.NewWorkout(decoded.Messages...)

	var wantNames []string
	for _, s := range wkt.WorkoutSteps {
		wantNames = append(wantNames, s.WktStepName)
	}

	flattened := FlattenSteps(steps)
	if len(flattened) != len(wantNames) {
		t.Fatalf("FlattenSteps returned %d steps, FIT encode produced %d: flattened=%v fit=%v",
			len(flattened), len(wantNames), namesOf(flattened), wantNames)
	}
	for i, s := range flattened {
		if s.Name != wantNames[i] {
			t.Errorf("step %d: FlattenSteps name = %q, FIT encode name = %q (flattened=%v fit=%v)",
				i, s.Name, wantNames[i], namesOf(flattened), wantNames)
		}
	}
}

func namesOf(steps []workout.WorkoutStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Name
	}
	return out
}

// ---- Step 2: RED — Analyze and MatchPlanned. ----

// intervalWorkout is warmup / 4 x (on 260-280 W, off) / cooldown, the
// repeat-block fixture the brief's step-mapping and outcome tests share.
func intervalWorkout() *workout.Workout {
	return &workout.Workout{
		Steps: []workout.WorkoutStep{
			{Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
			{
				Name: "Intervals", Repeat: 4,
				Steps: []workout.WorkoutStep{
					{Name: "On", Intensity: workout.IntensityInterval, Duration: workout.DurationTime, Seconds: 180, Target: workout.TargetPower, TargetLow: 260, TargetHigh: 280},
					{Name: "Off", Intensity: workout.IntensityRecovery, Duration: workout.DurationTime, Seconds: 120, Target: workout.TargetOpen},
				},
			},
			{Name: "Cooldown", Intensity: workout.IntensityCooldown, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
		},
	}
}

// intervalLaps returns the lap fixtures for intervalWorkout. FIT's repeat
// block is written once (children, then a marker step the device loops
// back to — see FlattenSteps's own doc comment), so flattened indices are
// Warmup=0, On=1, Off=2, the repeat marker=3, Cooldown=4: a real device
// laps 4 times at index 1 (on) and 4 times at index 2 (off), never once at
// the marker itself, exactly the "several laps map to the same step index"
// case the brief's resolution calls out.
func intervalLaps(onWatts int) []lapFixture {
	laps := []lapFixture{{StartSec: 0, EndSec: 300, StepIndex: 0}}
	sec := 300
	for i := 0; i < 4; i++ {
		laps = append(laps, lapFixture{StartSec: sec, EndSec: sec + 180, StepIndex: 1})
		sec += 180
		laps = append(laps, lapFixture{StartSec: sec, EndSec: sec + 120, StepIndex: 2})
		sec += 120
	}
	laps = append(laps, lapFixture{StartSec: sec, EndSec: sec + 300, StepIndex: 4})
	_ = onWatts
	return laps
}

// intervalSamples builds one fixture per second matching intervalLaps: the
// on-laps at onWatts, everything else (warmup/off/cooldown) at a plainly
// out-of-range low wattage so it can never be mistaken for an "on" effort.
func intervalSamples(onWatts int) []fixture {
	var out []fixture
	sec := 0
	add := func(seconds, watts int) {
		for i := 0; i < seconds; i++ {
			out = append(out, fixture{Sec: sec, Power: watts})
			sec++
		}
	}
	add(300, 100) // warmup
	for i := 0; i < 4; i++ {
		add(180, onWatts) // on
		add(120, 100)     // off
	}
	add(300, 100) // cooldown
	return out
}

func fullProfile() workout.RiderProfile {
	return workout.RiderProfile{FTPWatts: 250, MaxHR: 190, RestingHR: 50}
}

func TestAnalyzeAllOnLapsAtTargetIsNailed(t *testing.T) {
	act := buildActivity(t, intervalSamples(270), intervalLaps(270))
	planned := intervalWorkout()

	a := Analyze(Input{Activity: act, Planned: planned, Profile: fullProfile()})

	hits := 0
	for _, s := range a.Steps {
		if s.Result == "hit" {
			hits++
		}
	}
	if hits != 4 {
		t.Errorf("hits = %d, want 4 (steps=%+v)", hits, a.Steps)
	}
	if a.Outcome != OutcomeNailed {
		t.Errorf("Outcome = %q, want %q", a.Outcome, OutcomeNailed)
	}
}

func TestAnalyzeTwoUnderLapsIsStruggled(t *testing.T) {
	// Laps: on-laps alternate 270 (hit), 230 (under), 270 (hit), 230 (under)
	// -> 2 of 4 hit (50% < 75%).
	act := buildActivity(t, mixedOnLapSamples(270, 230, 270, 230), intervalLaps(0))
	planned := intervalWorkout()

	a := Analyze(Input{Activity: act, Planned: planned, Profile: fullProfile()})

	under := 0
	for _, s := range a.Steps {
		if s.Result == "under" {
			under++
		}
	}
	if under != 2 {
		t.Errorf("under = %d, want 2 (steps=%+v)", under, a.Steps)
	}
	if a.Outcome != OutcomeStruggled {
		t.Errorf("Outcome = %q, want %q", a.Outcome, OutcomeStruggled)
	}
}

// mixedOnLapSamples builds intervalSamples but with each of the 4 "on"
// blocks at its own wattage, in order.
func mixedOnLapSamples(w1, w2, w3, w4 int) []fixture {
	watts := []int{w1, w2, w3, w4}
	var out []fixture
	sec := 0
	add := func(seconds, watts int) {
		for i := 0; i < seconds; i++ {
			out = append(out, fixture{Sec: sec, Power: watts})
			sec++
		}
	}
	add(300, 100)
	for i := 0; i < 4; i++ {
		add(180, watts[i])
		add(120, 100)
	}
	add(300, 100)
	return out
}

func TestAnalyzeOneOverLapCountsAsOverNotUnder(t *testing.T) {
	// 3 on-laps hit (270), 1 over (300) -> 3/4 = 75%, not "fewer than 75%".
	act := buildActivity(t, mixedOnLapSamples(270, 270, 270, 300), intervalLaps(0))
	planned := intervalWorkout()

	a := Analyze(Input{Activity: act, Planned: planned, Profile: fullProfile()})

	var overs, unders int
	for _, s := range a.Steps {
		switch s.Result {
		case "over":
			overs++
		case "under":
			unders++
		}
	}
	if overs != 1 {
		t.Errorf("overs = %d, want 1 (steps=%+v)", overs, a.Steps)
	}
	if unders != 0 {
		t.Errorf("unders = %d, want 0 (steps=%+v)", unders, a.Steps)
	}
	if a.Outcome == OutcomeStruggled {
		t.Errorf("Outcome = %q, want not struggled (3 of 4 hit is not fewer than 75%%)", a.Outcome)
	}
}

func TestAnalyzeStepToleranceBoundary(t *testing.T) {
	// Target 260-280: 247 = 0.95*260 exactly -> hit; 246 -> under.
	single := &workout.Workout{Steps: []workout.WorkoutStep{
		{Name: "Steady", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetPower, TargetLow: 260, TargetHigh: 280},
	}}

	hitAct := buildActivity(t, constantPower(600, 247), []lapFixture{{StartSec: 0, EndSec: 600, StepIndex: 0}})
	hit := Analyze(Input{Activity: hitAct, Planned: single, Profile: fullProfile()})
	if len(hit.Steps) != 1 || hit.Steps[0].Result != "hit" {
		t.Errorf("247 W: steps = %+v, want a single hit", hit.Steps)
	}

	underAct := buildActivity(t, constantPower(600, 246), []lapFixture{{StartSec: 0, EndSec: 600, StepIndex: 0}})
	under := Analyze(Input{Activity: underAct, Planned: single, Profile: fullProfile()})
	if len(under.Steps) != 1 || under.Steps[0].Result != "under" {
		t.Errorf("246 W: steps = %+v, want a single under", under.Steps)
	}
}

func mainStepWorkout() *workout.Workout {
	return &workout.Workout{Steps: []workout.WorkoutStep{
		{Name: "Endurance", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetPower, TargetLow: 180, TargetHigh: 220},
	}}
}

func TestAnalyzeNoLapsMainStep80PercentInRangeIsNailed(t *testing.T) {
	// 80% of the ride (2880s) at 200 W (in range), 20% (720s) at 100 W (out).
	var samples []fixture
	sec := 0
	for i := 0; i < 2880; i++ {
		samples = append(samples, fixture{Sec: sec, Power: 200})
		sec++
	}
	for i := 0; i < 720; i++ {
		samples = append(samples, fixture{Sec: sec, Power: 100})
		sec++
	}
	act := buildActivity(t, samples, nil)

	a := Analyze(Input{Activity: act, Planned: mainStepWorkout(), Profile: fullProfile()})

	if len(a.Steps) != 1 {
		t.Fatalf("Steps = %+v, want exactly one main-step score", a.Steps)
	}
	if a.Steps[0].Result != "hit" {
		t.Errorf("main step result = %q, want hit", a.Steps[0].Result)
	}
	// Actual is always the physical-unit average, never a percentage — the
	// ride here is 2880 s at 200 W and 720 s at 100 W: (2880*200+720*100)/3600 = 180.
	if want := 180.0; math.Abs(a.Steps[0].Actual-want) > 0.01 {
		t.Errorf("Actual = %v, want %v (ride's average power, not a percentage)", a.Steps[0].Actual, want)
	}
	if want := 80.0; math.Abs(a.Steps[0].InTargetPct-want) > 0.01 {
		t.Errorf("InTargetPct = %v, want %v", a.Steps[0].InTargetPct, want)
	}
	if a.Outcome != OutcomeNailed {
		t.Errorf("Outcome = %q, want %q", a.Outcome, OutcomeNailed)
	}
}

func TestAnalyzeNoLapsMainStep60PercentInRangeIsStruggled(t *testing.T) {
	// 60% of the ride (2160s) in range, 40% (1440s) out.
	var samples []fixture
	sec := 0
	for i := 0; i < 2160; i++ {
		samples = append(samples, fixture{Sec: sec, Power: 200})
		sec++
	}
	for i := 0; i < 1440; i++ {
		samples = append(samples, fixture{Sec: sec, Power: 100})
		sec++
	}
	act := buildActivity(t, samples, nil)

	a := Analyze(Input{Activity: act, Planned: mainStepWorkout(), Profile: fullProfile()})

	if len(a.Steps) != 1 || a.Steps[0].Result != "under" {
		t.Errorf("Steps = %+v, want a single under", a.Steps)
	}
	// (2160*200+1440*100)/3600 = 160 W, not a percentage.
	if want := 160.0; math.Abs(a.Steps[0].Actual-want) > 0.01 {
		t.Errorf("Actual = %v, want %v (ride's average power)", a.Steps[0].Actual, want)
	}
	if want := 60.0; math.Abs(a.Steps[0].InTargetPct-want) > 0.01 {
		t.Errorf("InTargetPct = %v, want %v", a.Steps[0].InTargetPct, want)
	}
	if a.Outcome != OutcomeStruggled {
		t.Errorf("Outcome = %q, want %q", a.Outcome, OutcomeStruggled)
	}
}

func TestAnalyzeDurationRatioIncomplete(t *testing.T) {
	// Planned 3600s, ride 1620s -> ratio 0.45 -> incomplete.
	act := buildActivity(t, constantPower(1620, 200), nil)
	a := Analyze(Input{Activity: act, Planned: mainStepWorkout(), Profile: fullProfile()})
	if a.Outcome != OutcomeIncomplete {
		t.Errorf("Outcome = %q, want %q (ratio=%v)", a.Outcome, OutcomeIncomplete, a.DurationRatio)
	}
}

func TestAnalyzeDurationRatioCompletedWithAllHits(t *testing.T) {
	// Planned 3600s, ride 3060s (ratio 0.85), all in-range -> completed
	// (ratio < 0.9 keeps it out of "nailed").
	act := buildActivity(t, constantPower(3060, 200), nil)
	a := Analyze(Input{Activity: act, Planned: mainStepWorkout(), Profile: fullProfile()})
	if a.Outcome != OutcomeCompleted {
		t.Errorf("Outcome = %q, want %q (ratio=%v, steps=%+v)", a.Outcome, OutcomeCompleted, a.DurationRatio, a.Steps)
	}
}

func TestAnalyzeNilPlannedIsUnplanned(t *testing.T) {
	act := buildActivity(t, constantPower(1200, 200), nil)
	a := Analyze(Input{Activity: act, Planned: nil, Profile: fullProfile()})
	if a.Outcome != OutcomeUnplanned {
		t.Errorf("Outcome = %q, want %q", a.Outcome, OutcomeUnplanned)
	}
	if len(a.Steps) != 0 {
		t.Errorf("Steps = %+v, want none for an unplanned ride", a.Steps)
	}
}

// ---- Load source fallback order. ----

func TestAnalyzeLoadSourceFITPowerWhenPowerAndFTP(t *testing.T) {
	act := buildActivity(t, constantPower(3600, 250), nil)
	a := Analyze(Input{Activity: act, Profile: workout.RiderProfile{FTPWatts: 250}})
	if a.LoadSource != LoadSourceFITPower {
		t.Errorf("LoadSource = %q, want %q", a.LoadSource, LoadSourceFITPower)
	}
	if math.Abs(a.Load-100) > 1 {
		t.Errorf("Load = %v, want ~100 (an hour at FTP)", a.Load)
	}
}

func TestAnalyzeLoadSourceProviderTSSWhenNoPower(t *testing.T) {
	act := buildActivity(t, constantHR(3600, 140), nil)
	a := Analyze(Input{Activity: act, Summary: Summary{TSS: 80}, Profile: workout.RiderProfile{FTPWatts: 250, MaxHR: 190}})
	if a.LoadSource != LoadSourceProviderTSS {
		t.Errorf("LoadSource = %q, want %q", a.LoadSource, LoadSourceProviderTSS)
	}
	if a.Load != 80 {
		t.Errorf("Load = %v, want 80", a.Load)
	}
}

func TestAnalyzeLoadSourceFITHRWhenNoPowerNoSummaryTSS(t *testing.T) {
	act := buildActivity(t, constantHR(3600, 171), nil) // 0.9 * 190
	a := Analyze(Input{Activity: act, Profile: workout.RiderProfile{MaxHR: 190, RestingHR: 50}})
	if a.LoadSource != LoadSourceFITHR {
		t.Errorf("LoadSource = %q, want %q", a.LoadSource, LoadSourceFITHR)
	}
	if math.Abs(a.Load-100) > 1 {
		t.Errorf("Load = %v, want ~100", a.Load)
	}
}

func TestAnalyzeLoadSourceEstimateWhenNothing(t *testing.T) {
	profile := workout.RiderProfile{}
	a := Analyze(Input{Summary: Summary{DurationSeconds: 3600}, Profile: profile})
	if a.LoadSource != LoadSourceEstimate {
		t.Errorf("LoadSource = %q, want %q", a.LoadSource, LoadSourceEstimate)
	}
	want := workout.TrainingLoad(3600, 0, 0, profile)
	if a.Load != want {
		t.Errorf("Load = %v, want %v (workout.TrainingLoad)", a.Load, want)
	}
}

func TestAnalyzeNilActivityUsesSummaryMetricsAndScoresMainStep(t *testing.T) {
	summary := Summary{DurationSeconds: 3600, NormalizedPower: 210, TSS: 90}
	a := Analyze(Input{Summary: summary, Planned: mainStepWorkout(), Profile: fullProfile()})

	if a.NormalizedPower != 210 {
		t.Errorf("NormalizedPower = %v, want 210 (from summary)", a.NormalizedPower)
	}
	if a.TSS != 90 {
		t.Errorf("TSS = %v, want 90 (from summary)", a.TSS)
	}
	if a.PowerZoneSeconds != ([7]int{}) {
		t.Errorf("PowerZoneSeconds = %v, want empty with no activity", a.PowerZoneSeconds)
	}
	if a.HRZoneSeconds != ([5]int{}) {
		t.Errorf("HRZoneSeconds = %v, want empty with no activity", a.HRZoneSeconds)
	}
	if len(a.Steps) != 1 {
		t.Fatalf("Steps = %+v, want a single score against the main step", a.Steps)
	}
	// 210 is within [180*0.95, 220*1.05] -> hit.
	if a.Steps[0].Result != "hit" {
		t.Errorf("Steps[0].Result = %q, want hit (NP 210 vs target 180-220)", a.Steps[0].Result)
	}
	// ratio = 1 (DurationSeconds set, PlannedSeconds 3600) and the main
	// step hit -> nailed.
	if a.Outcome != OutcomeNailed {
		t.Errorf("Outcome = %q, want %q", a.Outcome, OutcomeNailed)
	}
}

// TestAnalyzeMainStepScoredByTimeWhenOnlyOtherStepsHaveLaps covers the
// ruling that the whole-ride time-in-range fallback applies whenever no lap
// maps to the *main* step specifically — not only when no lap scored
// anything at all. Here a lap scores the warmup (an open-target step, so it
// is never actually scored) and the device never laps the main step, so the
// only lap in the ride carries no scorable target: the main step must still
// get a time-in-range score, not be left unscored.
//
// Round 2's finding: this score must be computed over the samples the
// warmup lap did NOT already claim, not the whole ride — otherwise the
// lapped warmup's easy, out-of-range wattage dilutes the main step's own
// average and time-in-range fraction. Asserts the exact undiluted numbers
// (180 W / 80% in range for just the Endurance segment), which a
// whole-ride computation would report as ~173 W / ~73% instead.
func TestAnalyzeMainStepScoredByTimeWhenOnlyOtherStepsHaveLaps(t *testing.T) {
	w := &workout.Workout{Steps: []workout.WorkoutStep{
		{Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
		{Name: "Endurance", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3300, Target: workout.TargetPower, TargetLow: 180, TargetHigh: 220},
	}}
	// 80% of the non-warmup ride in range, one lap covering only the
	// warmup (step index 0, open target, never scored by scoreLaps, but
	// still excluded from the main step's own window).
	var samples []fixture
	sec := 0
	add := func(seconds, watts int) {
		for i := 0; i < seconds; i++ {
			samples = append(samples, fixture{Sec: sec, Power: watts})
			sec++
		}
	}
	add(300, 100)  // warmup
	add(2640, 200) // in range: 80% of 3300
	add(660, 100)  // out of range: 20% of 3300
	act := buildActivity(t, samples, []lapFixture{{StartSec: 0, EndSec: 300, StepIndex: 0}})

	a := Analyze(Input{Activity: act, Planned: w, Profile: fullProfile()})

	if len(a.Steps) != 1 {
		t.Fatalf("Steps = %+v, want a single main-step time-in-range score (the warmup lap scores nothing)", a.Steps)
	}
	if a.Steps[0].Index != 1 {
		t.Errorf("Steps[0].Index = %d, want 1 (the Endurance/main step)", a.Steps[0].Index)
	}
	if a.Steps[0].Result != "hit" {
		t.Errorf("Steps[0].Result = %q, want hit (80%% in range)", a.Steps[0].Result)
	}
	// (2640*200 + 660*100) / 3300 = 180 W exactly — the Endurance segment
	// alone, not the whole-ride average of ~173.33 W a diluted calculation
	// would report.
	if want := 180.0; math.Abs(a.Steps[0].Actual-want) > 0.01 {
		t.Errorf("Actual = %v, want %v (undiluted by the warmup lap)", a.Steps[0].Actual, want)
	}
	if want := 80.0; math.Abs(a.Steps[0].InTargetPct-want) > 0.01 {
		t.Errorf("InTargetPct = %v, want %v (undiluted by the warmup lap)", a.Steps[0].InTargetPct, want)
	}
	if a.Outcome != OutcomeNailed {
		t.Errorf("Outcome = %q, want %q", a.Outcome, OutcomeNailed)
	}
}

// TestAnalyzeLongLappedWarmupNoLongerFlipsMainStepVerdict is round 2's own
// regression case: a long, easy, lapped warmup whose out-of-range seconds
// would pull the whole-ride time-in-range fraction under the 70% hit
// threshold (3000 in-range of 4500 total = 66.7%), even though the main
// step itself is 100% in range across its own 3000 s. Excluding the
// warmup's lapped seconds (mainStepWindow) keeps the verdict "hit"; without
// the fix this reports "under" and the outcome would not reach nailed.
func TestAnalyzeLongLappedWarmupNoLongerFlipsMainStepVerdict(t *testing.T) {
	w := &workout.Workout{Steps: []workout.WorkoutStep{
		{Name: "Warmup", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime, Seconds: 1500, Target: workout.TargetOpen},
		{Name: "Endurance", Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3000, Target: workout.TargetPower, TargetLow: 180, TargetHigh: 220},
	}}
	var samples []fixture
	sec := 0
	add := func(seconds, watts int) {
		for i := 0; i < seconds; i++ {
			samples = append(samples, fixture{Sec: sec, Power: watts})
			sec++
		}
	}
	add(1500, 50)  // long easy warmup, well out of the main step's range
	add(3000, 200) // Endurance: 100% in range on its own
	act := buildActivity(t, samples, []lapFixture{{StartSec: 0, EndSec: 1500, StepIndex: 0}})

	a := Analyze(Input{Activity: act, Planned: w, Profile: fullProfile()})

	if len(a.Steps) != 1 {
		t.Fatalf("Steps = %+v, want a single main-step score", a.Steps)
	}
	if a.Steps[0].Result != "hit" {
		t.Errorf("Steps[0].Result = %q, want hit — a whole-ride calculation "+
			"(3000 in-range of 4500 = 66.7%%) would wrongly report \"under\"", a.Steps[0].Result)
	}
	if want := 200.0; math.Abs(a.Steps[0].Actual-want) > 0.01 {
		t.Errorf("Actual = %v, want %v", a.Steps[0].Actual, want)
	}
	if want := 100.0; math.Abs(a.Steps[0].InTargetPct-want) > 0.01 {
		t.Errorf("InTargetPct = %v, want %v", a.Steps[0].InTargetPct, want)
	}
	if a.Outcome != OutcomeNailed {
		t.Errorf("Outcome = %q, want %q", a.Outcome, OutcomeNailed)
	}
}

// TestAnalyzeNoHardStepsUsesMainStepForNailed exercises the no-hard-steps
// rule directly: the only step with a target is a cooldown, which
// isHardStep never counts as hard (intensity is neither interval nor
// active) — the outcome still reaches "nailed" through the main-step
// substitution the resolution describes, not through any hard-step count.
func TestAnalyzeNoHardStepsUsesMainStepForNailed(t *testing.T) {
	w := &workout.Workout{Steps: []workout.WorkoutStep{
		{Name: "Easy spin", Intensity: workout.IntensityCooldown, Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetPower, TargetLow: 100, TargetHigh: 140},
	}}
	act := buildActivity(t, constantPower(3600, 120), []lapFixture{{StartSec: 0, EndSec: 3600, StepIndex: 0}})

	a := Analyze(Input{Activity: act, Planned: w, Profile: fullProfile()})

	if len(a.Steps) != 1 || a.Steps[0].Result != "hit" {
		t.Fatalf("Steps = %+v, want a single hit", a.Steps)
	}
	if isHardStep(FlattenSteps(w.Steps)[0]) {
		t.Fatal("test setup error: the only step must not count as hard")
	}
	if a.Outcome != OutcomeNailed {
		t.Errorf("Outcome = %q, want %q (no hard steps, main step hit, ratio >= 0.9)", a.Outcome, OutcomeNailed)
	}
}

func TestMatchPlannedPicksClosestDurationSameSportAndDate(t *testing.T) {
	planned := []workout.Workout{
		{Date: "2026-01-05", Sport: "cycling", Steps: []workout.WorkoutStep{{Duration: workout.DurationTime, Seconds: 3600}}}, // 60 min
		{Date: "2026-01-05", Sport: "cycling", Steps: []workout.WorkoutStep{{Duration: workout.DurationTime, Seconds: 7200}}}, // 120 min
		{Date: "2026-01-05", Sport: "running", Steps: []workout.WorkoutStep{{Duration: workout.DurationTime, Seconds: 6600}}}, // 110 min, wrong sport
		{Date: "2026-01-06", Sport: "cycling", Steps: []workout.WorkoutStep{{Duration: workout.DurationTime, Seconds: 6600}}}, // 110 min, wrong date
	}

	got := MatchPlanned("2026-01-05", "cycling", 6600, planned) // ride 110 min
	if got == nil {
		t.Fatal("MatchPlanned returned nil")
	}
	if workout.PlannedSeconds(got.Steps) != 7200 {
		t.Errorf("matched workout planned seconds = %v, want 7200 (the 120-min one)", workout.PlannedSeconds(got.Steps))
	}
}
