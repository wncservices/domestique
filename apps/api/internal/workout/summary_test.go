package workout

import "testing"

func timeStep(intensity Intensity, seconds float64, target TargetType, lo, hi float64) WorkoutStep {
	return WorkoutStep{Intensity: intensity, Duration: DurationTime, Seconds: seconds, Target: target, TargetLow: lo, TargetHigh: hi}
}

func TestSummary(t *testing.T) {
	warm := timeStep(IntensityWarmup, 600, TargetPower, 120, 140)
	cool := timeStep(IntensityCooldown, 600, TargetPower, 100, 120)

	cases := []struct {
		name  string
		steps []WorkoutStep
		want  string
	}{
		{"repeat block", []WorkoutStep{warm, {
			Repeat: 3, Name: "Intervals",
			Steps: []WorkoutStep{
				timeStep(IntensityInterval, 600, TargetPower, 250, 260),
				timeStep(IntensityRecovery, 300, TargetPower, 100, 120),
			},
		}, cool}, "3 x 10 min at 250-260 W"},
		{"single power range", []WorkoutStep{warm, timeStep(IntensityActive, 3600, TargetPower, 180, 200), cool}, "60 min at 180-200 W"},
		{"single power value", []WorkoutStep{timeStep(IntensityActive, 3600, TargetPower, 200, 200)}, "60 min at 200 W"},
		{"heart rate range", []WorkoutStep{timeStep(IntensityActive, 2700, TargetHeartRate, 140, 150)}, "45 min at 140-150 bpm"},
		{"cadence", []WorkoutStep{timeStep(IntensityActive, 600, TargetCadence, 90, 100)}, "10 min at 90-100 rpm"},
		{"pace is shown per kilometre, fast end first", []WorkoutStep{timeStep(IntensityActive, 1800, TargetPace, 3.0, 3.5)}, "30 min at 4:46-5:33 /km"},
		{"open step", []WorkoutStep{{Intensity: IntensityActive, Duration: DurationOpen, Target: TargetOpen}}, "open effort"},
		{"time step without a target", []WorkoutStep{timeStep(IntensityActive, 3600, TargetOpen, 0, 0)}, "60 min, open effort"},
		{"open duration with a target", []WorkoutStep{{Intensity: IntensityActive, Duration: DurationOpen, Target: TargetPower, TargetLow: 200, TargetHigh: 210}}, "open at 200-210 W"},
		{"distance step", []WorkoutStep{{Intensity: IntensityActive, Duration: DurationDistance, Meters: 20000, Target: TargetPower, TargetLow: 180, TargetHigh: 200}}, "20 km at 180-200 W"},
		{"short distance", []WorkoutStep{{Intensity: IntensityActive, Duration: DurationDistance, Meters: 400, Target: TargetOpen}}, "400 m, open effort"},
		{"seconds that are not whole minutes", []WorkoutStep{{
			Repeat: 8, Steps: []WorkoutStep{timeStep(IntensityInterval, 150, TargetPower, 300, 320), timeStep(IntensityRecovery, 150, TargetOpen, 0, 0)},
		}}, "8 x 2 min 30 s at 300-320 W"},
		{"under a minute", []WorkoutStep{{
			Repeat: 6, Steps: []WorkoutStep{timeStep(IntensityInterval, 30, TargetPower, 500, 600), timeStep(IntensityRecovery, 90, TargetOpen, 0, 0)},
		}}, "6 x 30 s at 500-600 W"},
		{"a long step beats a short block", []WorkoutStep{
			timeStep(IntensityActive, 3600, TargetPower, 150, 170),
			{Repeat: 5, Steps: []WorkoutStep{timeStep(IntensityInterval, 30, TargetPower, 500, 600), timeStep(IntensityRecovery, 60, TargetOpen, 0, 0)}},
		}, "60 min at 150-170 W"},
		{"no active step", []WorkoutStep{warm, cool}, "open effort"},
		{"no steps at all", nil, "open effort"},
	}
	for _, c := range cases {
		if got := Summary(c.steps); got != c.want {
			t.Errorf("%s: Summary = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDurationLabel(t *testing.T) {
	cases := map[float64]string{0: "", 2700: "45 min", 3600: "1h00", 3900: "1h05", 7500: "2h05", 3630: "1h01"}
	for in, want := range cases {
		if got := DurationLabel(in); got != want {
			t.Errorf("DurationLabel(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestZoneLabel(t *testing.T) {
	cases := map[Zone]string{ZoneSweetSpot: "Sweet spot", ZoneVO2Max: "VO2 max", ZoneThreshold: "Threshold", ZoneEndurance: "Endurance", "": ""}
	for in, want := range cases {
		if got := ZoneLabel(in); got != want {
			t.Errorf("ZoneLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
