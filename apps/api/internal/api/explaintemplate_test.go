package api

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutshape"
)

// The template is what a rider reads when the model is off or has not
// answered. Every zone, and an FTP test, must have its own text.
func TestTemplateCoversEveryZoneAndTheTest(t *testing.T) {
	cases := map[string]workout.Workout{
		"endurance":  {Sport: model.SportCycling, Zone: workout.ZoneEndurance, Name: "Long ride"},
		"tempo":      {Sport: model.SportCycling, Zone: workout.ZoneTempo},
		"sweet_spot": {Sport: model.SportCycling, Zone: workout.ZoneSweetSpot},
		"threshold":  {Sport: model.SportCycling, Zone: workout.ZoneThreshold},
		"vo2max":     {Sport: model.SportCycling, Zone: workout.ZoneVO2Max},
		"anaerobic":  {Sport: model.SportCycling, Zone: workout.ZoneAnaerobic},
		"intervals":  {Sport: model.SportCycling, Zone: workout.ZoneIntervals},
		"unset":      {Sport: model.SportCycling},
		"recovery":   {Sport: model.SportCycling, Zone: workout.ZoneEndurance, Name: "Recovery ride"},
		"ftp test":   {Sport: model.SportCycling, Zone: workout.ZoneThreshold, TestProtocol: "ramp"},
	}
	seen := map[string]string{}
	for name, w := range cases {
		shape := workoutshape.Text(w, 250)
		got := templateExplanation(w, shape)
		if strings.TrimSpace(got.Summary) == "" {
			t.Errorf("%s: empty summary", name)
		}
		if n := utf8.RuneCountInString(got.Summary); n > 400 {
			t.Errorf("%s: summary is %d runes, want at most 400", name, n)
		}
		if strings.ContainsAny(got.Summary, "0123456789") {
			t.Errorf("%s: summary states a number; the template text is fixed", name)
		}
		if other, dup := seen[got.Summary]; dup {
			t.Errorf("%s and %s share a summary", name, other)
		}
		seen[got.Summary] = name
	}
}

func TestTemplateTestProtocolOverridesZone(t *testing.T) {
	plain := templateExplanation(workout.Workout{Zone: workout.ZoneThreshold}, "")
	test := templateExplanation(workout.Workout{Zone: workout.ZoneThreshold, TestProtocol: "twenty_minute"}, "")
	if plain.Summary == test.Summary {
		t.Error("an FTP test reads the same as a threshold session")
	}
}

func TestTemplateBlocksFollowTheShapeSteps(t *testing.T) {
	w := workout.Workout{
		Sport: model.SportCycling,
		Zone:  workout.ZoneSweetSpot,
		Steps: []workout.WorkoutStep{
			{Intensity: workout.IntensityWarmup, Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetPower, TargetLow: 125, TargetHigh: 137.5},
			{Repeat: 3, Steps: []workout.WorkoutStep{
				{Intensity: workout.IntensityInterval, Duration: workout.DurationTime, Seconds: 480, Target: workout.TargetPower, TargetLow: 215, TargetHigh: 240},
				{Intensity: workout.IntensityRecovery, Duration: workout.DurationTime, Seconds: 240, Target: workout.TargetPower, TargetLow: 125, TargetHigh: 137.5},
			}},
			{Intensity: workout.IntensityCooldown, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
		},
	}
	got := templateExplanation(w, workoutshape.Text(w, 250))

	wantLabels := []string{"Warm-up", "Repeat 3x", "Work", "Recovery", "Cool-down"}
	if len(got.Blocks) != len(wantLabels) {
		t.Fatalf("blocks = %+v, want %d", got.Blocks, len(wantLabels))
	}
	for i, want := range wantLabels {
		if got.Blocks[i].Label != want {
			t.Errorf("block %d label = %q, want %q", i, got.Blocks[i].Label, want)
		}
	}
	if !strings.Contains(got.Blocks[2].Cue, "85-95% FTP") {
		t.Errorf("work cue = %q, want the shape's target", got.Blocks[2].Cue)
	}
}

func TestTemplateBlocksAreNeverNil(t *testing.T) {
	got := templateExplanation(workout.Workout{Sport: model.SportCycling, Zone: workout.ZoneEndurance}, "sport cycling\nzone endurance\n")
	if got.Blocks == nil || len(got.Blocks) != 0 {
		t.Fatalf("blocks = %#v, want an empty non-nil slice", got.Blocks)
	}
}

// Recovery is a name given to an easy ride; a hard session that happens to be
// called recovery keeps its zone's text.
func TestRecoveryTextOnlyForEasyZones(t *testing.T) {
	hard := templateExplanation(workout.Workout{Sport: model.SportCycling, Zone: workout.ZoneThreshold, Name: "Recovery intervals"}, "")
	if hard.Summary != explainTemplateSummaries[workout.ZoneThreshold] {
		t.Errorf("threshold session named recovery got %q", hard.Summary)
	}
	unset := templateExplanation(workout.Workout{Sport: model.SportCycling, Name: "Recovery spin"}, "")
	if unset.Summary != explainTemplateRecovery {
		t.Errorf("unset-zone recovery got %q", unset.Summary)
	}
}

func TestTemplateBlocksAreCappedAtEight(t *testing.T) {
	var b strings.Builder
	b.WriteString("sport cycling\nzone endurance\n")
	for i := 0; i < 12; i++ {
		b.WriteString("work 5min 60% FTP\n")
	}
	blocks := templateExplanation(workout.Workout{Zone: workout.ZoneEndurance}, b.String()).Blocks
	if len(blocks) != 8 {
		t.Fatalf("blocks = %d, want 8", len(blocks))
	}
	if blocks[7].Label != "…and 5 more steps" {
		t.Errorf("last block = %+v", blocks[7])
	}
	// Exactly eight steps are all shown.
	var e strings.Builder
	e.WriteString("sport cycling\nzone endurance\n")
	for i := 0; i < 8; i++ {
		e.WriteString("work 5min 60% FTP\n")
	}
	exact := templateExplanation(workout.Workout{Zone: workout.ZoneEndurance}, e.String()).Blocks
	if len(exact) != 8 || strings.Contains(exact[7].Label, "more") {
		t.Errorf("eight steps collapsed: %+v", exact)
	}
}
