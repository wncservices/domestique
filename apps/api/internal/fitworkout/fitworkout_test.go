package fitworkout

import (
	"bytes"
	"testing"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/typedef"
)

func exampleSteps() []Step {
	return []Step{
		{Name: "Warmup", Intensity: "warmup", Duration: DurationTime, Seconds: 600, Target: TargetOpen},
		{
			Name: "Intervals", Repeat: 3,
			Steps: []Step{
				{Name: "On", Intensity: "interval", Duration: DurationTime, Seconds: 180,
					Target: TargetPower, TargetLow: 280, TargetHigh: 300},
				{Name: "Off", Intensity: "recovery", Duration: DurationTime, Seconds: 120,
					Target: TargetPower, TargetLow: 100, TargetHigh: 150},
			},
		},
		{Name: "Cooldown", Intensity: "cooldown", Duration: DurationOpen, Target: TargetOpen},
	}
}

func decodeWorkout(t *testing.T, raw []byte) *filedef.Workout {
	t.Helper()
	fitFile, err := decoder.New(bytes.NewReader(raw)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return filedef.NewWorkout(fitFile.Messages...)
}

func TestEncodeRoundTrip(t *testing.T) {
	raw, err := Encode(exampleSteps(), Options{Name: "Threshold 6x3", Sport: typedef.SportCycling})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	wkt := decodeWorkout(t, raw)
	if wkt.Workout == nil {
		t.Fatal("no workout message")
	}
	if got := wkt.Workout.WktName; got != "Threshold 6x3" {
		t.Errorf("name = %q", got)
	}
	if wkt.Workout.Sport != typedef.SportCycling {
		t.Errorf("sport = %v", wkt.Workout.Sport)
	}

	// Warmup, On, Off, the repeat marker, Cooldown — 5 flattened steps for
	// 3 top-level entries, one of which is a 2-step repeat block.
	if got, want := len(wkt.WorkoutSteps), 5; got != want {
		t.Fatalf("step count = %d, want %d", got, want)
	}
	if int(wkt.Workout.NumValidSteps) != len(wkt.WorkoutSteps) {
		t.Errorf("num_valid_steps = %d, want %d to match the actual step count",
			wkt.Workout.NumValidSteps, len(wkt.WorkoutSteps))
	}

	warmup := wkt.WorkoutSteps[0]
	if warmup.DurationType != typedef.WktStepDurationTime {
		t.Errorf("warmup duration type = %v", warmup.DurationType)
	}
	if warmup.DurationValue != 600*1000 {
		t.Errorf("warmup duration value = %d, want %d (600s in ms)", warmup.DurationValue, 600*1000)
	}
	if warmup.TargetType != typedef.WktStepTargetOpen {
		t.Errorf("warmup target type = %v", warmup.TargetType)
	}

	on := wkt.WorkoutSteps[1]
	if on.TargetType != typedef.WktStepTargetPower {
		t.Errorf("on target type = %v", on.TargetType)
	}
	if want := uint32(280) + uint32(typedef.WorkoutPowerWattsOffset); on.CustomTargetValueLow != want {
		t.Errorf("on target low = %d, want %d (280W + offset)", on.CustomTargetValueLow, want)
	}
	if want := uint32(300) + uint32(typedef.WorkoutPowerWattsOffset); on.CustomTargetValueHigh != want {
		t.Errorf("on target high = %d, want %d (300W + offset)", on.CustomTargetValueHigh, want)
	}

	// The repeat marker is the 4th flattened step (index 3): Warmup(0),
	// On(1), Off(2), repeat(3), Cooldown(4).
	repeat := wkt.WorkoutSteps[3]
	if repeat.DurationType != typedef.WktStepDurationRepeatUntilStepsCmplt {
		t.Fatalf("repeat duration type = %v", repeat.DurationType)
	}
	if repeat.DurationValue != 1 {
		t.Errorf("repeat points back to message_index %d, want 1 (the first child step, On)", repeat.DurationValue)
	}
	if repeat.TargetValue != 3 {
		t.Errorf("repeat count = %d, want 3", repeat.TargetValue)
	}

	cooldown := wkt.WorkoutSteps[4]
	if cooldown.DurationType != typedef.WktStepDurationOpen {
		t.Errorf("cooldown duration type = %v", cooldown.DurationType)
	}
	if cooldown.DurationValue != basetype.Uint32Invalid {
		t.Errorf("cooldown duration value = %d, want it left invalid for an open duration", cooldown.DurationValue)
	}
}

func TestEncodeHeartRateAndPaceTargets(t *testing.T) {
	raw, err := Encode([]Step{
		{Name: "Tempo HR", Duration: DurationTime, Seconds: 1200,
			Target: TargetHeartRate, TargetLow: 150, TargetHigh: 160},
		{Name: "Tempo pace", Duration: DurationDistance, Meters: 5000,
			Target: TargetPace, TargetLow: 3.5, TargetHigh: 3.8},
		{Name: "Cadence drill", Duration: DurationTime, Seconds: 300,
			Target: TargetCadence, TargetLow: 95, TargetHigh: 100},
	}, Options{Sport: typedef.SportRunning})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	wkt := decodeWorkout(t, raw)
	hr, pace, cadence := wkt.WorkoutSteps[0], wkt.WorkoutSteps[1], wkt.WorkoutSteps[2]

	if hr.TargetType != typedef.WktStepTargetHeartRate {
		t.Errorf("hr target type = %v", hr.TargetType)
	}
	if want := uint32(150) + uint32(typedef.WorkoutHrBpmOffset); hr.CustomTargetValueLow != want {
		t.Errorf("hr low = %d, want %d", hr.CustomTargetValueLow, want)
	}

	if pace.TargetType != typedef.WktStepTargetSpeed {
		t.Errorf("pace target type = %v, want speed (FIT has no separate pace target)", pace.TargetType)
	}
	if pace.CustomTargetValueLow != 3500 {
		t.Errorf("pace low = %d, want 3500 (3.5 m/s * 1000)", pace.CustomTargetValueLow)
	}
	if pace.DurationType != typedef.WktStepDurationDistance || pace.DurationValue != 5000*100 {
		t.Errorf("pace duration = type %v value %d", pace.DurationType, pace.DurationValue)
	}

	if cadence.TargetType != typedef.WktStepTargetCadence {
		t.Errorf("cadence target type = %v", cadence.TargetType)
	}
	if cadence.CustomTargetValueLow != 95 {
		t.Errorf("cadence low = %d, want 95 (no offset)", cadence.CustomTargetValueLow)
	}
}

func TestEncodeDefaultsTheName(t *testing.T) {
	raw, err := Encode(exampleSteps(), Options{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	wkt := decodeWorkout(t, raw)
	if wkt.Workout.WktName != "Workout" {
		t.Errorf("name = %q, want default", wkt.Workout.WktName)
	}
	if wkt.Workout.Sport != typedef.SportCycling {
		t.Errorf("sport = %v, want default cycling", wkt.Workout.Sport)
	}
}

func TestEncodeRejectsNoSteps(t *testing.T) {
	if _, err := Encode(nil, Options{}); err == nil {
		t.Fatal("expected an error for no steps")
	}
}

func TestEncodeRejectsATimeStepWithNoSeconds(t *testing.T) {
	_, err := Encode([]Step{{Name: "Bad", Duration: DurationTime, Target: TargetOpen}}, Options{})
	if err == nil {
		t.Fatal("expected an error for a time step with no seconds")
	}
}

func TestEncodeRejectsATargetWithNoRange(t *testing.T) {
	_, err := Encode([]Step{{Name: "Bad", Duration: DurationOpen, Target: TargetPower}}, Options{})
	if err == nil {
		t.Fatal("expected an error for a power target with no low/high")
	}
}

func TestEncodeRejectsAnEmptyRepeatBlock(t *testing.T) {
	_, err := Encode([]Step{{Name: "Intervals", Repeat: 4}}, Options{})
	if err == nil {
		t.Fatal("expected an error for a repeat block with no steps")
	}
}

func TestEncodeSwapsAnInvertedRange(t *testing.T) {
	raw, err := Encode([]Step{
		{Name: "Backwards", Duration: DurationTime, Seconds: 60, Target: TargetPower, TargetLow: 300, TargetHigh: 200},
	}, Options{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	step := decodeWorkout(t, raw).WorkoutSteps[0]
	if step.CustomTargetValueLow >= step.CustomTargetValueHigh {
		t.Errorf("low %d should be below high %d after swapping", step.CustomTargetValueLow, step.CustomTargetValueHigh)
	}
}

func TestSportFromString(t *testing.T) {
	if SportFromString("running") != typedef.SportRunning {
		t.Error(`SportFromString("running") should be running`)
	}
	if SportFromString("cycling") != typedef.SportCycling {
		t.Error(`SportFromString("cycling") should default to cycling`)
	}
	if SportFromString("") != typedef.SportCycling {
		t.Error(`SportFromString("") should default to cycling`)
	}
}

// ---------- independent format checks ----------
//
// The round-trip tests above prove the library agrees with itself. These
// check the bytes against the FIT specification directly — the same two-track
// approach internal/fitcourse's own test file takes, and for the same
// reason: a bug in the library, or a wrong assumption about how to drive it,
// should not be able to pass unnoticed by only ever checking its own output
// against itself.

func TestFileHeaderMatchesTheSpec(t *testing.T) {
	raw, err := Encode(exampleSteps(), Options{Name: "Header check"})
	if err != nil {
		t.Fatal(err)
	}

	if len(raw) < 14 {
		t.Fatalf("file is %d bytes; a header alone is 12 or 14", len(raw))
	}

	headerSize := int(raw[0])
	if headerSize != 12 && headerSize != 14 {
		t.Errorf("header size = %d, want 12 or 14", headerSize)
	}

	// Bytes 8..11 are the ASCII signature ".FIT".
	if got := string(raw[8:12]); got != ".FIT" {
		t.Errorf("signature = %q, want .FIT", got)
	}

	// Bytes 4..7 are the data size: everything after the header except the
	// trailing 2-byte CRC.
	dataSize := int(raw[4]) | int(raw[5])<<8 | int(raw[6])<<16 | int(raw[7])<<24
	if want := len(raw) - headerSize - 2; dataSize != want {
		t.Errorf("header data size = %d, want %d", dataSize, want)
	}
}

// crc16FIT is the CRC the FIT spec defines, implemented here from the spec's
// table so it is not the library's own implementation checking its own
// output — the identical table internal/fitcourse's own test file uses.
func crc16FIT(data []byte) uint16 {
	table := [16]uint16{
		0x0000, 0xCC01, 0xD801, 0x1400, 0xF001, 0x3C00, 0x2800, 0xE401,
		0xA001, 0x6C00, 0x7800, 0xB401, 0x5000, 0x9C01, 0x8801, 0x4400,
	}

	var crc uint16
	for _, b := range data {
		check := crc & 0xF
		crc >>= 4
		crc = crc ^ table[check] ^ table[b&0xF]

		check = crc & 0xF
		crc >>= 4
		crc = crc ^ table[check] ^ table[(b>>4)&0xF]
	}
	return crc
}

func TestTrailingCRCIsCorrect(t *testing.T) {
	raw, err := Encode(exampleSteps(), Options{Name: "CRC check"})
	if err != nil {
		t.Fatal(err)
	}

	body := raw[:len(raw)-2]
	want := crc16FIT(body)
	got := uint16(raw[len(raw)-2]) | uint16(raw[len(raw)-1])<<8

	if got != want {
		t.Errorf("trailing CRC = %#04x, computed %#04x — a device would reject this file", got, want)
	}
}
