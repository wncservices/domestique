package workoutshape

import (
	"math"
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
		Zone:  workout.ZoneSweetSpot,
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
	if !strings.Contains(text, "zone sweet_spot") {
		t.Errorf("shape text lost the zone:\n%s", text)
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

func TestShapeIgnoresTheName(t *testing.T) {
	a := sweetSpot(250)
	a.Name = "Sweet spot 3x8"
	b := sweetSpot(250)
	b.Name = "Ride to mum's 250W"

	textA, textB := Text(a, 250), Text(b, 250)
	if textA != textB {
		t.Errorf("name changed the shape:\n%s\n---\n%s", textA, textB)
	}
	if Key(textA) != Key(textB) {
		t.Errorf("name changed the key")
	}
	for _, name := range []string{"Sweet spot 3x8", "Ride to mum's 250W", "mum", "250"} {
		if strings.Contains(textB, name) {
			t.Errorf("shape text holds %q:\n%s", name, textB)
		}
	}
}

func TestUnsetZoneReadsUnset(t *testing.T) {
	w := sweetSpot(250)
	w.Zone = ""
	if !strings.Contains(Text(w, 250), "zone unset") {
		t.Errorf("empty zone not written as unset:\n%s", Text(w, 250))
	}
}

func TestUnknownZoneAndSportAreNotWritten(t *testing.T) {
	clean := sweetSpot(250)
	clean.Zone = ""
	clean.Sport = "junk"

	hostile := sweetSpot(250)
	hostile.Zone = "x 250W\nwarmup 99min open"
	hostile.Sport = "junk 250\nwork 1min open"

	cleanText, hostileText := Text(clean, 250), Text(hostile, 250)
	if hostileText != cleanText {
		t.Errorf("unknown zone or sport changed the shape:\n%s\n---\n%s", hostileText, cleanText)
	}
	for _, bad := range []string{"250", "junk", "x ", "99min"} {
		if strings.Contains(hostileText, bad) {
			t.Errorf("shape text holds %q:\n%s", bad, hostileText)
		}
	}
}

func TestKnownZoneIsWritten(t *testing.T) {
	w := sweetSpot(250)
	w.Zone = workout.ZoneVO2Max
	if !strings.Contains(Text(w, 250), "zone vo2max") {
		t.Errorf("known zone not written:\n%s", Text(w, 250))
	}
}

func TestStepWithChildrenKeepsThemWithoutRepeat(t *testing.T) {
	block := func(seconds float64) workout.Workout {
		return workout.Workout{Sport: model.SportCycling, Steps: []workout.WorkoutStep{
			{Repeat: 1, Name: "Block", Steps: []workout.WorkoutStep{
				{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: seconds,
					Target: workout.TargetOpen},
			}},
		}}
	}
	a, b := Text(block(600), 250), Text(block(900), 250)
	if !strings.Contains(a, "work 10min open") {
		t.Errorf("children of a Repeat<=1 block are missing:\n%s", a)
	}
	if Key(a) == Key(b) {
		t.Errorf("different children share a key:\n%s\n---\n%s", a, b)
	}
}

func TestNonFiniteFTPWritesPowerWithoutNumber(t *testing.T) {
	for _, ftp := range []float64{math.NaN(), math.Inf(1), -1} {
		text := Text(sweetSpot(250), ftp)
		if strings.Contains(text, "%") || strings.Contains(text, "NaN") || strings.Contains(text, "Inf") {
			t.Errorf("ftp %v writes a number:\n%s", ftp, text)
		}
		if !strings.Contains(text, "power") {
			t.Errorf("ftp %v does not name power:\n%s", ftp, text)
		}
	}
}

// Riders at odd FTPs get whole-watt targets. Their percentages land on the
// same 5 % bands as at round FTPs, so one explanation is shared.
func TestIntegerRoundedWattsAtOddFTPs(t *testing.T) {
	build := func(ftp float64) workout.Workout {
		w := workout.Workout{Sport: model.SportCycling, Zone: workout.ZoneSweetSpot}
		for _, pct := range []float64{0.55, 0.75, 0.88, 0.95} {
			w.Steps = append(w.Steps, workout.WorkoutStep{
				Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 300,
				Target:    workout.TargetPower,
				TargetLow: math.Round(pct * ftp), TargetHigh: math.Round(pct * ftp),
			})
		}
		return w
	}
	ref := Key(Text(build(250), 250))
	for _, ftp := range []float64{237, 251} {
		if got := Key(Text(build(ftp), ftp)); got != ref {
			t.Errorf("FTP %v gives a different shape:\n%s\n---\n%s", ftp, Text(build(ftp), ftp), Text(build(250), 250))
		}
	}
}
