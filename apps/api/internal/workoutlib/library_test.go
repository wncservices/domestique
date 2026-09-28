package workoutlib

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/fitworkout"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// every zone/sport combination the spec's table defines.
func allLadderKeys() []struct {
	sport model.Sport
	zone  string
} {
	return []struct {
		sport model.Sport
		zone  string
	}{
		{model.SportCycling, "tempo"},
		{model.SportCycling, "sweet_spot"},
		{model.SportCycling, "threshold"},
		{model.SportCycling, "vo2max"},
		{model.SportCycling, "anaerobic"},
		{model.SportRunning, "tempo"},
		{model.SportRunning, "threshold"},
		{model.SportRunning, "intervals"},
	}
}

func TestLadderForEveryZoneHasTenNonDecreasingRungs(t *testing.T) {
	for _, k := range allLadderKeys() {
		l, ok := LadderFor(k.sport, k.zone)
		if !ok {
			t.Fatalf("LadderFor(%s, %s): not found", k.sport, k.zone)
		}
		if l.Label == "" {
			t.Errorf("%s/%s: empty label", k.sport, k.zone)
		}
		var prevTotal int
		for i, r := range l.Rungs {
			wantLevel := i + 1
			if r.Level != wantLevel {
				t.Errorf("%s/%s rung %d: Level=%d, want %d", k.sport, k.zone, i, r.Level, wantLevel)
			}
			if r.Reps < 1 {
				t.Errorf("%s/%s level %d: Reps=%d, want >=1", k.sport, k.zone, r.Level, r.Reps)
			}
			if r.WorkSeconds <= 0 {
				t.Errorf("%s/%s level %d: WorkSeconds=%d, want >0", k.sport, k.zone, r.Level, r.WorkSeconds)
			}
			if r.Reps == 1 && r.RestSeconds != 0 {
				t.Errorf("%s/%s level %d: single-rep rung has RestSeconds=%d, want 0", k.sport, k.zone, r.Level, r.RestSeconds)
			}
			if r.Reps >= 2 && r.RestSeconds <= 0 {
				t.Errorf("%s/%s level %d: multi-rep rung has RestSeconds=%d, want >0", k.sport, k.zone, r.Level, r.RestSeconds)
			}
			if r.LowPct <= 0 || r.HighPct <= r.LowPct {
				t.Errorf("%s/%s level %d: LowPct=%v HighPct=%v, want 0<Low<High", k.sport, k.zone, r.Level, r.LowPct, r.HighPct)
			}
			// Spec's own testing note: "non-decreasing total work time
			// (reps × work) up the ladder" — work only, rest excluded.
			total := r.Reps * r.WorkSeconds
			if total < prevTotal {
				t.Errorf("%s/%s level %d: total work time %ds is less than level %d's %ds (must be non-decreasing)",
					k.sport, k.zone, r.Level, total, r.Level-1, prevTotal)
			}
			prevTotal = total
		}
	}
}

func TestLadderForUnknownCombinationReturnsFalse(t *testing.T) {
	cases := []struct {
		sport model.Sport
		zone  string
	}{
		{model.SportCycling, "intervals"},
		{model.SportRunning, "vo2max"},
		{model.SportRunning, "sweet_spot"},
		{model.SportRunning, "anaerobic"},
		{model.SportCycling, "endurance"},
		{model.SportCycling, "nonsense"},
	}
	for _, c := range cases {
		if _, ok := LadderFor(c.sport, c.zone); ok {
			t.Errorf("LadderFor(%s, %s): got true, want false", c.sport, c.zone)
		}
	}
}

func mustLadder(t *testing.T, sport model.Sport, zone string) Ladder {
	t.Helper()
	l, ok := LadderFor(sport, zone)
	if !ok {
		t.Fatalf("LadderFor(%s, %s): not found", sport, zone)
	}
	return l
}

func TestPickClosestLevel(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "threshold")

	r, ok := Pick(l, 5.0, 1e9)
	if !ok || r.Level != 5 {
		t.Fatalf("Pick(5.0): got level %d ok=%v, want level 5", r.Level, ok)
	}

	r, ok = Pick(l, 5.4, 1e9)
	if !ok || r.Level != 5 {
		t.Fatalf("Pick(5.4): got level %d ok=%v, want level 5", r.Level, ok)
	}

	r, ok = Pick(l, 5.6, 1e9)
	if !ok || r.Level != 6 {
		t.Fatalf("Pick(5.6): got level %d ok=%v, want level 6", r.Level, ok)
	}
}

func TestPickTieBreaksToLowerLevel(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "threshold")

	// Exactly between two levels: tie goes to the lower one.
	r, ok := Pick(l, 5.5, 1e9)
	if !ok || r.Level != 5 {
		t.Fatalf("Pick(5.5): got level %d ok=%v, want level 5 (tie -> lower)", r.Level, ok)
	}
}

func TestPickClampsBeyondBothEnds(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "threshold")

	if r, ok := Pick(l, -5, 1e9); !ok || r.Level != 1 {
		t.Fatalf("Pick(-5): got level %d ok=%v, want level 1", r.Level, ok)
	}
	if r, ok := Pick(l, 50, 1e9); !ok || r.Level != 10 {
		t.Fatalf("Pick(50): got level %d ok=%v, want level 10", r.Level, ok)
	}
}

func TestPickSkipsRungsOverTimeBudget(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "threshold")

	// Target level 10, but only enough time for something much shorter:
	// must fall back to the highest level that still fits.
	budget := TotalSeconds(l.Rungs[2]) // level 3's own total, exactly
	r, ok := Pick(l, 10, budget)
	if !ok {
		t.Fatalf("Pick(10, tight budget): got ok=false, want a rung within budget")
	}
	if TotalSeconds(r) > budget {
		t.Fatalf("Pick returned level %d whose total %v exceeds budget %v", r.Level, TotalSeconds(r), budget)
	}
	if r.Level > 3 {
		t.Fatalf("Pick(10, budget=level 3's total): got level %d, want <=3", r.Level)
	}
}

func TestPickNoneFitsReturnsFalse(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "threshold")
	if _, ok := Pick(l, 5, 1); ok {
		t.Fatalf("Pick with a 1-second budget: got ok=true, want false (even the easiest rung needs the 20-minute warmup+cooldown)")
	}
}

func TestTotalSecondsIncludesWarmupAndCooldown(t *testing.T) {
	r := Rung{Level: 1, Reps: 3, WorkSeconds: 300, RestSeconds: 60}
	got := TotalSeconds(r)
	want := float64(600 + 600 + 3*(300+60))
	if got != want {
		t.Fatalf("TotalSeconds: got %v, want %v", got, want)
	}
}

func findStep(t *testing.T, steps []workout.WorkoutStep, name string) workout.WorkoutStep {
	t.Helper()
	for _, s := range steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step named %q among %d steps", name, len(steps))
	return workout.WorkoutStep{}
}

func TestInstantiateWithFTPUsesPowerTargets(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "threshold")
	r := l.Rungs[4] // level 5: 3x12'/5'
	profile := workout.RiderProfile{Rider: "alice", FTPWatts: 250}

	req := Instantiate(l, r, profile)

	if req.Sport != model.SportCycling {
		t.Errorf("Sport = %v, want cycling", req.Sport)
	}
	if req.Zone != workout.ZoneThreshold {
		t.Errorf("Zone = %v, want threshold", req.Zone)
	}
	if req.Level != 5 {
		t.Errorf("Level = %v, want 5", req.Level)
	}
	wantName := "Threshold 3×12"
	if req.Name != wantName {
		t.Errorf("Name = %q, want %q", req.Name, wantName)
	}

	warmup := findStep(t, req.Steps, "Warmup")
	if warmup.Target != workout.TargetPower {
		t.Errorf("warmup target = %v, want power", warmup.Target)
	}

	block := findStep(t, req.Steps, "Threshold")
	if block.Repeat != 3 {
		t.Fatalf("work block Repeat = %d, want 3", block.Repeat)
	}
	work := findStep(t, block.Steps, "Work")
	if work.Target != workout.TargetPower {
		t.Fatalf("work target = %v, want power", work.Target)
	}
	if work.TargetLow != 250*0.95 || work.TargetHigh != 250*1.05 {
		t.Errorf("work target = [%v, %v], want [%v, %v]", work.TargetLow, work.TargetHigh, 250*0.95, 250*1.05)
	}
	rest := findStep(t, block.Steps, "Recovery")
	if rest.Target != workout.TargetPower {
		t.Fatalf("rest target = %v, want power", rest.Target)
	}
	if rest.TargetLow != 250*0.50 || rest.TargetHigh != 250*0.50 {
		t.Errorf("rest target = [%v, %v], want a single value at 50%% FTP (%v)", rest.TargetLow, rest.TargetHigh, 250*0.50)
	}

	cooldown := findStep(t, req.Steps, "Cooldown")
	if cooldown.Target != workout.TargetPower {
		t.Errorf("cooldown target = %v, want power", cooldown.Target)
	}
	if cooldown.TargetLow != 250*0.45 || cooldown.TargetHigh != 250*0.55 {
		t.Errorf("cooldown target = [%v, %v], want [%v, %v]", cooldown.TargetLow, cooldown.TargetHigh, 250*0.45, 250*0.55)
	}
}

func TestInstantiateRunningWithThresholdPaceUsesPaceTargets(t *testing.T) {
	l := mustLadder(t, model.SportRunning, "intervals")
	r := l.Rungs[4] // level 5: 5x3'/2'
	profile := workout.RiderProfile{Rider: "bob", ThresholdPaceSecPerKM: 240}

	req := Instantiate(l, r, profile)

	threshold := 1000.0 / 240
	block := findStep(t, req.Steps, "Intervals")
	work := findStep(t, block.Steps, "Work")
	if work.Target != workout.TargetPace {
		t.Fatalf("work target = %v, want pace", work.Target)
	}
	if work.TargetLow >= work.TargetHigh {
		t.Fatalf("work target low (%v) should be < high (%v)", work.TargetLow, work.TargetHigh)
	}
	wantLow, wantHigh := threshold*1.06, threshold*1.15
	if work.TargetLow != wantLow || work.TargetHigh != wantHigh {
		t.Errorf("work target = [%v, %v], want [%v, %v]", work.TargetLow, work.TargetHigh, wantLow, wantHigh)
	}
}

func TestInstantiateHROnlyUsesZoneMappedHeartRateFractions(t *testing.T) {
	profile := workout.RiderProfile{Rider: "carol", MaxHR: 190}

	cases := []struct {
		zone              string
		wantLow, wantHigh float64
	}{
		{"tempo", 0.80, 0.88},
		{"sweet_spot", 0.80, 0.88},
		{"threshold", 0.88, 0.93},
		{"vo2max", 0.90, 0.97},
		{"anaerobic", 0.93, 1.00},
	}
	for _, c := range cases {
		l := mustLadder(t, model.SportCycling, c.zone)
		r := l.Rungs[0]
		req := Instantiate(l, r, profile)
		block := findStep(t, req.Steps, l.Label)
		var work workout.WorkoutStep
		if r.Reps >= 2 {
			work = findStep(t, block.Steps, "Work")
		} else {
			work = block
		}
		if work.Target != workout.TargetHeartRate {
			t.Fatalf("%s: work target = %v, want heart_rate", c.zone, work.Target)
		}
		wantLow, wantHigh := 190*c.wantLow, 190*c.wantHigh
		if work.TargetLow != wantLow || work.TargetHigh != wantHigh {
			t.Errorf("%s: work HR target = [%v, %v], want [%v, %v]", c.zone, work.TargetLow, work.TargetHigh, wantLow, wantHigh)
		}

		rest := findStep(t, req.Steps, "Warmup")
		if rest.Target != workout.TargetHeartRate {
			t.Fatalf("%s: warmup target = %v, want heart_rate", c.zone, rest.Target)
		}
		if rest.TargetLow != 190*0.50 {
			t.Errorf("%s: warmup low = %v, want %v (50%% max HR)", c.zone, rest.TargetLow, 190*0.50)
		}
	}
}

func TestInstantiateNoProfileDataUsesOpenTargets(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "tempo")
	r := l.Rungs[0]
	profile := workout.RiderProfile{Rider: "dave"}

	req := Instantiate(l, r, profile)
	for _, s := range req.Steps {
		steps := []workout.WorkoutStep{s}
		if s.Repeat >= 2 {
			steps = s.Steps
		}
		for _, leaf := range steps {
			if leaf.Target != workout.TargetOpen {
				t.Errorf("step %q: target = %v, want open", leaf.Name, leaf.Target)
			}
			if leaf.TargetLow != 0 || leaf.TargetHigh != 0 {
				t.Errorf("step %q: target range = [%v, %v], want [0, 0]", leaf.Name, leaf.TargetLow, leaf.TargetHigh)
			}
		}
	}
}

func TestInstantiateWarmupAndCooldownArePresent(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "vo2max")
	r := l.Rungs[3]
	profile := workout.RiderProfile{Rider: "erin", FTPWatts: 200}

	req := Instantiate(l, r, profile)

	var warmups []workout.WorkoutStep
	for _, s := range req.Steps {
		if s.Intensity == workout.IntensityWarmup {
			warmups = append(warmups, s)
		}
	}
	if len(warmups) != 2 {
		t.Fatalf("got %d warmup steps, want 2 (two 5-minute ramp steps)", len(warmups))
	}
	var totalWarmup float64
	for _, w := range warmups {
		totalWarmup += w.Seconds
	}
	if totalWarmup != 600 {
		t.Errorf("total warmup seconds = %v, want 600", totalWarmup)
	}
	if warmups[0].TargetLow >= warmups[1].TargetLow {
		t.Errorf("warmup does not ramp up: step1 low %v, step2 low %v", warmups[0].TargetLow, warmups[1].TargetLow)
	}

	cooldown := findStep(t, req.Steps, "Cooldown")
	if cooldown.Intensity != workout.IntensityCooldown {
		t.Errorf("cooldown intensity = %v, want cooldown", cooldown.Intensity)
	}
	if cooldown.Seconds != 600 {
		t.Errorf("cooldown seconds = %v, want 600", cooldown.Seconds)
	}
}

func TestInstantiateSingleRepRungIsPlainStepNotRepeatBlock(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "tempo")
	r := l.Rungs[7] // level 8: 1x60'
	if r.Reps != 1 {
		t.Fatalf("test assumption wrong: level 8 tempo has Reps=%d, want 1", r.Reps)
	}
	profile := workout.RiderProfile{Rider: "frank", FTPWatts: 220}

	req := Instantiate(l, r, profile)
	work := findStep(t, req.Steps, "Tempo")
	if work.Repeat >= 2 {
		t.Fatalf("single-rep rung produced a repeat block (Repeat=%d), want a plain step", work.Repeat)
	}
	if work.Seconds != 3600 {
		t.Errorf("work seconds = %v, want 3600 (60 minutes)", work.Seconds)
	}
	wantName := "Tempo 1×60"
	if req.Name != wantName {
		t.Errorf("Name = %q, want %q", req.Name, wantName)
	}
}

func TestRungNameFormatting(t *testing.T) {
	cases := []struct {
		label string
		r     Rung
		want  string
	}{
		{"Threshold", Rung{Reps: 3, WorkSeconds: 12 * 60}, "Threshold 3×12"},
		{"VO2max", Rung{Reps: 5, WorkSeconds: 4 * 60}, "VO2max 5×4"},
		{"Anaerobic", Rung{Reps: 6, WorkSeconds: 30}, "Anaerobic 6×30s"},
		{"Anaerobic", Rung{Reps: 6, WorkSeconds: 90}, "Anaerobic 6×90s"},
		{"Tempo run", Rung{Reps: 2, WorkSeconds: 20 * 60}, "Tempo run 2×20"},
		{"Tempo", Rung{Reps: 1, WorkSeconds: 60 * 60}, "Tempo 1×60"},
	}
	for _, c := range cases {
		got := rungName(c.label, c.r)
		if got != c.want {
			t.Errorf("rungName(%q, %+v) = %q, want %q", c.label, c.r, got, c.want)
		}
	}
}

// TestEveryRungEncodesToFIT proves every rung of every ladder — instantiated
// with a full profile so every target type actually gets non-open values —
// produces steps internal/fitworkout.Encode accepts without error. This is
// the FIT round trip the brief and spec both call for: the same encoder the
// real download/push paths use, not a hand-rolled check of this package's
// own step shapes.
func TestEveryRungEncodesToFIT(t *testing.T) {
	profiles := map[model.Sport]workout.RiderProfile{
		model.SportCycling: {Rider: "r", FTPWatts: 250},
		model.SportRunning: {Rider: "r", ThresholdPaceSecPerKM: 240},
	}
	for _, k := range allLadderKeys() {
		l := mustLadder(t, k.sport, k.zone)
		profile := profiles[k.sport]
		for _, r := range l.Rungs {
			req := Instantiate(l, r, profile)
			fitSteps := workout.FITSteps(req.Steps)
			_, err := fitworkout.Encode(fitSteps, fitworkout.Options{
				Name:  req.Name,
				Sport: fitworkout.SportFromString(string(req.Sport)),
			})
			if err != nil {
				t.Fatalf("%s/%s level %d (%q): fitworkout.Encode failed: %v", k.sport, k.zone, r.Level, req.Name, err)
			}
		}
	}
}

func TestLabelsMatchSpec(t *testing.T) {
	want := map[string]string{
		"cycling/tempo":      "Tempo",
		"cycling/sweet_spot": "Sweet spot",
		"cycling/threshold":  "Threshold",
		"cycling/vo2max":     "VO2max",
		"cycling/anaerobic":  "Anaerobic",
		"running/tempo":      "Tempo run",
		"running/threshold":  "Threshold run",
		"running/intervals":  "Intervals",
	}
	for _, k := range allLadderKeys() {
		l := mustLadder(t, k.sport, k.zone)
		key := string(k.sport) + "/" + k.zone
		if l.Label != want[key] {
			t.Errorf("%s label = %q, want %q", key, l.Label, want[key])
		}
	}
}

func TestPercentRangesMatchSpec(t *testing.T) {
	want := map[string][2]float64{
		"cycling/tempo":      {0.76, 0.87},
		"cycling/sweet_spot": {0.88, 0.94},
		"cycling/threshold":  {0.95, 1.05},
		"cycling/vo2max":     {1.06, 1.20},
		"cycling/anaerobic":  {1.21, 1.50},
		"running/tempo":      {0.88, 0.95},
		"running/threshold":  {0.96, 1.03},
		"running/intervals":  {1.06, 1.15},
	}
	for _, k := range allLadderKeys() {
		l := mustLadder(t, k.sport, k.zone)
		key := string(k.sport) + "/" + k.zone
		for _, r := range l.Rungs {
			if r.LowPct != want[key][0] || r.HighPct != want[key][1] {
				t.Errorf("%s level %d: pct = [%v, %v], want %v", key, r.Level, r.LowPct, r.HighPct, want[key])
			}
		}
	}
}

func TestSpecExampleRungsMatchTable(t *testing.T) {
	// A sample of exact rows straight out of the spec's table, spot-checked
	// so a transcription slip anywhere shows up here.
	l := mustLadder(t, model.SportCycling, "vo2max")
	if got := l.Rungs[4]; got.Reps != 5 || got.WorkSeconds != 4*60 || got.RestSeconds != 4*60 {
		t.Errorf("vo2max level 5 = %+v, want 5x4'/4'", got)
	}
	l = mustLadder(t, model.SportCycling, "anaerobic")
	if got := l.Rungs[0]; got.Reps != 6 || got.WorkSeconds != 30 || got.RestSeconds != 3*60 {
		t.Errorf("anaerobic level 1 = %+v, want 6x30\"/3'", got)
	}
	l = mustLadder(t, model.SportRunning, "intervals")
	if got := l.Rungs[2]; got.Reps != 6 || got.WorkSeconds != 2*60 || got.RestSeconds != 90 {
		t.Errorf("intervals(run) level 3 = %+v, want 6x2'/1'30\"", got)
	}
	l = mustLadder(t, model.SportRunning, "tempo")
	if got := l.Rungs[5]; got.Reps != 1 || got.WorkSeconds != 40*60 {
		t.Errorf("tempo(run) level 6 = %+v, want 1x40'", got)
	}
}

func TestInstantiateNameContainsMultiplicationSign(t *testing.T) {
	l := mustLadder(t, model.SportCycling, "sweet_spot")
	r := l.Rungs[1]
	req := Instantiate(l, r, workout.RiderProfile{})
	if !strings.Contains(req.Name, "×") {
		t.Errorf("Name %q does not contain ×", req.Name)
	}
}

// --- Friel %LTHR heart-rate targets (threshold HR known) ---

func approx(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

func TestHRRangeUsesFrielTableWhenThresholdHRKnown(t *testing.T) {
	cases := []struct {
		sport             model.Sport
		zone              string
		wantLow, wantHigh float64
	}{
		{model.SportCycling, "", 0.70, 0.81},
		{model.SportCycling, "endurance", 0.81, 0.89},
		{model.SportCycling, "tempo", 0.90, 0.93},
		{model.SportCycling, "sweet_spot", 0.92, 0.96},
		{model.SportCycling, "threshold", 0.94, 0.99},
		{model.SportCycling, "vo2max", 1.03, 1.06},
		{model.SportCycling, "intervals", 1.03, 1.06},
		{model.SportCycling, "anaerobic", 1.07, 1.10},
		{model.SportRunning, "", 0.72, 0.85},
		{model.SportRunning, "endurance", 0.85, 0.89},
		{model.SportRunning, "tempo", 0.90, 0.94},
		{model.SportRunning, "sweet_spot", 0.92, 0.96},
		{model.SportRunning, "threshold", 0.95, 0.99},
		{model.SportRunning, "vo2max", 1.03, 1.06},
		{model.SportRunning, "intervals", 1.03, 1.06},
		{model.SportRunning, "anaerobic", 1.07, 1.10},
	}
	profile := workout.RiderProfile{ThresholdHR: 160}
	for _, c := range cases {
		low, high, ok := HRRange(c.sport, profile, c.zone)
		if !ok || !approx(low, 160*c.wantLow) || !approx(high, 160*c.wantHigh) {
			t.Errorf("%s %q: got [%v, %v] ok=%v, want [%v, %v]", c.sport, c.zone, low, high, ok, 160*c.wantLow, 160*c.wantHigh)
		}
	}
}

func TestHRRangeCapsAtMaxHR(t *testing.T) {
	// LTHR 170 x 1.07..1.10 = 181.9..187 -> high capped at 185.
	low, high, _ := HRRange(model.SportCycling, workout.RiderProfile{ThresholdHR: 170, MaxHR: 185}, "anaerobic")
	if !approx(low, 181.9) || high != 185 {
		t.Errorf("got [%v, %v], want [181.9, 185]", low, high)
	}
	// Both ends above the ceiling: low and high each capped.
	low, high, _ = HRRange(model.SportCycling, workout.RiderProfile{ThresholdHR: 175, MaxHR: 185}, "anaerobic")
	if low != 185 || high != 185 {
		t.Errorf("got [%v, %v], want [185, 185]", low, high)
	}
	// A max HR below LTHR is stale or mistyped: skip the cap, keep Friel's targets.
	low, high, _ = HRRange(model.SportCycling, workout.RiderProfile{ThresholdHR: 170, MaxHR: 165}, "tempo")
	if !approx(low, 170*0.90) || !approx(high, 170*0.93) {
		t.Errorf("MaxHR < LTHR: got [%v, %v], want uncapped [%v, %v]", low, high, 170*0.90, 170*0.93)
	}
	// Below the ceiling nothing changes.
	low, high, _ = HRRange(model.SportCycling, workout.RiderProfile{ThresholdHR: 150, MaxHR: 190}, "tempo")
	if !approx(low, 135) || !approx(high, 139.5) {
		t.Errorf("got [%v, %v], want [135, 139.5]", low, high)
	}
}

func TestHRRangeWithOnlyMaxHRKeepsTodaysNumbers(t *testing.T) {
	profile := workout.RiderProfile{MaxHR: 200}
	for _, c := range []struct {
		zone              string
		wantLow, wantHigh float64
	}{
		{"", 0.50, 0.60},
		{"endurance", 0.60, 0.75},
		{"tempo", 0.80, 0.88},
		{"sweet_spot", 0.80, 0.88},
		{"threshold", 0.88, 0.93},
		{"vo2max", 0.90, 0.97},
		{"intervals", 0.90, 0.97},
		{"anaerobic", 0.93, 1.00},
	} {
		low, high, ok := HRRange(model.SportCycling, profile, c.zone)
		if !ok || !approx(low, 200*c.wantLow) || !approx(high, 200*c.wantHigh) {
			t.Errorf("%q: got [%v, %v] ok=%v, want [%v, %v]", c.zone, low, high, ok, 200*c.wantLow, 200*c.wantHigh)
		}
	}
	if _, _, ok := HRRange(model.SportCycling, workout.RiderProfile{}, "tempo"); ok {
		t.Error("no MaxHR and no ThresholdHR should report ok=false")
	}
}

func TestInstantiateUsesFrielTargetsForThresholdHR(t *testing.T) {
	profile := workout.RiderProfile{Rider: "carol", ThresholdHR: 160, MaxHR: 190}
	l := mustLadder(t, model.SportCycling, "threshold")
	r := l.Rungs[0]
	req := Instantiate(l, r, profile)

	warm := findStep(t, req.Steps, "Warmup")
	if warm.Target != workout.TargetHeartRate || !approx(warm.TargetLow, 160*0.70) || !approx(warm.TargetHigh, 160*0.81) {
		t.Errorf("warmup = %v [%v, %v], want heart_rate 0.70-0.81 x LTHR", warm.Target, warm.TargetLow, warm.TargetHigh)
	}
	cool := findStep(t, req.Steps, "Cooldown")
	if cool.Target != workout.TargetHeartRate || !approx(cool.TargetLow, 160*0.70) || !approx(cool.TargetHigh, 160*0.81) {
		t.Errorf("cooldown = %v [%v, %v]", cool.Target, cool.TargetLow, cool.TargetHigh)
	}
	block := findStep(t, req.Steps, l.Label)
	work := block
	if r.Reps >= 2 {
		work = findStep(t, block.Steps, "Work")
		rest := findStep(t, block.Steps, "Recovery")
		if rest.Target != workout.TargetHeartRate || !approx(rest.TargetLow, 160*0.70) || !approx(rest.TargetHigh, 160*0.81) {
			t.Errorf("rest = %v [%v, %v], want the easy zone off LTHR", rest.Target, rest.TargetLow, rest.TargetHigh)
		}
	}
	if work.Target != workout.TargetHeartRate || !approx(work.TargetLow, 160*0.94) || !approx(work.TargetHigh, 160*0.99) {
		t.Errorf("work = %v [%v, %v], want heart_rate 0.94-0.99 x LTHR", work.Target, work.TargetLow, work.TargetHigh)
	}
}

func TestFTPAndPaceBranchesIgnoreThresholdHR(t *testing.T) {
	ftp := workout.RiderProfile{FTPWatts: 250, ThresholdHR: 160, MaxHR: 190}
	if tt, low, high := target(model.SportCycling, ftp, 0.5, 0.6, "tempo"); tt != workout.TargetPower || low != 125 || high != 150 {
		t.Errorf("cycling with FTP = %v [%v, %v], want power 125-150", tt, low, high)
	}
	pace := workout.RiderProfile{ThresholdPaceSecPerKM: 300, ThresholdHR: 160}
	if tt, _, _ := target(model.SportRunning, pace, 0.8, 0.9, "tempo"); tt != workout.TargetPace {
		t.Errorf("running with a pace = %v, want pace", tt)
	}
}
