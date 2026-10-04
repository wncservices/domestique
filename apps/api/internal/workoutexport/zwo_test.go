package workoutexport

import (
	"encoding/xml"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/name; -update rewrites it. The goldens
// were read by hand against the Zwift tag reference when they were written.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create it)", name, err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from golden.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func pw(name string, in workout.Intensity, secs, lo, hi float64) workout.WorkoutStep {
	return workout.WorkoutStep{Name: name, Intensity: in, Duration: workout.DurationTime, Seconds: secs,
		Target: workout.TargetPower, TargetLow: lo, TargetHigh: hi}
}

func steady(name string, secs, watts float64) workout.WorkoutStep {
	return pw(name, workout.IntensityActive, secs, watts, watts)
}

func open(name string, secs float64) workout.WorkoutStep {
	return workout.WorkoutStep{Name: name, Intensity: workout.IntensityActive, Duration: workout.DurationTime,
		Seconds: secs, Target: workout.TargetOpen}
}

func repeat(n int, name string, kids ...workout.WorkoutStep) workout.WorkoutStep {
	return workout.WorkoutStep{Name: name, Repeat: n, Steps: kids}
}

var meta = Meta{Name: "Over-unders", Description: "Two by twenty."}

func overUnders() []workout.WorkoutStep {
	return []workout.WorkoutStep{
		pw("Warm up", workout.IntensityWarmup, 600, 100, 160),
		repeat(3, "Over-unders",
			steady("Over", 240, 260),
			steady("Under", 120, 215)),
		pw("Cool down", workout.IntensityCooldown, 300, 100, 130),
	}
}

func TestZwoOverUndersGolden(t *testing.T) {
	got, err := Zwo(overUnders(), 250, meta)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "overunders.zwo", got)
}

func TestZwoWarmupCooldownDirection(t *testing.T) {
	// A warmup runs low to high, a cooldown high to low; PowerLow is the power
	// at the start and PowerHigh the power at the end in both elements.
	got, err := Zwo([]workout.WorkoutStep{
		pw("w", workout.IntensityWarmup, 600, 100, 160),
		pw("c", workout.IntensityCooldown, 300, 100, 160), // stored low..high, ridden high..low
	}, 200, meta)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		`<Warmup Duration="600" PowerLow="0.500" PowerHigh="0.800">`,
		`<Cooldown Duration="300" PowerLow="0.800" PowerHigh="0.500">`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
}

func TestZwoSweetSpotGolden(t *testing.T) {
	steps := []workout.WorkoutStep{
		pw("Warm up", workout.IntensityWarmup, 900, 125, 175),
		steady("Sweet spot 1", 1200, 225),
		steady("Rest", 300, 125),
		pw("Sweet spot 2", workout.IntensityActive, 1200, 220, 230), // range -> midpoint 225
		pw("Cool down", workout.IntensityCooldown, 600, 125, 175),
	}
	got, err := Zwo(steps, 250, Meta{Name: "Sweet spot", Description: ""})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "sweetspot.zwo", got)
	if strings.Count(string(got), `Power="0.900"`) != 2 {
		t.Errorf("range did not collapse to its midpoint:\n%s", got)
	}
}

func totalSeconds(doc string) int {
	// Parse generically: sum Duration, and Repeat*(On+Off) for IntervalsT.
	dec := xml.NewDecoder(strings.NewReader(doc))
	total := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		a := map[string]int{}
		for _, at := range se.Attr {
			n := 0
			for _, c := range at.Value {
				if c < '0' || c > '9' {
					n = -1
					break
				}
				n = n*10 + int(c-'0')
			}
			if n >= 0 {
				a[at.Name.Local] = n
			}
		}
		if se.Name.Local == "IntervalsT" {
			total += a["Repeat"] * (a["OnDuration"] + a["OffDuration"])
		} else {
			total += a["Duration"]
		}
	}
	return total
}

func TestZwoRepeatUnrolledWhenNotTwoPowerChildren(t *testing.T) {
	three := repeat(2, "Three", steady("a", 60, 200), steady("b", 30, 150), steady("c", 45, 100))
	nested := repeat(2, "Outer", repeat(2, "Inner", steady("x", 60, 220), steady("y", 30, 120)), steady("rest", 90, 100))
	withOpen := repeat(2, "Open", steady("on", 60, 250), open("free", 60))
	rampKid := repeat(2, "Ramp", pw("up", workout.IntensityWarmup, 60, 100, 200), steady("off", 60, 100))

	for name, tc := range map[string]struct {
		step  workout.WorkoutStep
		total int
	}{
		"three":  {three, 2 * (60 + 30 + 45)},
		"nested": {nested, 2 * (2*(60+30) + 90)},
		"open":   {withOpen, 240},
		"ramp":   {rampKid, 240},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Zwo([]workout.WorkoutStep{tc.step}, 250, meta)
			if err != nil {
				t.Fatal(err)
			}
			s := string(got)
			if strings.Contains(s, "IntervalsT") {
				t.Errorf("must unroll, got IntervalsT:\n%s", s)
			}
			if n := totalSeconds(s); n != tc.total {
				t.Errorf("total seconds = %d, want %d", n, tc.total)
			}
		})
	}
	// The unrolled three-child block is golden: it is the shape that matters.
	got, err := Zwo([]workout.WorkoutStep{three}, 250, meta)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "unrolled.zwo", got)
}

func TestZwoIntervalsTExactShape(t *testing.T) {
	got, err := Zwo([]workout.WorkoutStep{repeat(5, "V", steady("on", 180, 300), steady("off", 120, 125))}, 250, meta)
	if err != nil {
		t.Fatal(err)
	}
	want := `<IntervalsT Repeat="5" OnDuration="180" OffDuration="120" OnPower="1.200" OffPower="0.500"/>`
	if !strings.Contains(string(got), want) {
		t.Errorf("missing %s in\n%s", want, got)
	}
	if strings.Contains(string(got), "textevent") {
		t.Errorf("no textevent inside IntervalsT:\n%s", got)
	}
}

func TestZwoOpenStepIsFreeRide(t *testing.T) {
	got, err := Zwo([]workout.WorkoutStep{open("", 1200)}, 250, meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `<FreeRide Duration="1200"/>`) {
		t.Errorf("want FreeRide:\n%s", got)
	}
	// A cadence-only step is an open step.
	got, err = Zwo([]workout.WorkoutStep{{Intensity: workout.IntensityActive, Duration: workout.DurationTime,
		Seconds: 120, Target: workout.TargetCadence, TargetLow: 90, TargetHigh: 100}}, 250, meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `<FreeRide Duration="120"/>`) {
		t.Errorf("cadence-only step must be FreeRide:\n%s", got)
	}
}

func TestZwoRampTestStaysSteady(t *testing.T) {
	var steps []workout.WorkoutStep
	for i := 0; i < 5; i++ {
		steps = append(steps, steady("", 60, 125+float64(i)*25))
	}
	got, err := Zwo(steps, 250, Meta{Name: "Ramp test"})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(got), "<SteadyState "); n != 5 {
		t.Errorf("SteadyState count = %d, want 5", n)
	}
	if strings.Contains(string(got), "<Ramp") {
		t.Errorf("must not emit Ramp:\n%s", got)
	}
	golden(t, "ramptest.zwo", got)
}

func TestZwoTextEvent(t *testing.T) {
	got, err := Zwo([]workout.WorkoutStep{steady("Hold it", 300, 200), steady("", 60, 100)}, 200, meta)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, `<textevent timeoffset="0" message="Hold it"/>`) {
		t.Errorf("named step needs a textevent:\n%s", s)
	}
	if n := strings.Count(s, "textevent"); n != 1 {
		t.Errorf("unnamed step must have none, got %d mentions", n)
	}
}

func TestZwoEscaping(t *testing.T) {
	name := `Tom & "Jerry" <5'> Zoë ☃`
	got, err := Zwo([]workout.WorkoutStep{steady(name, 60, 200)}, 200, Meta{Name: name, Description: name})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name        string `xml:"name"`
		Description string `xml:"description"`
		Workout     struct {
			Steady struct {
				Text struct {
					Message string `xml:"message,attr"`
				} `xml:"textevent"`
			} `xml:"SteadyState"`
		} `xml:"workout"`
	}
	if err := xml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("output is not well-formed XML: %v\n%s", err, got)
	}
	if doc.Name != name || doc.Description != name || doc.Workout.Steady.Text.Message != name {
		t.Errorf("round trip lost text: %+v", doc)
	}
	if strings.Contains(string(got), `message="Tom & `) {
		t.Errorf("unescaped ampersand:\n%s", got)
	}
}

func TestZwoRefusals(t *testing.T) {
	hr := workout.WorkoutStep{Name: "z2", Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetHeartRate, TargetLow: 120, TargetHigh: 140}
	pace := workout.WorkoutStep{Name: "run", Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetPace, TargetLow: 3, TargetHigh: 3}
	for name, st := range map[string]workout.WorkoutStep{"hr": hr, "pace": pace} {
		if _, err := Zwo([]workout.WorkoutStep{steady("a", 60, 200), st}, 250, meta); !errors.Is(err, ErrHRTarget) {
			t.Errorf("%s: err = %v, want ErrHRTarget", name, err)
		}
		// Inside a repeat block too, and before the FTP check: the rider is told
		// the workout cannot be power, not only that FTP is missing.
		if _, err := Zwo([]workout.WorkoutStep{repeat(2, "r", st)}, 0, meta); !errors.Is(err, ErrHRTarget) {
			t.Errorf("%s in repeat, no ftp: err = %v, want ErrHRTarget", name, err)
		}
	}
	for _, ftp := range []float64{0, -5} {
		if _, err := Zwo([]workout.WorkoutStep{steady("a", 60, 200)}, ftp, meta); !errors.Is(err, ErrNoFTP) {
			t.Errorf("ftp %v: err = %v, want ErrNoFTP", ftp, err)
		}
	}
	dist := workout.WorkoutStep{Duration: workout.DurationDistance, Meters: 1000, Target: workout.TargetPower, TargetLow: 100, TargetHigh: 100}
	if _, err := Zwo([]workout.WorkoutStep{dist}, 250, meta); !errors.Is(err, ErrNotTimed) {
		t.Errorf("distance step: err = %v, want ErrNotTimed", err)
	}
	if _, err := Zwo(nil, 250, meta); !errors.Is(err, ErrEmpty) {
		t.Errorf("no steps: err = %v, want ErrEmpty", err)
	}
}

func TestZwoFractionsAndWholeSeconds(t *testing.T) {
	got, err := Zwo([]workout.WorkoutStep{steady("a", 59.6, 233.3333)}, 300, meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `Duration="60" Power="0.778"`) {
		t.Errorf("want whole seconds and 3-decimal fraction:\n%s", got)
	}
}
