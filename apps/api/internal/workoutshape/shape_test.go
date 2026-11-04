package workoutshape

import (
	"regexp"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// sweetSpot is a 10 minute warmup, two 20 minute blocks at 90-95% of FTP,
// and a cooldown, built for the given FTP so every watt target is the same
// fraction of it.
func sweetSpot(ftp float64) workout.Workout {
	return workout.Workout{
		Sport: model.SportCycling,
		Name:  "Sweet spot",
		Steps: []workout.WorkoutStep{
			{Name: "Warm up", Intensity: workout.IntensityWarmup, Duration: workout.DurationTime,
				Seconds: 600, Target: workout.TargetPower, TargetLow: 0.5 * ftp, TargetHigh: 0.6 * ftp},
			{Repeat: 2, Name: "Blocks", Steps: []workout.WorkoutStep{
				{Name: "Work", Intensity: workout.IntensityActive, Duration: workout.DurationTime,
					Seconds: 1200, Target: workout.TargetPower, TargetLow: 0.9 * ftp, TargetHigh: 0.95 * ftp},
				{Name: "Rest", Intensity: workout.IntensityRecovery, Duration: workout.DurationTime,
					Seconds: 300, Target: workout.TargetPower, TargetLow: 0.5 * ftp, TargetHigh: 0.55 * ftp},
			}},
			{Name: "Cool down", Intensity: workout.IntensityCooldown, Duration: workout.DurationTime,
				Seconds: 600, Target: workout.TargetOpen},
		},
	}
}

var wattsLike = regexp.MustCompile(`\d\s?W\b`)

func TestShapeHasNoWatts(t *testing.T) {
	text := Text(sweetSpot(250), 250)

	for _, forbidden := range []string{"250", "225", "238", "213", "150", "140"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("shape text holds %q:\n%s", forbidden, text)
		}
	}
	if wattsLike.MatchString(text) {
		t.Errorf("shape text holds a watt figure:\n%s", text)
	}
	if !strings.Contains(text, "%") {
		t.Errorf("shape text has no %%FTP range:\n%s", text)
	}
	if !strings.Contains(text, "Sweet spot") {
		t.Errorf("shape text lost the session name:\n%s", text)
	}
}

func TestSameSessionDifferentFTPSameKey(t *testing.T) {
	a := Key(Text(sweetSpot(200), 200))
	b := Key(Text(sweetSpot(300), 300))
	if a != b {
		t.Errorf("same session at FTP 200 and 300 gave different keys:\n%s\n%s",
			Text(sweetSpot(200), 200), Text(sweetSpot(300), 300))
	}
	if len(a) != 64 {
		t.Errorf("key is %d characters, want 64 hex", len(a))
	}
}

func TestEasedSessionHasItsOwnKey(t *testing.T) {
	generated := sweetSpot(250)
	eased := sweetSpot(250)
	// Readiness eases the work blocks to the rung below.
	eased.Steps[1].Steps[0].TargetLow = 0.75 * 250
	eased.Steps[1].Steps[0].TargetHigh = 0.8 * 250

	if Key(Text(generated, 250)) == Key(Text(eased, 250)) {
		t.Errorf("an eased session shares the generated session's key:\n%s", Text(eased, 250))
	}
}

func TestRepeatBlocksNest(t *testing.T) {
	text := Text(sweetSpot(250), 250)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")

	var repeatIdx = -1
	for i, l := range lines {
		if strings.Contains(l, "repeat 2x") {
			repeatIdx = i
		}
	}
	if repeatIdx < 0 {
		t.Fatalf("no repeat line in:\n%s", text)
	}
	if repeatIdx+2 >= len(lines) {
		t.Fatalf("repeat block has no children in:\n%s", text)
	}
	for _, child := range lines[repeatIdx+1 : repeatIdx+3] {
		if !strings.HasPrefix(child, "  ") {
			t.Errorf("repeat child is not nested: %q\n%s", child, text)
		}
	}
	if strings.HasPrefix(lines[repeatIdx+3], "  ") {
		t.Errorf("line after the repeat block is still nested:\n%s", text)
	}
}

func TestNoFTPWritesPowerWithoutNumber(t *testing.T) {
	text := Text(sweetSpot(250), 0)
	if !strings.Contains(text, "power") {
		t.Errorf("no-FTP shape does not name power:\n%s", text)
	}
	if strings.Contains(text, "%") {
		t.Errorf("no-FTP shape writes a percentage:\n%s", text)
	}
	if strings.Contains(text, "250") {
		t.Errorf("no-FTP shape holds the watt figure:\n%s", text)
	}
}

func TestHeartRateAndPaceHaveNoNumbers(t *testing.T) {
	w := workout.Workout{
		Sport: model.SportRunning,
		Name:  "Tempo run",
		Steps: []workout.WorkoutStep{
			{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 1500,
				Target: workout.TargetHeartRate, TargetLow: 150, TargetHigh: 160},
			{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 600,
				Target: workout.TargetPace, TargetLow: 3.5, TargetHigh: 3.7},
		},
	}
	text := Text(w, 250)
	if !strings.Contains(text, "heart rate") || !strings.Contains(text, "pace") {
		t.Errorf("heart rate or pace not named:\n%s", text)
	}
	for _, forbidden := range []string{"150", "160", "3.5", "3.7"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("shape text holds %q:\n%s", forbidden, text)
		}
	}
}

func TestDistanceSteps(t *testing.T) {
	w := workout.Workout{
		Sport: model.SportRunning,
		Name:  "Strides",
		Steps: []workout.WorkoutStep{
			{Intensity: workout.IntensityActive, Duration: workout.DurationDistance, Meters: 800,
				Target: workout.TargetOpen},
		},
	}
	text := Text(w, 0)
	if !strings.Contains(text, "800m") {
		t.Errorf("distance step not written in metres:\n%s", text)
	}
}
